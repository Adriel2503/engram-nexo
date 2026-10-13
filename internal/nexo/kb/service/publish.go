package service

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/ingest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/source"
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
	ContentBase64 string        `json:"content_base64,omitempty"` // formatos binarios (docx)
	Title         string        `json:"title,omitempty"`
	Category      string        `json:"category,omitempty"`
	Tags          []string      `json:"tags,omitempty"`
	Owner         string        `json:"owner,omitempty"`
	ValidFrom     *time.Time    `json:"valid_from,omitempty"`
	ValidUntil    *time.Time    `json:"valid_until,omitempty"`
	Origin        domain.Origin `json:"origin"`
	ChangeNote    string        `json:"change_note,omitempty"`

	// Internos (subida y propuestas, NX-10 2b): no vienen del JSON.
	raw        []byte   // contenido ya leído (evita base64)
	extraTags  []string // etiquetas de la fuente (se suman a las del archivo)
	retire     bool     // publicar y dejar retirado (KP-11)
	retireNote string
	proposalID string // aprobación de una propuesta (C-07)
	titleWins  bool   // el título de la fuente manda (wiki: nombre de la página, no su primer título)
}

// PruneSpec activa la poda por origen (RF-13). Solo con el lote completo:
// Total debe coincidir con los documentos del pedido o, si el cliente envió
// el lote en varias páginas, con Keys (todas las claves del origen). Confirm
// permite superar el tope de seguridad (KP-01).
type PruneSpec struct {
	OriginKind string   `json:"origin_kind"` // git (por defecto)
	OriginRef  string   `json:"origin_ref"`  // repositorio (org/repo) o colección subida
	Total      int      `json:"total"`
	Keys       []string `json:"keys,omitempty"`
	Confirm    bool     `json:"confirm,omitempty"`
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
	Title    string `json:"title,omitempty"`
	Status   string `json:"status"` // created | updated | unchanged | rejected | retired | skipped
	Version  int    `json:"version,omitempty"`
	Sections int    `json:"sections,omitempty"`
	Code     string `json:"error_code,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Note     string `json:"note,omitempty"` // estado sugerido por la fuente (retirado, vencido)
}

// BatchResult es la respuesta del lote.
type BatchResult struct {
	Space   string         `json:"space"`
	DryRun  bool           `json:"dry_run"`
	Results []DocResult    `json:"results"`
	Pruned  []DocResult    `json:"pruned"`
	Summary map[string]int `json:"summary"`
	// PruneSkipped explica por qué no se podó (lote parcial, documentos sin
	// clave, errores internos, tope de seguridad).
	PruneSkipped string `json:"prune_skipped,omitempty"`
	// NX-10 2b: fuente de una subida, archivos no publicados y registro de la
	// ejecución (nexo_kb_publish_runs, KP-02).
	Source  string           `json:"source,omitempty"`
	Skipped []source.Skipped `json:"skipped,omitempty"`
	RunID   string           `json:"run_id,omitempty"`
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
	retire     bool
	retireNote string
	proposalID string
}

// PublishBatch publica un lote (design §6.1): una transacción por documento
// (un documento malo no aborta el lote), idempotente por hash, con poda
// opcional por origen y modo dry-run sin escrituras. Cada lote queda en
// nexo_kb_publish_runs (KP-02).
func (s *Service) PublishBatch(ctx context.Context, id authz.Identity, slug string, req BatchRequest) (BatchResult, error) {
	started := time.Now().UTC()
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
	res := s.publishAll(ctx, id, sp, req)
	s.recordRun(ctx, id, sp, runInfo{kind: "batch", started: started, origin: firstOrigin(req.Documents), dryRun: req.DryRun, prune: req.Prune}, &res)
	return res, nil
}

// publishAll publica los documentos ya autorizados (lote o subida).
func (s *Service) publishAll(ctx context.Context, id authz.Identity, sp domain.Space, req BatchRequest) BatchResult {
	res := BatchResult{Space: sp.Slug, DryRun: req.DryRun, Results: make([]DocResult, 0, len(req.Documents)), Pruned: []DocResult{}, Summary: map[string]int{}}
	keys := map[string]bool{}
	keyless, internal := false, false
	for _, d := range req.Documents {
		if err := ctx.Err(); err != nil {
			internal = true
			res.Results = append(res.Results, DocResult{DocKey: d.DocKey, Path: d.Path, Status: domain.ResultRejected, Code: "internal_error", Reason: err.Error()})
			continue
		}
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
	return res
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
	switch {
	case d.raw != nil:
		raw = d.raw
	case d.ContentBase64 != "":
		b, err := base64.StdEncoding.DecodeString(d.ContentBase64)
		if err != nil {
			return nil, reject(key, d.Path, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "content_base64 is not valid base64"))
		}
		raw = b
	default:
		raw = []byte(d.Content)
	}
	name := d.Path
	if name == "" {
		name = key
	}
	format := d.Format
	if format == "" && d.MIME == "" && path.Ext(name) == "" {
		format = domain.FormatMarkdown
	}
	if conv, ok := s.reg.Lookup(format, d.MIME, name); ok && textOnly(conv.Format()) && int64(len(raw)) > s.limits.MaxDocBytes {
		return nil, reject(key, d.Path, domain.Errorf(domain.KindTooLarge, domain.CodeTooLarge, "document is %d bytes; the limit is %d", len(raw), s.limits.MaxDocBytes))
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
	p := &prepared{key: key, path: d.Path, raw: raw, conv: conv, origin: d.Origin, changeNote: strings.TrimSpace(d.ChangeNote),
		retire: d.retire, retireNote: d.retireNote, proposalID: d.proposalID}
	p.title = firstNonEmpty(m.Title, d.Title, key)
	if d.titleWins && strings.TrimSpace(d.Title) != "" {
		p.title = strings.TrimSpace(d.Title)
	}
	p.category = strings.ToLower(firstNonEmpty(m.Category, d.Category))
	p.tags = d.Tags
	if len(m.Tags) > 0 {
		p.tags = m.Tags
	}
	p.tags = normTags(append(append([]string{}, p.tags...), d.extraTags...))
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
	if o.Kind == domain.OriginProposal && p.proposalID == "" {
		return nil, reject(key, d.Path, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "origin proposal is reserved for approved proposals"))
	}
	// RC-08: enlace a la fuente declarado en el front matter (source_url).
	if o.SourceURL == "" {
		if v, ok := m.Extra["source_url"].(string); ok {
			o.SourceURL = cleanSourceURL(v)
		}
	} else {
		o.SourceURL = cleanSourceURL(o.SourceURL)
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

// textOnly: el límite de NEXO_KB_MAX_DOC_BYTES se aplica a la entrada de los
// formatos de texto; los binarios (docx) tienen el suyo (ingest.Limits).
func textOnly(format string) bool {
	return format == domain.FormatMarkdown || format == domain.FormatText || format == ingest.FormatADOMarkdown
}

// cleanSourceURL acepta solo enlaces http(s) cortos (se muestran como texto,
// nunca se siguen en el servidor).
func cleanSourceURL(u string) string {
	u = strings.TrimSpace(u)
	if len(u) > 2048 || !(strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://")) || strings.ContainsAny(u, " \t\r\n<>\"") {
		return ""
	}
	return u
}

func (s *Service) resolveUsername(ctx context.Context, owner string) string {
	var id string
	if err := s.db.QueryRowContext(ctx, `SELECT principal_id::text FROM cloud_human_users WHERE username = $1`, strings.TrimSpace(owner)).Scan(&id); err != nil {
		return ""
	}
	return id
}

// metaHash cubre los metadatos de la versión. valid_from solo cuenta si se
// declaró (el valor por defecto, "ahora", cambiaría en cada reintento). La
// ruta de títulos y el enlace a la fuente (2b) solo cuentan si vienen, así
// los documentos publicados antes no cambian de hash.
func metaHash(p *prepared) string {
	m := map[string]any{"title": p.title, "category": p.category, "tags": p.tags, "owner": p.owner}
	if p.validFrom != nil {
		m["valid_from"] = p.validFrom.UTC().Format(time.RFC3339Nano)
	}
	if p.validUntil != nil {
		m["valid_until"] = p.validUntil.UTC().Format(time.RFC3339Nano)
	}
	if len(p.origin.PathTitles) > 0 {
		m["path_titles"] = p.origin.PathTitles
	}
	if p.origin.SourceURL != "" {
		m["source_url"] = p.origin.SourceURL
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
		same := found && cur.ContentSHA256 == p.contentSHA && cur.MetaSHA256 == p.metaSHA
		switch {
		case !found:
			return DocResult{DocKey: p.key, Path: p.path, Title: p.title, Status: domain.ResultCreated, Version: 1, Sections: len(p.sections), Note: p.retireNote}, nil
		case same && (cur.Status == domain.DocActive) != p.retire:
			return DocResult{DocKey: p.key, Path: p.path, Title: p.title, Status: domain.ResultUnchanged, Version: cur.CurrentVersion, Note: p.retireNote}, nil
		case same && p.retire:
			return DocResult{DocKey: p.key, Path: p.path, Title: p.title, Status: domain.ResultRetired, Version: cur.CurrentVersion, Note: p.retireNote}, nil
		default:
			return DocResult{DocKey: p.key, Path: p.path, Title: p.title, Status: domain.ResultUpdated, Version: cur.CurrentVersion + 1, Sections: len(p.sections), Note: p.retireNote}, nil
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
		r, err := s.writeVersion(ctx, tx, id, sp, p)
		out = r
		return err
	})
	return out, err
}

// writeVersion escribe la versión de un documento preparado dentro de tx
// (lo usan la publicación y la aprobación de propuestas).
func (s *Service) writeVersion(ctx context.Context, tx *sql.Tx, id authz.Identity, sp domain.Space, p *prepared) (DocResult, error) {
	now, err := store.Now(ctx, tx)
	if err != nil {
		return DocResult{}, err
	}
	cur, found, err := store.GetDocument(ctx, tx, sp.ID, p.key, true)
	if err != nil {
		return DocResult{}, err
	}
	if found && cur.ContentSHA256 == p.contentSHA && cur.MetaSHA256 == p.metaSHA {
		switch {
		case cur.Status == domain.DocActive && !p.retire:
			// idempotente: sin versión ni evento (RF-11)
			return DocResult{DocKey: p.key, Path: p.path, Title: p.title, Status: domain.ResultUnchanged, Version: cur.CurrentVersion}, nil
		case cur.Status == domain.DocRetired && p.retire:
			return DocResult{DocKey: p.key, Path: p.path, Title: p.title, Status: domain.ResultUnchanged, Version: cur.CurrentVersion, Note: p.retireNote}, nil
		case cur.Status == domain.DocActive && p.retire:
			// Mismo contenido, ahora marcado para retirar por la fuente.
			if err := s.retireInTx(ctx, tx, id, sp, cur.ID, p.key, cur.CurrentVersion, p.retireNote, now); err != nil {
				return DocResult{}, err
			}
			return DocResult{DocKey: p.key, Path: p.path, Title: p.title, Status: domain.ResultRetired, Version: cur.CurrentVersion, Note: p.retireNote}, nil
		}
		// Retirado sin marca de retiro: se reactiva con una versión nueva (D-I14).
	}
	validFrom := now
	if p.validFrom != nil {
		validFrom = p.validFrom.UTC()
	}
	if p.validUntil != nil && !p.validUntil.After(validFrom) {
		return DocResult{}, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "valid_until must be after valid_from (%s)", validFrom.Format(time.RFC3339))
	}
	docID, version, status := store.NewID(), 1, domain.ResultCreated
	if found {
		docID, version, status = cur.ID, cur.CurrentVersion+1, domain.ResultUpdated
	}
	blobRef, err := s.blobs.Put(ctx, p.conv.SourceSHA256, p.conv.MIME, p.raw)
	if err != nil {
		return DocResult{}, fmt.Errorf("kb: blob store: %w", err)
	}
	dw := store.DocWrite{ID: docID, SpaceID: sp.ID, DocKey: p.key, Title: p.title, Category: p.category, Tags: p.tags,
		OwnerLabel: p.owner, OwnerPrincipalID: p.ownerID, ValidFrom: validFrom, ValidUntil: p.validUntil, Version: version,
		OriginKind: p.origin.Kind, OriginRef: p.origin.Ref()}
	if found {
		if err := store.CloseCurrentSections(ctx, tx, docID, validFrom, now); err != nil {
			return DocResult{}, err
		}
		if err := store.UpdateDocumentVersion(ctx, tx, dw, now); err != nil {
			return DocResult{}, err
		}
	} else if err := store.InsertDocument(ctx, tx, dw, now); err != nil {
		return DocResult{}, err
	}
	versionID := store.NewID()
	v := store.VersionWrite{SpaceID: sp.ID, MetaSHA256: p.metaSHA, Actor: actorOf(id)}
	v.ID, v.DocumentID, v.Version.Version = versionID, docID, version
	v.Title, v.Category, v.Tags, v.OwnerLabel = p.title, p.category, p.tags, p.owner
	v.ContentMD, v.ContentSHA256 = p.conv.Markdown, p.contentSHA
	v.SourceFormat, v.SourceMIME, v.SourceSHA256, v.SourceSizeBytes, v.SourceBlobRef = p.conv.Format, p.conv.MIME, p.conv.SourceSHA256, p.conv.SourceSize, string(blobRef)
	v.Origin, v.ChangeNote, v.ValidFrom, v.ValidUntil, v.SectionCount, v.PublishedAt = p.origin, p.changeNote, validFrom, p.validUntil, len(p.sections), now
	if err := store.InsertVersion(ctx, tx, v); err != nil {
		return DocResult{}, err
	}
	isCurrent := p.validUntil == nil || p.validUntil.After(now)
	if err := s.st.InsertSections(ctx, tx, store.SectionWrite{SpaceID: sp.ID, DocumentID: docID, VersionID: versionID, Category: p.category,
		Tags: p.tags, ValidFrom: validFrom, ValidUntil: p.validUntil, RecordedFrom: now, IsCurrent: isCurrent, Sections: p.sections,
		Title: p.title, PathTitles: p.origin.PathTitles}); err != nil {
		return DocResult{}, err
	}
	if err := store.InsertChange(ctx, tx, sp.ID, docID, domain.ChangeUpsert, now); err != nil {
		return DocResult{}, err
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
	if p.proposalID != "" {
		details["proposal_id"] = p.proposalID
	}
	if err := store.InsertEvent(ctx, tx, store.EventWrite{SpaceID: sp.ID, SpaceSlug: sp.Slug, Action: domain.ActionDocumentPublished,
		TargetKind: "document", TargetID: p.key, Actor: actorOf(id), Details: details, At: &now}); err != nil {
		return DocResult{}, err
	}
	out := DocResult{DocKey: p.key, Path: p.path, Title: p.title, Status: status, Version: version, Sections: len(p.sections)}
	if p.retire {
		if err := s.retireInTx(ctx, tx, id, sp, docID, p.key, version, p.retireNote, now); err != nil {
			return DocResult{}, err
		}
		out.Status, out.Note = domain.ResultRetired, p.retireNote
	} else if p.validUntil != nil && !p.validUntil.After(now) {
		out.Note = "published as expired (valid until " + p.validUntil.Format("2006-01-02") + ")"
	}
	return out, nil
}

// retireInTx retira un documento recién publicado por indicación de la fuente
// (KP-11): mismas filas que un retiro manual, con motivo source_hint.
func (s *Service) retireInTx(ctx context.Context, tx *sql.Tx, id authz.Identity, sp domain.Space, docID, docKey string, version int, note string, now time.Time) error {
	if err := store.CloseCurrentSections(ctx, tx, docID, now, now); err != nil {
		return err
	}
	if err := store.RetireDocument(ctx, tx, docID, "", note, now); err != nil {
		return err
	}
	if err := store.InsertChange(ctx, tx, sp.ID, docID, domain.ChangeRemove, now); err != nil {
		return err
	}
	return store.InsertEvent(ctx, tx, store.EventWrite{SpaceID: sp.ID, SpaceSlug: sp.Slug, Action: domain.ActionDocumentRetired, TargetKind: "document",
		TargetID: docKey, Actor: actorOf(id), Details: map[string]any{"doc_key": docKey, "version": version, "note": note, "reason": "source_hint"}, At: &now})
}

// ─── Poda (RF-13) con tope de seguridad (KP-01) ──────────────────────────────

// Variables del tope de la poda.
const (
	EnvPruneMaxPercent = "NEXO_KB_PRUNE_MAX_PERCENT" // por defecto 25
	EnvPruneMinDocs    = "NEXO_KB_PRUNE_MIN_DOCS"    // por defecto 5: hasta esta cantidad no aplica el porcentaje
)

func pruneCap() (pct, minDocs int) {
	pct, minDocs = 25, 5
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(EnvPruneMaxPercent))); err == nil && n >= 0 && n <= 100 {
		pct = n
	}
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(EnvPruneMinDocs))); err == nil && n >= 0 {
		minDocs = n
	}
	return pct, minDocs
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
	var gone []store.DocRow
	for _, d := range docs {
		if !present[d.DocKey] {
			gone = append(gone, d)
		}
	}
	// KP-01: no retirar más de un porcentaje de los documentos activos del
	// origen en una ejecución sin confirmación explícita (un lote mal armado
	// no vacía el espacio).
	pct, minDocs := pruneCap()
	if len(gone) > minDocs && len(gone)*100 > pct*len(docs) && !ps.Confirm {
		for _, d := range gone {
			res.Pruned = append(res.Pruned, DocResult{DocKey: d.DocKey, Status: domain.ResultRejected, Version: d.CurrentVersion, Code: domain.CodePruneCap,
				Reason: "would be retired (not present in " + ps.OriginRef + "); prune cap exceeded"})
		}
		return fmt.Sprintf("%s: the prune would retire %d of %d active documents of %s (more than %d%%); send prune.confirm=true (CLI --confirm-prune) to proceed",
			domain.CodePruneCap, len(gone), len(docs), ps.OriginRef, pct)
	}
	for _, d := range gone {
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

func firstOrigin(docs []PublishDoc) domain.Origin {
	if len(docs) == 0 {
		return domain.Origin{}
	}
	o := docs[0].Origin
	o.Path, o.Filename, o.PathTitles, o.SourceURL, o.Order = "", "", nil, "", 0
	return o
}
