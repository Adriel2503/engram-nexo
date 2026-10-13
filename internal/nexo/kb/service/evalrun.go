package service

// Nexo IA · NX-10 entrega 2b — medición en proceso (R0) y registro de
// corridas de evaluación (RC-28).

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/eval"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/ingest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/rerank"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/search"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/store"
)

// EvalRetriever evalúa el servicio en proceso: la misma búsqueda (con sus
// filtros de permisos y tiempo) que atiende la API.
type EvalRetriever struct {
	svc *Service
	id  authz.Identity
	// NoChanges desactiva la consulta de cambios (línea base anterior a
	// RC-27).
	NoChanges bool
	// Mode es el modo de las preguntas que no fijan uno (NX-10 R2: hybrid
	// para medir el modo híbrido; vacío = el del servidor).
	Mode string
	// Rerank (NX-10 R3): "on" u "off" para medir con o sin reordenamiento;
	// vacío = el del servidor.
	Rerank string
}

// NewEvalRetriever crea el evaluador en proceso.
func NewEvalRetriever(svc *Service, id authz.Identity) *EvalRetriever {
	return &EvalRetriever{svc: svc, id: id}
}

// Name implementa eval.Retriever.
func (r *EvalRetriever) Name() string {
	name := r.svc.searcher.Retriever().Name()
	if r.Mode == search.ModeFTS {
		name = store.IndexKindPGFTS
	}
	if v, _ := search.NormalizeRerank(r.Rerank); v == search.RerankOn {
		name += "+rerank"
	}
	return name
}

// Search implementa eval.Retriever.
func (r *EvalRetriever) Search(ctx context.Context, q eval.Question, spaces []string, at, knownAt *time.Time, limit int) (eval.Outcome, error) {
	mode := q.Mode
	if mode == "" {
		mode = r.Mode
	}
	res, err := r.svc.Search(authz.WithRequestCache(ctx), r.id, SearchRequest{Q: q.Question, Spaces: spaces, At: at, KnownAt: knownAt, Limit: limit, Mode: mode, Rerank: r.Rerank})
	if err != nil {
		return eval.Outcome{}, err
	}
	out := eval.Outcome{NoResults: res.NoResults}
	for _, h := range res.Hits {
		out.Hits = append(out.Hits, eval.Hit{Space: h.Space, DocKey: h.DocKey, Anchor: h.Anchor, Version: h.Version, PrePos: h.PreRerankPosition})
	}
	if ri := res.Rerank; ri != nil { // NX-10 R3: qué hizo el reordenador y cuánto tardó
		out.Rerank = &eval.RerankTrace{Status: ri.Status, Reason: ri.Reason, LatencyMS: ri.LatencyMS, Candidates: ri.Candidates, Reordered: ri.Reordered,
			TopChanged: ri.TopChanged, Dropped: ri.Dropped}
	}
	return out, nil
}

// Changes implementa eval.ChangeLister.
func (r *EvalRetriever) Changes(ctx context.Context, q eval.Question, spaces []string, since, until *time.Time) ([]eval.Hit, error) {
	if r.NoChanges || since == nil {
		return nil, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "changes not available")
	}
	res, err := r.svc.Changes(authz.WithRequestCache(ctx), r.id, ChangesRequest{Since: *since, Until: until, Spaces: spaces})
	if err != nil {
		return nil, err
	}
	out := make([]eval.Hit, 0, len(res.Changes))
	for _, c := range res.Changes {
		v := c.VersionAfter
		if v == 0 {
			v = c.VersionBefore
		}
		out = append(out, eval.Hit{Space: c.Space, DocKey: c.DocKey, Anchor: c.Anchor, Version: v, Change: c.Change})
	}
	return out, nil
}

// EvalConfig describe la configuración medida (se guarda en la corrida).
func (s *Service) EvalConfig() map[string]any {
	cfg := map[string]any{"retriever": s.searcher.Retriever().Name(), "index": s.st.IndexOpts(), "sectioner": s.sections}
	if c, ok := s.searcher.Retriever().(interface{ Config() map[string]any }); ok {
		cfg["search"] = c.Config()
	}
	cfg["r1"] = s.r1
	if s.hybrid != nil { // NX-10 R2
		cfg["r2"] = s.r2
		cfg["embedder"] = s.embCfg.Describe()
		if vi, ok, err := s.st.ActiveVectorIndex(context.Background()); err == nil && ok {
			cfg["vector_index"] = map[string]any{"version": vi.Version, "signature": vi.Signature}
		}
	}
	if s.rr != nil { // NX-10 R3
		cfg["r3"] = s.r3
		cfg["reranker"] = s.rrCfg.Describe()
		cfg["rerank_prompt"] = rerank.PromptVersion
	}
	return cfg
}

