package service

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/ingest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/store"
)

// PublishDoc es un documento de un lote (RF-10, RF-12). Los metadatos del
// pedido son valores por defecto: el front matter del archivo los reemplaza.
type PublishDoc struct {
	DocKey        string        `json:"doc_key,omitempty"`
	Path          string        `json:"path,omitempty"` // ruta relativa (doc_key por defecto, conversor por extensión)
	Format        string        `json:"format,omitempty"`
	MIME          string        `json:"mime,omitempty"`
	Content       string        `json:"content,omitempty"`        // texto del archivo (md, txt), con front matter
	ContentBase64 string        `json:"content_base64,omitempty"` // formatos binarios (K2/K3)
	Title         string        `json:"title,omitempty"`
	Category      string        `json:"category,omitempty"`
	Tags          []string      `json:"tags,omitempty"`
	Owner         string        `json:"owner,omitempty"`
	ValidFrom     *time.Time    `json:"valid_from,omitempty"`
	ValidUntil    *time.Time    `json:"valid_until,omitempty"`
	Origin        domain.Origin `json:"origin"`
	ChangeNote    string        `json:"change_note,omitempty"`
}

// PruneSpec activa la poda por origen (RF-13). Solo con el lote completo:
// Total debe coincidir con los documentos del pedido o, si el cliente envió
// el lote en varias páginas, con Keys (todas las claves del origen).
type PruneSpec struct {
	OriginKind string   `json:"origin_kind"` // git (por defecto)
	OriginRef  string   `json:"origin_ref"`  // repositorio (org/repo)
	Total      int      `json:"total"`
	Keys       []string `json:"keys,omitempty"`
}

// BatchRequest es el cuerpo de POST /spaces/{slug}/documents:batch.
type BatchRequest struct {
	Documents []PublishDoc `json:"documents"`
	DryRun    bool         `json:"dry_run"`
	Prune     *PruneSpec   `json:"prune,omitempty"`
}

