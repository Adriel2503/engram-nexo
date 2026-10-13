package service

// Nexo IA · NX-10 entrega 2b — subida de archivos sueltos o de una carpeta
// (zip) a un espacio (HU-29, decisiones del 2026-10-13): una sola API que usan
// la CLI (`engram nexo kb upload`) y el panel. Reutiliza la publicación por
// lotes (idempotencia por hash, versiones, poda opcional, dry-run) a través de
// publishAll; los archivos pasan por una fuente (dir o azure-devops-wiki) y
// por el registro de conversores (md, txt, docx, ado-md). No se guardan los
// originales (D-K11): mejorar un conversor exige volver a subir.

import (
	"context"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/ingest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/ingest/docx"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/source"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/store"
)

// DefaultRegistry es el registro de conversores del servidor y la CLI:
// Markdown, texto, wiki de Azure DevOps y Word (.docx, entrega 2b).
func DefaultRegistry() *ingest.Registry {
	r := ingest.DefaultRegistry()
	r.Register(docx.Converter{})
	return r
}

// Variables de entorno de la subida.
const (
	EnvUploadMaxBytes     = "NEXO_KB_UPLOAD_MAX_BYTES"      // cuerpo de la subida (256 MB)
	EnvUploadMaxFiles     = "NEXO_KB_UPLOAD_MAX_FILES"      // documentos indexables por subida (2 000)
	EnvUploadMaxFileBytes = "NEXO_KB_UPLOAD_MAX_FILE_BYTES" // por archivo (20 MB)
	EnvUploadMaxUnzipped  = "NEXO_KB_UPLOAD_MAX_UNZIPPED"   // descomprimido indexable de un zip (64 MB)
	EnvUploadMaxEntries   = "NEXO_KB_UPLOAD_MAX_ENTRIES"    // entradas de un zip (10 000)
)

// UploadLimits son los límites de una subida.
type UploadLimits struct {
	MaxBytes     int64
	MaxFiles     int
	MaxFileBytes int64
	MaxUnzipped  int64
	MaxEntries   int
}

// UploadLimitsFromEnv lee los límites (valores inválidos: por defecto).
func UploadLimitsFromEnv() UploadLimits {
	l := UploadLimits{MaxBytes: 256 << 20, MaxFiles: 2000, MaxFileBytes: 20 << 20, MaxUnzipped: 64 << 20, MaxEntries: 10000}
	i64 := func(env string, dst *int64) {
		if n, err := strconv.ParseInt(strings.TrimSpace(os.Getenv(env)), 10, 64); err == nil && n > 0 {
			*dst = n
		}
	}
	i := func(env string, dst *int) {
		if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(env))); err == nil && n > 0 {
			*dst = n
		}
	}
	i64(EnvUploadMaxBytes, &l.MaxBytes)
	i(EnvUploadMaxFiles, &l.MaxFiles)
	i64(EnvUploadMaxFileBytes, &l.MaxFileBytes)
	i64(EnvUploadMaxUnzipped, &l.MaxUnzipped)
	i(EnvUploadMaxEntries, &l.MaxEntries)
	return l
}

// ZipLimits arma los límites del lector de zip con el registro de conversores.
func (s *Service) ZipLimits() source.ZipLimits {
	u := s.upload
	reg := s.reg
	return source.ZipLimits{MaxEntries: u.MaxEntries, MaxFileBytes: u.MaxFileBytes, MaxTotalBytes: u.MaxUnzipped, MaxRatio: 100,
		IndexableSuffix: func(name string) bool { return reg.Supported(name) }}
}

// UploadLimits devuelve los límites de subida.
func (s *Service) UploadLimits() UploadLimits { return s.upload }

// CanPublish comprueba space.publish antes de leer una subida (la denegación
// queda auditada como en el resto de las rutas).
func (s *Service) CanPublish(ctx context.Context, id authz.Identity, slug string) (domain.Space, error) {
	return s.authorize(ctx, id, slug, authz.CapSpacePublish, map[string]any{"action": "documents.upload"})
}

// UploadRequest es una subida ya leída (archivos en memoria).
type UploadRequest struct {
	Files      []source.File
	Source     string // auto (por defecto), dir, azure-devops-wiki
	Collection string // nombre de la colección (referencia de la poda): zip o carpeta
	WikiURL    string // URL de la wiki de Azure DevOps (enlaces RC-08)
	Category   string
	Owner      string
	Tags       []string
	ChangeNote string
	DryRun     bool
	Prune      bool
	Confirm    bool // supera el tope de la poda (KP-01)
	// Datos de la subida para el registro (KP-02); nunca el contenido.
	UploadNames  []string
	UploadBytes  int64
	UploadSHA256 string
	Skipped      []source.Skipped // avisos de la lectura (zip)
}

