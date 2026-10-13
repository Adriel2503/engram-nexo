package httpapi

// Nexo IA · NX-10 entrega 2b — rutas nuevas de /nexo/kb/v1:
//
//	POST /spaces/{slug}/uploads              space.publish (multipart: archivos sueltos o un zip con una carpeta)
//	GET  /spaces/{slug}/publish-runs         space.audit.read o platform.audit.read (KP-02)
//	POST /spaces/{slug}/proposals            space.propose (C-07, KP-08)
//	GET  /proposals[?space=&status=&mine=]   propias; todas las del espacio con space.review
//	GET  /proposals/{id}                     autor o space.review (404 si no)
//	POST /proposals/{id}:approve|:discard    space.review   (409 version_conflict salvo force)
//	POST /proposals/{id}:withdraw            autor
//	GET  /changes?since=&until=&spaces=&clock=   space.read (RC-27)
//	GET  /sync?since=&limit=                 identidad (C-09, KP-07)
//	POST /eval-runs                          space.publish en los espacios medidos (RC-28)
//	GET  /eval-runs[?set=&accepted=]         identidad con algún espacio visible
//	GET  /admin/index                       platform.settings (estado del índice)
//	POST /admin/index:reindex               platform.settings (reindexado seguro, RC-03)

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/constants"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/search"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/service"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/source"
)

// EnvPublicURL es la URL pública de Nexo para los enlaces permanentes de las
// citas (RC-08). Sin ella se usa el host de la petición.
const EnvPublicURL = "NEXO_KB_PUBLIC_URL"

func (a *api) register2b(h func(string, handler)) {
	h("POST "+Prefix+"/spaces/{slug}/uploads", a.upload)
	h("GET "+Prefix+"/spaces/{slug}/publish-runs", a.publishRuns)
	h("POST "+Prefix+"/spaces/{slug}/proposals", a.createProposal)
	h("GET "+Prefix+"/proposals", a.listProposals)
	h("GET "+Prefix+"/proposals/{id}", a.getProposal)
	h("POST "+Prefix+"/proposals/{idAction}", a.proposalAction)
	h("GET "+Prefix+"/changes", a.changes)
	h("GET "+Prefix+"/sync", a.sync)
	h("POST "+Prefix+"/eval-runs", a.recordEvalRun)
	h("GET "+Prefix+"/eval-runs", a.listEvalRuns)
	h("GET "+Prefix+"/admin/index", a.indexState)
	h("POST "+Prefix+"/admin/index:reindex", a.reindex)
}