// DocResult es el resultado por documento.
type DocResult struct {
	DocKey   string `json:"doc_key"`
	Path     string `json:"path,omitempty"`
	Status   string `json:"status"` // created | updated | unchanged | rejected | retired
	Version  int    `json:"version,omitempty"`
	Sections int    `json:"sections,omitempty"`
	Code     string `json:"error_code,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// BatchResult es la respuesta del lote.
type BatchResult struct {
	Space   string         `json:"space"`
	DryRun  bool           `json:"dry_run"`
	Results []DocResult    `json:"results"`
	Pruned  []DocResult    `json:"pruned"`
	Summary map[string]int `json:"summary"`
	// PruneSkipped explica por qué no se podó (lote parcial, documentos sin
	// clave, errores internos).
	PruneSkipped string `json:"prune_skipped,omitempty"`
}

// prepared es un documento validado y convertido, listo para escribir.
type prepared struct {
	key        string
	path       string
	title      string
	category   string
	tags       []string
	owner      string
	ownerID    string
	validFrom  *time.Time
	validUntil *time.Time
	origin     domain.Origin
	changeNote string
	conv       ingest.Result
	raw        []byte
	contentSHA string
	metaSHA    string
	sections   []ingest.SectionDraft
}

// PublishBatch publica un lote (design §6.1): una transacción por documento
// (un documento malo no aborta el lote), idempotente por hash, con poda
// opcional por origen y modo dry-run sin escrituras.
func (s *Service) PublishBatch(ctx context.Context, id authz.Identity, slug string, req BatchRequest) (BatchResult, error) {
	sp, err := s.authorize(ctx, id, slug, authz.CapSpacePublish, map[string]any{"action": "documents.batch", "documents": len(req.Documents), "dry_run": req.DryRun})
	if err != nil {
		return BatchResult{}, err
	}
	if len(req.Documents) == 0 && req.Prune == nil {
		return BatchResult{}, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "documents is empty")
	}
	if len(req.Documents) > s.limits.MaxBatch {
		return BatchResult{}, domain.Errorf(domain.KindTooLarge, domain.CodeTooLarge, "a batch accepts at most %d documents (got %d); send several requests", s.limits.MaxBatch, len(req.Documents))
	}
	res := BatchResult{Space: sp.Slug, DryRun: req.DryRun, Results: make([]DocResult, 0, len(req.Documents)), Pruned: []DocResult{}, Summary: map[string]int{}}
	keys := map[string]bool{}
	keyless, internal := false, false
	for _, d := range req.Documents {
		p, rej := s.prepare(ctx, sp, d)
		if rej != nil {
			res.Results = append(res.Results, *rej)
			if rej.DocKey == "" {
				keyless = true
			} else {
				keys[rej.DocKey] = true
			}
			continue
		}
		if keys[p.key] {
			res.Results = append(res.Results, DocResult{DocKey: p.key, Path: p.path, Status: domain.ResultRejected, Code: domain.CodeConflict, Reason: "duplicate doc_key in the batch"})
			continue
		}
		keys[p.key] = true
		r, err := s.publishOne(ctx, id, sp, p, req.DryRun)
		if err != nil {
			if de := (*domain.Error)(nil); errors.As(err, &de) {
				res.Results = append(res.Results, DocResult{DocKey: p.key, Path: p.path, Status: domain.ResultRejected, Code: de.Code, Reason: de.Message})
				continue
			}
			internal = true
			res.Results = append(res.Results, DocResult{DocKey: p.key, Path: p.path, Status: domain.ResultRejected, Code: "internal_error", Reason: err.Error()})
			continue
		}
		res.Results = append(res.Results, r)
	}
	if req.Prune != nil {
		if reason := s.prune(ctx, id, sp, req, keys, keyless, internal, &res); reason != "" {
			res.PruneSkipped = reason
		}
	}
	for _, r := range res.Results {
		res.Summary[r.Status]++
	}
	res.Summary[domain.ResultRetired] += len(res.Pruned)
	return res, nil
}

func reject(key, p string, err error) *DocResult {
	r := &DocResult{DocKey: key, Path: p, Status: domain.ResultRejected, Code: domain.CodeInvalid, Reason: err.Error()}
	var de *domain.Error
	if errors.As(err, &de) {
		r.Code, r.Reason = de.Code, de.Message
	}
	return r
}

// DocKeyFromPath: ruta relativa sin extensión, con barras normales.
func DocKeyFromPath(p string) string {
	p = strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
	p = strings.TrimPrefix(path.Clean("/"+p), "/")
	if p == "" || p == "." {
		return ""
	}
	return strings.TrimSuffix(p, path.Ext(p))
}

func (s *Service) prepare(ctx context.Context, sp domain.Space, d PublishDoc) (*prepared, *DocResult) {
	key := strings.TrimSpace(d.DocKey)
	if key == "" {
		key = DocKeyFromPath(d.Path)
	}
	var raw []byte
	if d.ContentBase64 != "" {
		b, err := base64.StdEncoding.DecodeString(d.ContentBase64)
		if err != nil {
			return nil, reject(key, d.Path, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "content_base64 is not valid base64"))
		}
		raw = b
	} else {
		raw = []byte(d.Content)
	}
	if int64(len(raw)) > s.limits.MaxDocBytes {
		return nil, reject(key, d.Path, domain.Errorf(domain.KindTooLarge, domain.CodeTooLarge, "document is %d bytes; the limit is %d", len(raw), s.limits.MaxDocBytes))
	}
	name := d.Path
	if name == "" {
		name = key
	}
	format := d.Format
	if format == "" && d.MIME == "" && path.Ext(name) == "" {
		format = domain.FormatMarkdown
	}
	conv, err := s.reg.Convert(ctx, format, ingest.Input{Name: name, MIME: d.MIME, Data: raw}, s.limits.Convert)
	if err != nil {
		return nil, reject(key, d.Path, err)
	}
	m := conv.Meta
	if m.ID != "" {
		key = strings.TrimSpace(m.ID)
	}
	if !domain.ValidDocKey(key) {
		return nil, reject(key, d.Path, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "doc_key %q is invalid (letters, digits, . _ - /; up to 200)", key))
	}
	p := &prepared{key: key, path: d.Path, raw: raw, conv: conv, origin: d.Origin, changeNote: strings.TrimSpace(d.ChangeNote)}
	p.title = firstNonEmpty(m.Title, d.Title, key)
	p.category = strings.ToLower(firstNonEmpty(m.Category, d.Category))
	p.tags = d.Tags
	if len(m.Tags) > 0 {
		p.tags = m.Tags
	}
	p.tags = normTags(p.tags)
	p.owner = firstNonEmpty(m.Owner, d.Owner)
	p.validFrom, p.validUntil = d.ValidFrom, d.ValidUntil
	if m.ValidFrom != nil {
		p.validFrom = m.ValidFrom
	}
	if m.ValidUntil != nil {
		p.validUntil = m.ValidUntil
	}
	if p.category == "" {
		return nil, reject(key, d.Path, domain.Errorf(domain.KindInvalid, domain.CodeCategory, "category is required (one of %s)", strings.Join(sp.Categories, ", ")))
	}
	if !slices.Contains(sp.Categories, p.category) {
		return nil, reject(key, d.Path, domain.Errorf(domain.KindInvalid, domain.CodeCategory, "category %q is not allowed in space %s (allowed: %s)", p.category, sp.Slug, strings.Join(sp.Categories, ", ")))
	}
	if p.validFrom != nil && p.validUntil != nil && !p.validUntil.After(*p.validFrom) {
		return nil, reject(key, d.Path, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "valid_until must be after valid_from"))
	}
	if strings.TrimSpace(conv.Markdown) == "" {
		return nil, reject(key, d.Path, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "document has no content"))
	}
	if found := ingest.DetectSecrets(conv.Markdown + "\n" + p.title); len(found) > 0 {
		kinds := make([]string, 0, len(found))
		for _, f := range found {
			kinds = append(kinds, fmt.Sprintf("%s (line %d)", f.Kind, f.Line))
		}
		return nil, reject(key, d.Path, domain.Errorf(domain.KindInvalid, domain.CodeSecret, "the document contains evident secrets (RN-K4): %s", strings.Join(kinds, ", ")))
	}
	// Origen: la clase se deduce si no viene; lo declarado es referencia (RN-K5).
	o := &p.origin
	o.Kind = strings.ToLower(strings.TrimSpace(o.Kind))
	if o.Kind == "" {
		switch {
		case o.Repo != "":
			o.Kind = domain.OriginGit
		case o.Filename != "":
			o.Kind = domain.OriginUpload
		default:
			o.Kind = domain.OriginManual
		}
	}
	if o.Kind == domain.OriginGit && strings.TrimSpace(o.Repo) == "" {
		return nil, reject(key, d.Path, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "origin.repo is required for git origin"))
	}
	if o.Kind == domain.OriginGit && o.Path == "" {
		o.Path = d.Path
	}
	if o.Kind == domain.OriginProposal {
		return nil, reject(key, d.Path, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "origin proposal is reserved for approved proposals"))
	}
	if p.owner != "" {
		p.ownerID = s.resolveUsername(ctx, p.owner)
	}
	p.contentSHA = ingest.SHA256Hex(conv.Markdown)
	p.metaSHA = metaHash(p)
	p.sections = ingest.Section(conv.Blocks, s.sections)
	if len(p.sections) == 0 {
		return nil, reject(key, d.Path, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "document has no sections"))
	}
	return p, nil
}

func (s *Service) resolveUsername(ctx context.Context, owner string) string {
	var id string
	if err := s.db.QueryRowContext(ctx, `SELECT principal_id::text FROM cloud_human_users WHERE username = $1`, strings.TrimSpace(owner)).Scan(&id); err != nil {
		return ""
	}
	return id
}

// metaHash cubre los metadatos de la versión. valid_from solo cuenta si se
// declaró (el valor por defecto, "ahora", cambiaría en cada reintento).
func metaHash(p *prepared) string {
	m := map[string]any{"title": p.title, "category": p.category, "tags": p.tags, "owner": p.owner}
	if p.validFrom != nil {
		m["valid_from"] = p.validFrom.UTC().Format(time.RFC3339Nano)
	}
	if p.validUntil != nil {
		m["valid_until"] = p.validUntil.UTC().Format(time.RFC3339Nano)
	}
	raw, _ := json.Marshal(m)
	return ingest.SHA256Hex(string(raw))
}

func (s *Service) publishOne(ctx context.Context, id authz.Identity, sp domain.Space, p *prepared, dryRun bool) (DocResult, error) {
	if dryRun {
		cur, found, err := store.GetDocument(ctx, s.db, sp.ID, p.key, false)
		if err != nil {
			return DocResult{}, err
		}
		switch {
		case !found:
			return DocResult{DocKey: p.key, Path: p.path, Status: domain.ResultCreated, Version: 1, Sections: len(p.sections)}, nil
		case cur.Status == domain.DocActive && cur.ContentSHA256 == p.contentSHA && cur.MetaSHA256 == p.metaSHA:
			return DocResult{DocKey: p.key, Path: p.path, Status: domain.ResultUnchanged, Version: cur.CurrentVersion}, nil
		default:
			return DocResult{DocKey: p.key, Path: p.path, Status: domain.ResultUpdated, Version: cur.CurrentVersion + 1, Sections: len(p.sections)}, nil
		}
	}
	var out DocResult
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		out, err = s.publishTx(ctx, id, sp, p)
		var pgErr *pgconn.PgError
		if err != nil && errors.As(err, &pgErr) && pgErr.Code == "23505" && attempt == 0 {
			continue // otro publicador creó el mismo documento a la vez: reintentar sobre su fila
		}
		break
	}
	return out, err
}

func (s *Service) publishTx(ctx context.Context, id authz.Identity, sp domain.Space, p *prepared) (DocResult, error) {
	var out DocResult
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		now, err := store.Now(ctx, tx)
		if err != nil {
			return err
		}
		cur, found, err := store.GetDocument(ctx, tx, sp.ID, p.key, true)
		if err != nil {
			return err
		}
		if found && cur.Status == domain.DocActive && cur.ContentSHA256 == p.contentSHA && cur.MetaSHA256 == p.metaSHA {
			out = DocResult{DocKey: p.key, Path: p.path, Status: domain.ResultUnchanged, Version: cur.CurrentVersion}
			return nil // idempotente: sin versión ni evento (RF-11)
		}
		validFrom := now
		if p.validFrom != nil {
			validFrom = p.validFrom.UTC()
		}
		if p.validUntil != nil && !p.validUntil.After(validFrom) {
			return domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "valid_until must be after valid_from (%s)", validFrom.Format(time.RFC3339))
		}
		docID, version, status := store.NewID(), 1, domain.ResultCreated
		if found {
			docID, version, status = cur.ID, cur.CurrentVersion+1, domain.ResultUpdated
		}
		blobRef, err := s.blobs.Put(ctx, p.conv.SourceSHA256, p.conv.MIME, p.raw)
		if err != nil {
			return fmt.Errorf("kb: blob store: %w", err)
		}
		dw := store.DocWrite{ID: docID, SpaceID: sp.ID, DocKey: p.key, Title: p.title, Category: p.category, Tags: p.tags,
			OwnerLabel: p.owner, OwnerPrincipalID: p.ownerID, ValidFrom: validFrom, ValidUntil: p.validUntil, Version: version,
			OriginKind: p.origin.Kind, OriginRef: p.origin.Ref()}
		if found {
			if err := store.CloseCurrentSections(ctx, tx, docID, validFrom, now); err != nil {
				return err
			}
			if err := store.UpdateDocumentVersion(ctx, tx, dw, now); err != nil {
				return err
			}
		} else if err := store.InsertDocument(ctx, tx, dw, now); err != nil {
			return err
		}
		versionID := store.NewID()
		v := store.VersionWrite{SpaceID: sp.ID, MetaSHA256: p.metaSHA, Actor: actorOf(id)}
		v.ID, v.DocumentID, v.Version.Version = versionID, docID, version
		v.Title, v.Category, v.Tags, v.OwnerLabel = p.title, p.category, p.tags, p.owner
		v.ContentMD, v.ContentSHA256 = p.conv.Markdown, p.contentSHA
		v.SourceFormat, v.SourceMIME, v.SourceSHA256, v.SourceSizeBytes, v.SourceBlobRef = p.conv.Format, p.conv.MIME, p.conv.SourceSHA256, p.conv.SourceSize, string(blobRef)
		v.Origin, v.ChangeNote, v.ValidFrom, v.ValidUntil, v.SectionCount, v.PublishedAt = p.origin, p.changeNote, validFrom, p.validUntil, len(p.sections), now
		if err := store.InsertVersion(ctx, tx, v); err != nil {
			return err
		}
		isCurrent := p.validUntil == nil || p.validUntil.After(now)
		if err := s.st.InsertSections(ctx, tx, store.SectionWrite{SpaceID: sp.ID, DocumentID: docID, VersionID: versionID, Category: p.category,
			Tags: p.tags, ValidFrom: validFrom, ValidUntil: p.validUntil, RecordedFrom: now, IsCurrent: isCurrent, Sections: p.sections}); err != nil {
			return err
		}
		if err := store.InsertChange(ctx, tx, sp.ID, docID, domain.ChangeUpsert, now); err != nil {
			return err
		}
		details := map[string]any{"doc_key": p.key, "version": version, "result": status, "content_sha256": p.contentSHA,
			"source_sha256": p.conv.SourceSHA256, "source_format": p.conv.Format, "sections": len(p.sections), "origin": p.origin,
			"valid_from": validFrom, "title": p.title, "category": p.category}
		if p.validUntil != nil {
			details["valid_until"] = *p.validUntil
		}
		if found {
			details["previous_version"] = cur.CurrentVersion
			if cur.Status == domain.DocRetired {
				details["reactivated"] = true
			}
		}
		if err := store.InsertEvent(ctx, tx, store.EventWrite{SpaceID: sp.ID, SpaceSlug: sp.Slug, Action: domain.ActionDocumentPublished,
			TargetKind: "document", TargetID: p.key, Actor: actorOf(id), Details: details, At: &now}); err != nil {
			return err
		}
		out = DocResult{DocKey: p.key, Path: p.path, Status: status, Version: version, Sections: len(p.sections)}
		return nil
	})
	return out, err
}

// prune retira los documentos activos del mismo origen que no vinieron en el
// lote (RF-13). Devuelve el motivo si no podó.
func (s *Service) prune(ctx context.Context, id authz.Identity, sp domain.Space, req BatchRequest, keys map[string]bool, keyless, internal bool, res *BatchResult) string {
	ps := *req.Prune
	ps.OriginKind = strings.ToLower(strings.TrimSpace(ps.OriginKind))
	if ps.OriginKind == "" {
		ps.OriginKind = domain.OriginGit
	}
	ps.OriginRef = strings.TrimSpace(ps.OriginRef)
	if ps.OriginRef == "" {
		return "prune.origin_ref is required"
	}
	if ps.OriginKind == domain.OriginProposal || ps.OriginKind == domain.OriginManual {
		return "prune only applies to git, upload or import origins"
	}
	present := keys
	if len(ps.Keys) > 0 {
		if len(ps.Keys) != ps.Total {
			return fmt.Sprintf("partial batch: prune.keys has %d keys but prune.total is %d", len(ps.Keys), ps.Total)
		}
		present = map[string]bool{}
		for _, k := range ps.Keys {
			present[strings.TrimSpace(k)] = true
		}
		for k := range keys {
			present[k] = true
		}
	} else if ps.Total != len(req.Documents) {
		return fmt.Sprintf("partial batch: prune.total is %d but the request has %d documents (send prune.keys with every key)", ps.Total, len(req.Documents))
	}
	if keyless {
		return "a document of the batch has no doc_key: prune skipped to avoid retiring it"
	}
	if internal {
		return "a document of the batch failed with an internal error: prune skipped"
	}
	docs, err := store.ActiveDocsByOrigin(ctx, s.db, sp.ID, ps.OriginKind, ps.OriginRef)
	if err != nil {
		return "prune failed: " + err.Error()
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].DocKey < docs[j].DocKey })
	for _, d := range docs {
		if present[d.DocKey] {
			continue
		}
		if req.DryRun {
			res.Pruned = append(res.Pruned, DocResult{DocKey: d.DocKey, Status: domain.ResultRetired, Version: d.CurrentVersion, Reason: "not present in " + ps.OriginRef})
			continue
		}
		_, err := s.retire(ctx, id, sp, d.DocKey, "", "pruned: not present in "+ps.OriginRef, map[string]any{"reason": "prune", "origin_ref": ps.OriginRef})
		if err != nil {
			res.Pruned = append(res.Pruned, DocResult{DocKey: d.DocKey, Status: domain.ResultRejected, Code: "internal_error", Reason: err.Error()})
			continue
		}
		res.Pruned = append(res.Pruned, DocResult{DocKey: d.DocKey, Status: domain.ResultRetired, Version: d.CurrentVersion, Reason: "not present in " + ps.OriginRef})
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

func normTags(tags []string) []string {
	out := []string{}
	for _, t := range tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t != "" && !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}