// Upload publica los archivos subidos (space.publish).
func (s *Service) Upload(ctx context.Context, id authz.Identity, slug string, req UploadRequest) (BatchResult, error) {
	started := time.Now().UTC()
	sp, err := s.authorize(ctx, id, slug, authz.CapSpacePublish, map[string]any{"action": "documents.upload", "files": len(req.Files), "dry_run": req.DryRun})
	if err != nil {
		return BatchResult{}, err
	}
	walked, err := source.Walk(ctx, req.Files, source.Options{Kind: req.Source, Registry: s.reg, WikiURL: req.WikiURL})
	if err != nil {
		return BatchResult{}, err
	}
	if len(walked.Docs) > s.upload.MaxFiles {
		return BatchResult{}, domain.Errorf(domain.KindTooLarge, domain.CodeTooLarge, "the upload has %d indexable files; the limit is %d", len(walked.Docs), s.upload.MaxFiles)
	}
	if len(walked.Docs) == 0 && !req.Prune {
		return BatchResult{}, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "no indexable files in the upload (accepted: .md, .txt, .docx)")
	}
	collection := strings.TrimSpace(req.Collection)
	if collection == "" {
		collection = "upload"
	}
	if len(collection) > 200 {
		collection = collection[:200]
	}
	category := s.defaultCategory(sp, req.Category)
	docs := make([]PublishDoc, 0, len(walked.Docs))
	for _, d := range walked.Docs {
		pd := PublishDoc{DocKey: d.DocKey, Path: d.Path, Format: d.Format, Title: d.Title, Category: category, Owner: req.Owner,
			Tags: req.Tags, ValidFrom: d.Hint.ValidFrom, ValidUntil: d.Hint.ValidUntil, ChangeNote: req.ChangeNote,
			Origin: domain.Origin{Kind: domain.OriginUpload, Filename: d.Path, Source: collection, PathTitles: d.PathTitles, SourceURL: d.SourceURL, Order: d.Order},
			raw:    d.Data, extraTags: d.Hint.Tags, retire: d.Hint.Retire, retireNote: d.Hint.Note, titleWins: walked.Kind == source.KindADO}
		if !d.Hint.Retire && d.Hint.ValidUntil != nil {
			pd.ChangeNote = firstNonEmpty(pd.ChangeNote, d.Hint.Note)
		}
		docs = append(docs, pd)
	}
	breq := BatchRequest{Documents: docs, DryRun: req.DryRun}
	if req.Prune {
		breq.Prune = &PruneSpec{OriginKind: domain.OriginUpload, OriginRef: collection, Total: len(docs), Confirm: req.Confirm}
	}
	res := s.publishAll(ctx, id, sp, breq)
	res.Source = walked.Kind
	res.Skipped = append(append([]source.Skipped{}, req.Skipped...), walked.Skipped...)
	for _, sk := range res.Skipped {
		if strings.HasSuffix(sk.Path, ".md") && strings.Contains(sk.Reason, "empty") {
			res.Summary[domain.ResultSkipped]++
		}
	}
	upload := map[string]any{"files": len(req.Files), "bytes": req.UploadBytes, "sha256": req.UploadSHA256, "collection": collection, "source": walked.Kind}
	if len(req.UploadNames) > 0 {
		names := req.UploadNames
		if len(names) > 20 {
			names = names[:20]
		}
		upload["names"] = names
	}
	s.recordRun(ctx, id, sp, runInfo{kind: "upload", started: started, source: walked.Kind, dryRun: req.DryRun, prune: breq.Prune,
		origin: domain.Origin{Kind: domain.OriginUpload, Source: collection}, upload: upload}, &res)
	return res, nil
}

// defaultCategory: la pedida, la del espacio (settings.default_category),
// "lineamiento" si el espacio la admite o la primera del espacio.
func (s *Service) defaultCategory(sp domain.Space, requested string) string {
	if c := strings.ToLower(strings.TrimSpace(requested)); c != "" {
		return c
	}
	if v, ok := sp.Settings["default_category"].(string); ok && slices.Contains(sp.Categories, v) {
		return v
	}
	if slices.Contains(sp.Categories, "lineamiento") {
		return "lineamiento"
	}
	if len(sp.Categories) > 0 {
		return sp.Categories[0]
	}
	return ""
}