// BaseURL devuelve la URL pública para los enlaces: NEXO_KB_PUBLIC_URL si
// está configurada; si no, el esquema y el host que informó un proxy
// confiable (WithForwardedOrigin, publicurl.go) o, sin proxy, los de la
// conexión.
func BaseURL(r *http.Request) string {
	if v := strings.TrimRight(strings.TrimSpace(os.Getenv(EnvPublicURL)), "/"); v != "" {
		return v
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	if o, ok := forwardedOriginFrom(r.Context()); ok {
		if o.scheme != "" {
			scheme = o.scheme
		}
		if o.host != "" {
			host = o.host
		}
	}
	if !validHost(host) {
		return ""
	}
	return scheme + "://" + host
}

// ─── Subida (HU-29) ──────────────────────────────────────────────────────────

// maxFieldBytes acota los campos de texto del formulario de subida.
const maxFieldBytes = 4096

// ParseUpload lee una subida multipart (lo usan la API y el panel): campos
// file/files (archivos sueltos; el nombre puede traer la ruta relativa de una
// carpeta) o zip (una carpeta comprimida) y las opciones. Los zip se pasan a
// un archivo temporal (nunca se descomprimen a disco) y se borran al terminar.
func ParseUpload(w http.ResponseWriter, r *http.Request, svc *service.Service) (service.UploadRequest, map[string]string, error) {
	lim := svc.UploadLimits()
	r.Body = http.MaxBytesReader(w, r.Body, lim.MaxBytes)
	mr, err := r.MultipartReader()
	if err != nil {
		return service.UploadRequest{}, nil, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "expected multipart/form-data: %v", err)
	}
	req := service.UploadRequest{}
	fields := map[string]string{}
	hasher := sha256.New()
	var total int64
	files := 0
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return req, fields, tooLargeOr(err, lim.MaxBytes)
		}
		name := part.FormName()
		filename := rawFilename(part.Header.Get("Content-Disposition"))
		if filename == "" {
			v, err := io.ReadAll(io.LimitReader(part, maxFieldBytes+1))
			_ = part.Close()
			if err != nil {
				return req, fields, tooLargeOr(err, lim.MaxBytes)
			}
			if len(v) > maxFieldBytes {
				return req, fields, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "field %q is too long", name)
			}
			if name != "" && name != "csrf_token" {
				fields[name] = strings.TrimSpace(string(v))
			}
			continue
		}
		files++
		clean := source.CleanPath(filename)
		isZip := name == "zip" || strings.EqualFold(path.Ext(filename), ".zip")
		if isZip {
			fs, skipped, n, err := readZipPart(r, part, svc, hasher)
			_ = part.Close()
			if err != nil {
				return req, fields, err
			}
			total += n
			req.Files = append(req.Files, fs...)
			req.Skipped = append(req.Skipped, skipped...)
			req.UploadNames = append(req.UploadNames, safeName(filename))
			if req.Collection == "" {
				req.Collection = strings.TrimSuffix(path.Base(clean), path.Ext(clean))
			}
			continue
		}
		data, err := io.ReadAll(io.LimitReader(io.TeeReader(part, hasher), lim.MaxFileBytes+1))
		_ = part.Close()
		if err != nil {
			return req, fields, tooLargeOr(err, lim.MaxBytes)
		}
		if int64(len(data)) > lim.MaxFileBytes {
			return req, fields, domain.Errorf(domain.KindTooLarge, domain.CodeTooLarge, "%s exceeds %d bytes", safeName(filename), lim.MaxFileBytes)
		}
		total += int64(len(data))
		if clean == "" {
			req.Skipped = append(req.Skipped, source.Skipped{Path: safeName(filename), Reason: "invalid file name (absolute, .. or control characters)"})
			continue
		}
		if files > lim.MaxFiles {
			return req, fields, domain.Errorf(domain.KindTooLarge, domain.CodeTooLarge, "the upload has more than %d files", lim.MaxFiles)
		}
		req.Files = append(req.Files, source.File{Path: clean, Data: data})
		if len(req.UploadNames) < 50 {
			req.UploadNames = append(req.UploadNames, clean)
		}
	}
	req.UploadBytes = total
	req.UploadSHA256 = hex.EncodeToString(hasher.Sum(nil))
	req.Source = fields["source"]
	if c := fields["collection"]; c != "" {
		req.Collection = c
	}
	req.WikiURL, req.Category, req.Owner, req.ChangeNote = fields["wiki_url"], fields["category"], fields["owner"], fields["change_note"]
	if t := fields["tags"]; t != "" {
		req.Tags = strings.Split(t, ",")
	}
	req.DryRun, req.Prune, req.Confirm = isTrue(fields["dry_run"]), isTrue(fields["prune"]), isTrue(fields["confirm_prune"])
	if len(req.Files) == 0 && !req.Prune {
		return req, fields, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "no files in the upload (fields file or zip)")
	}
	return req, fields, nil
}

func readZipPart(r *http.Request, part io.Reader, svc *service.Service, hasher hash.Hash) ([]source.File, []source.Skipped, int64, error) {
	tmp, err := os.CreateTemp("", "nexo-kb-upload-*.zip")
	if err != nil {
		return nil, nil, 0, err
	}
	defer func() { _ = tmp.Close(); _ = os.Remove(tmp.Name()) }()
	n, err := io.Copy(tmp, io.TeeReader(part, hasher))
	if err != nil {
		return nil, nil, 0, tooLargeOr(err, svc.UploadLimits().MaxBytes)
	}
	files, skipped, err := source.ReadZip(r.Context(), tmp, n, svc.ZipLimits())
	if errors.Is(err, source.ErrZipBomb) {
		return nil, nil, 0, domain.Errorf(domain.KindTooLarge, domain.CodeTooLarge, "%s", err.Error())
	}
	return files, skipped, n, err
}