// RecordEvalRunInput es una corrida a registrar.
type RecordEvalRunInput struct {
	Report    eval.Report    `json:"report"`
	Config    map[string]any `json:"config"`
	Mode      string         `json:"mode"`
	Accepted  bool           `json:"accepted"`
	CommitRef string         `json:"commit"`
	Note      string         `json:"note"`
	Spaces    []string       `json:"spaces"`
}

// RecordEvalRun guarda la corrida en nexo_kb_eval_runs. Requiere
// space.publish en cada espacio medido (quien mantiene el conocimiento) o
// platform.audit.read más token write (auditoría de calidad).
func (s *Service) RecordEvalRun(ctx context.Context, id authz.Identity, in RecordEvalRunInput) (store.EvalRun, error) {
	if len(in.Spaces) == 0 {
		return store.EvalRun{}, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "spaces is required")
	}
	if in.Report.SetSHA256 == "" || in.Report.Questions == 0 {
		return store.EvalRun{}, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "report.set_sha256 and questions are required")
	}
	platform := false
	if d, err := s.eng.Can(ctx, id, authz.PlatformScope(), authz.CapPlatformAuditRead); err == nil && d.Allowed && authz.TokenScopeAllows(id.TokenScope, authz.CapSpacePublish) {
		platform = true
	}
	if !platform {
		for _, slug := range in.Spaces {
			if _, err := s.authorize(ctx, id, slug, authz.CapSpacePublish, map[string]any{"action": "eval_runs.record"}); err != nil {
				return store.EvalRun{}, err
			}
		}
	}
	cfg := in.Config
	if cfg == nil {
		cfg = map[string]any{}
	}
	rawCfg, _ := json.Marshal(cfg)
	metrics := map[string]any{}
	if raw, err := json.Marshal(in.Report.Total); err == nil {
		_ = json.Unmarshal(raw, &metrics)
	}
	byClass := map[string]any{}
	if raw, err := json.Marshal(in.Report.ByClass); err == nil {
		_ = json.Unmarshal(raw, &byClass)
	}
	mode := strings.TrimSpace(in.Mode)
	if mode == "" {
		mode = "fts"
	}
	run := store.EvalRun{ID: store.NewID(), Retriever: in.Report.Retriever, Mode: mode, IndexVersion: store.IndexVersionPGFTS, Config: cfg,
		ConfigSHA256: ingest.SHA256Hex(string(rawCfg)), QuestionSetSHA256: in.Report.SetSHA256, QuestionSetName: in.Report.Set, Spaces: in.Spaces,
		Accepted: in.Accepted, Metrics: metrics, ByClass: byClass, Results: in.Report.Results, CommitRef: truncate(in.CommitRef, 80), Note: truncate(in.Note, 500)}
	if err := store.InsertEvalRun(ctx, s.db, run, actorOf(id)); err != nil {
		return store.EvalRun{}, err
	}
	for _, slug := range in.Spaces {
		if sp, found, err := store.GetSpace(ctx, s.db, strings.ToLower(strings.TrimSpace(slug)), false); err == nil && found {
			_ = store.InsertEvent(ctx, s.db, store.EventWrite{SpaceID: sp.ID, SpaceSlug: sp.Slug, Action: domain.ActionEvalRun, TargetKind: "eval_run",
				TargetID: run.ID, Actor: actorOf(id), Details: map[string]any{"set": in.Report.Set, "set_sha256": in.Report.SetSHA256, "accepted": in.Accepted,
					"hit_at_5": in.Report.Total.HitAt5, "mrr": in.Report.Total.MRR}})
		}
	}
	run.Results = nil
	run.RanAt = time.Now().UTC()
	return run, nil
}

// ListEvalRuns lista corridas (sin el detalle por pregunta). Requiere una
// identidad con algún espacio visible.
func (s *Service) ListEvalRuns(ctx context.Context, id authz.Identity, setSHA string, acceptedOnly bool, limit int) ([]store.EvalRun, error) {
	ids, _, err := s.visibleSpaceIDs(ctx, id, nil)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []store.EvalRun{}, nil
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return store.ListEvalRuns(ctx, s.db, strings.TrimSpace(setSHA), acceptedOnly, limit)
}