// ─── Registro de ejecuciones (KP-02) ─────────────────────────────────────────

type runInfo struct {
	kind    string
	started time.Time
	source  string
	origin  domain.Origin
	dryRun  bool
	prune   *PruneSpec
	upload  map[string]any
}

// recordRun deja la ejecución en nexo_kb_publish_runs y un evento resumen
// publish.run (mejor esfuerzo: la publicación ya ocurrió y tiene sus eventos
// por documento).
func (s *Service) recordRun(ctx context.Context, id authz.Identity, sp domain.Space, ri runInfo, res *BatchResult) {
	runID := store.NewID()
	var rejected []any
	for _, r := range res.Results {
		if r.Status == domain.ResultRejected && len(rejected) < 100 {
			rejected = append(rejected, map[string]any{"doc_key": r.DocKey, "path": r.Path, "error_code": r.Code, "reason": truncate(r.Reason, 300)})
		}
	}
	origin := map[string]any{"kind": ri.origin.Kind}
	for k, v := range map[string]string{"repo": ri.origin.Repo, "branch": ri.origin.Branch, "commit": ri.origin.Commit, "ci_run": ri.origin.CIRun,
		"author": ri.origin.Author, "source": ri.origin.Source} {
		if v != "" {
			origin[k] = v
		}
	}
	if ri.prune != nil {
		origin["prune"] = map[string]any{"origin_kind": ri.prune.OriginKind, "origin_ref": ri.prune.OriginRef, "confirm": ri.prune.Confirm}
	}
	w := store.PublishRunWrite{SpaceID: sp.ID, Act: actorOf(id)}
	w.ID, w.Space, w.Kind, w.DryRun, w.StartedAt = runID, sp.Slug, ri.kind, ri.dryRun, ri.started
	w.Source, w.Origin, w.Upload, w.Summary, w.Rejected = ri.source, origin, ri.upload, res.Summary, rejected
	w.Pruned, w.PruneSkipped, w.Skipped = len(res.Pruned), res.PruneSkipped, len(res.Skipped)
	recCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := store.InsertPublishRun(recCtx, s.db, w); err != nil {
		logf("[nexo] kb: no se pudo registrar la ejecución de publicación: %v", err)
		return
	}
	res.RunID = runID
	// El evento resumen solo si la ejecución cambió algo: reenviar un lote sin
	// cambios no deja eventos (RF-11) y un dry-run no escribe en el registro
	// del espacio (la fila de nexo_kb_publish_runs sí queda, para observar
	// cada corrida del CI).
	changed := res.Summary[domain.ResultCreated] + res.Summary[domain.ResultUpdated] + res.Summary[domain.ResultRetired] + res.Summary[domain.ResultRejected]
	if ri.dryRun || changed == 0 {
		return
	}
	details := map[string]any{"run_id": runID, "kind": ri.kind, "dry_run": ri.dryRun, "summary": res.Summary, "pruned": len(res.Pruned)}
	if res.PruneSkipped != "" {
		details["prune_skipped"] = truncate(res.PruneSkipped, 300)
	}
	if ri.source != "" {
		details["source"] = ri.source
	}
	_ = store.InsertEvent(recCtx, s.db, store.EventWrite{SpaceID: sp.ID, SpaceSlug: sp.Slug, Action: domain.ActionPublishRun, TargetKind: "publish_run",
		TargetID: runID, Actor: actorOf(id), Details: details})
}

// ListPublishRuns requiere space.audit.read (o platform.audit.read).
func (s *Service) ListPublishRuns(ctx context.Context, id authz.Identity, slug string, before *time.Time, limit int) ([]store.PublishRun, error) {
	sp, err := s.auditSpace(ctx, id, slug, "publish_runs.read")
	if err != nil {
		return nil, err
	}
	return store.ListPublishRuns(ctx, s.db, sp.ID, before, limit)
}

// auditSpace: space.audit.read en el espacio o platform.audit.read.
func (s *Service) auditSpace(ctx context.Context, id authz.Identity, slug, action string) (domain.Space, error) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if d, err := s.eng.Can(ctx, id, authz.PlatformScope(), authz.CapPlatformAuditRead); err == nil && d.Allowed && domain.ValidSlug(slug) {
		if sp, found, err := store.GetSpace(ctx, s.db, slug, false); err != nil {
			return domain.Space{}, err
		} else if found {
			return sp, nil
		}
	}
	return s.authorize(ctx, id, slug, authz.CapSpaceAuditRead, map[string]any{"action": action})
}

func truncate(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "..."
}