// rawFilename lee filename del Content-Disposition sin quitar la ruta
// (multipart.Part.FileName se queda solo con el nombre base): las subidas de
// carpeta del navegador envían la ruta relativa. Se sanea después.
func rawFilename(cd string) string {
	_, params, err := mime.ParseMediaType(cd)
	if err != nil {
		return ""
	}
	return params["filename"]
}

func safeName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, s)
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func isTrue(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	return v == "1" || v == "true" || v == "on" || v == "yes"
}

func tooLargeOr(err error, max int64) error {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return domain.Errorf(domain.KindTooLarge, domain.CodeTooLarge, "upload exceeds %d bytes", max)
	}
	if de := (*domain.Error)(nil); errors.As(err, &de) {
		return err
	}
	return domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "invalid upload: %v", err)
}

func (a *api) upload(w http.ResponseWriter, r *http.Request, svc *service.Service, id authz.Identity) {
	// La autorización se decide antes de leer el cuerpo (un lector no sube
	// 256 MB para recibir un 403).
	if _, err := svc.CanPublish(r.Context(), id, r.PathValue("slug")); err != nil {
		writeErr(w, err)
		return
	}
	req, _, err := ParseUpload(w, r, svc)
	if err != nil {
		writeErr(w, err)
		return
	}
	if v := strings.TrimSpace(r.URL.Query().Get("dry_run")); isTrue(v) {
		req.DryRun = true
	}
	res, err := svc.Upload(r.Context(), id, r.PathValue("slug"), req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (a *api) publishRuns(w http.ResponseWriter, r *http.Request, svc *service.Service, id authz.Identity) {
	limit, err := limitParam(r)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	before, err := timeParam(r, "before")
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	runs, err := svc.ListPublishRuns(r.Context(), id, r.PathValue("slug"), before, limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": runs, "limit": limit})
}

// ─── Propuestas (C-07) ───────────────────────────────────────────────────────

func (a *api) createProposal(w http.ResponseWriter, r *http.Request, svc *service.Service, id authz.Identity) {
	var in service.ProposalInput
	if !a.decode(w, r, &in) {
		return
	}
	p, err := svc.CreateProposal(r.Context(), id, r.PathValue("slug"), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (a *api) listProposals(w http.ResponseWriter, r *http.Request, svc *service.Service, id authz.Identity) {
	q := r.URL.Query()
	limit, err := limitParam(r)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	before, err := timeParam(r, "before")
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	items, err := svc.ListProposals(r.Context(), id, service.ProposalQuery{Space: q.Get("space"), Status: q.Get("status"), Mine: isTrue(q.Get("mine")),
		Before: before, Limit: limit})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "limit": limit})
}

func (a *api) getProposal(w http.ResponseWriter, r *http.Request, svc *service.Service, id authz.Identity) {
	p, err := svc.GetProposal(r.Context(), id, r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

type proposalActionBody struct {
	service.ApproveInput
	Reason string `json:"reason"`
}

func (a *api) proposalAction(w http.ResponseWriter, r *http.Request, svc *service.Service, id authz.Identity) {
	pid, action := splitAction(r.PathValue("idAction"))
	var body proposalActionBody
	if r.ContentLength != 0 && !a.decode(w, r, &body) {
		return
	}
	var out any
	var err error
	switch action {
	case "approve":
		out, err = svc.ApproveProposal(r.Context(), id, pid, body.ApproveInput)
	case "discard":
		out, err = svc.DiscardProposal(r.Context(), id, pid, firstNonEmpty(body.Reason, body.Note))
	case "withdraw":
		out, err = svc.WithdrawProposal(r.Context(), id, pid, firstNonEmpty(body.Reason, body.Note))
	default:
		writeError(w, http.StatusNotFound, constants.UpgradeErrorClassRepairable, domain.CodeNotFound, "unknown proposal action: use {id}:approve, {id}:discard or {id}:withdraw")
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ─── Cambios (RC-27) y feed (C-09) ───────────────────────────────────────────

func (a *api) changes(w http.ResponseWriter, r *http.Request, svc *service.Service, id authz.Identity) {
	since, err := timeParam(r, "since")
	if err != nil || since == nil {
		badRequest(w, "since is required (YYYY-MM-DD or RFC 3339)")
		return
	}
	until, err := timeParam(r, "until")
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	limit := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if limit, err = strconv.Atoi(raw); err != nil || limit <= 0 {
			badRequest(w, "limit must be a positive integer (max 1000)")
			return
		}
	}
	res, err := svc.Changes(r.Context(), id, service.ChangesRequest{Since: *since, Until: until, Spaces: spacesParam(r), Clock: r.URL.Query().Get("clock"), Limit: limit})
	if err != nil {
		writeErr(w, err)
		return
	}
	base := BaseURL(r)
	out := make([]map[string]any, 0, len(res.Changes))
	for _, c := range res.Changes {
		v := c.VersionAfter
		if v == 0 {
			v = c.VersionBefore
		}
		out = append(out, map[string]any{"space": c.Space, "doc_key": c.DocKey, "title": c.Title, "anchor": c.Anchor, "heading_path": c.HeadingPath,
			"change": c.Change, "version_before": c.VersionBefore, "version_after": c.VersionAfter, "at": c.At, "document_status": c.DocStatus,
			"superseded_by": c.SupersededBy, "link": base + search.SectionPath(c.Space, c.DocKey, c.Anchor, v)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"since": res.Since, "until": res.Until, "clock": res.Clock, "changes": out, "summary": res.Summary,
		"truncated": res.Truncated, "no_changes": res.NoChanges})
}

func (a *api) sync(w http.ResponseWriter, r *http.Request, svc *service.Service, id authz.Identity) {
	var since int64
	if raw := strings.TrimSpace(r.URL.Query().Get("since")); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			badRequest(w, "since must be a non-negative integer (seq)")
			return
		}
		since = n
	}
	limit := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			badRequest(w, "limit must be a positive integer (max 500)")
			return
		}
		limit = n
	}
	res, err := svc.Sync(r.Context(), id, since, limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	res.PublicURL = BaseURL(r) // tanda 2: enlaces de la copia local (RC-08)
	writeJSON(w, http.StatusOK, res)
}

// ─── Evaluación (RC-28) ──────────────────────────────────────────────────────

func (a *api) recordEvalRun(w http.ResponseWriter, r *http.Request, svc *service.Service, id authz.Identity) {
	var in service.RecordEvalRunInput
	if !a.decode(w, r, &in) {
		return
	}
	run, err := svc.RecordEvalRun(r.Context(), id, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, run)
}

func (a *api) listEvalRuns(w http.ResponseWriter, r *http.Request, svc *service.Service, id authz.Identity) {
	limit, err := limitParam(r)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	runs, err := svc.ListEvalRuns(r.Context(), id, r.URL.Query().Get("set"), isTrue(r.URL.Query().Get("accepted")), limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": runs, "limit": limit})
}

// ─── Utilidades ──────────────────────────────────────────────────────────────

func spacesParam(r *http.Request) []string {
	var spaces []string
	for _, s := range r.URL.Query()["spaces"] {
		for _, part := range strings.Split(s, ",") {
			if part = strings.TrimSpace(part); part != "" {
				spaces = append(spaces, part)
			}
		}
	}
	return spaces
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}

// ─── Índice (RC-03: reindexado seguro) ───────────────────────────────────────

func (a *api) indexState(w http.ResponseWriter, r *http.Request, svc *service.Service, id authz.Identity) {
	st, err := svc.IndexState(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (a *api) reindex(w http.ResponseWriter, r *http.Request, svc *service.Service, id authz.Identity) {
	res, err := svc.Reindex(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
