package store

// Nexo IA · NX-10 entrega 2b — registro de ejecuciones de publicación
// (KP-02) y de corridas de evaluación (RC-28). Ambas tablas son de solo
// inserción.

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
)

// PublishRun es una ejecución de publicación (lote, subida o aprobación).
type PublishRun struct {
	ID           string         `json:"id"`
	Space        string         `json:"space"`
	Kind         string         `json:"kind"`
	DryRun       bool           `json:"dry_run"`
	StartedAt    time.Time      `json:"started_at"`
	FinishedAt   time.Time      `json:"finished_at"`
	Source       string         `json:"source,omitempty"`
	Origin       map[string]any `json:"origin,omitempty"`
	Upload       map[string]any `json:"upload,omitempty"`
	Summary      map[string]int `json:"summary"`
	Rejected     []any          `json:"rejected,omitempty"`
	Pruned       int            `json:"pruned"`
	PruneSkipped string         `json:"prune_skipped,omitempty"`
	Skipped      int            `json:"skipped"`
	Actor        string         `json:"actor"`
	ActorID      string         `json:"actor_principal_id,omitempty"`
	ClientIP     string         `json:"client_ip,omitempty"`
}

// PublishRunWrite son los datos a registrar.
type PublishRunWrite struct {
	PublishRun
	SpaceID string
	Act     Actor
}

func jsonOrNil(v any) any {
	if v == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil || string(raw) == "null" {
		return nil
	}
	return string(raw)
}

// InsertPublishRun registra una ejecución.
func InsertPublishRun(ctx context.Context, q Querier, w PublishRunWrite) error {
	// NX-07: origen, subida, rechazos y fuente son texto externo (repositorio, rutas, claves de
	// documento): se quitan formatos de credencial antes de guardar.
	w.Source, w.PruneSkipped = security.NexoRedactExternal(w.Source, 0), security.NexoRedactExternal(w.PruneSkipped, 0)
	w.Origin, w.Upload = security.NexoRedactAuditMetadata(w.Origin), security.NexoRedactAuditMetadata(w.Upload)
	if w.Rejected != nil {
		if r, ok := security.NexoRedactAuditMetadata(map[string]any{"r": w.Rejected})["r"].([]any); ok {
			w.Rejected = r
		}
	}
	origin := jsonOrNil(w.Origin)
	if origin == nil {
		origin = "{}"
	}
	summary := jsonOrNil(w.Summary)
	if summary == nil {
		summary = "{}"
	}
	_, err := q.ExecContext(ctx, `
		INSERT INTO nexo_kb_publish_runs (id, space_id, space_slug, kind, dry_run, started_at, finished_at, source, origin, upload, summary,
			rejected, pruned, prune_skipped, skipped, actor_principal_id, actor_token_id, actor_label, client_ip, user_agent)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, now(), NULLIF($7, ''), $8::jsonb, $9::jsonb, $10::jsonb, $11::jsonb, $12, NULLIF($13, ''), $14,
			$15::bigint, $16::bigint, $17, NULLIF($18, ''), NULLIF($19, ''))`,
		w.ID, w.SpaceID, w.Space, w.Kind, w.DryRun, w.StartedAt, w.Source, origin, jsonOrNil(w.Upload), summary, jsonOrNil(w.Rejected),
		w.Pruned, w.PruneSkipped, w.Skipped, nullBigint(w.Act.PrincipalID), nullBigint(w.Act.TokenID), labelOrUnknown(w.Act.Label), w.Act.ClientIP, security.NexoRedactExternal(w.Act.UserAgent, 0))
	return err
}

// labelOrUnknown limpia el nombre del autor de formatos de credencial (NX-07).
func labelOrUnknown(s string) string {
	s = security.NexoRedactExternal(s, 0)
	if s == "" {
		return "desconocido"
	}
	return s
}

// ListPublishRuns lista las ejecuciones de un espacio (cursor = finished_at
// anterior), de la más reciente a la más antigua.
func ListPublishRuns(ctx context.Context, q Querier, spaceID string, before *time.Time, limit int) ([]PublishRun, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT id::text, space_slug, kind, dry_run, started_at, finished_at, COALESCE(source, ''), origin::text, COALESCE(upload::text, ''),
			summary::text, COALESCE(rejected::text, ''), pruned, COALESCE(prune_skipped, ''), skipped, actor_label,
			COALESCE(actor_principal_id::text, ''), COALESCE(client_ip, '')
		FROM nexo_kb_publish_runs WHERE space_id = $1::uuid AND ($2::timestamptz IS NULL OR finished_at < $2)
		ORDER BY finished_at DESC LIMIT $3`, spaceID, nullTime(before), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PublishRun{}
	for rows.Next() {
		var r PublishRun
		var origin, upload, summary, rejected string
		if err := rows.Scan(&r.ID, &r.Space, &r.Kind, &r.DryRun, &r.StartedAt, &r.FinishedAt, &r.Source, &origin, &upload, &summary, &rejected,
			&r.Pruned, &r.PruneSkipped, &r.Skipped, &r.Actor, &r.ActorID, &r.ClientIP); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(origin), &r.Origin)
		if upload != "" {
			_ = json.Unmarshal([]byte(upload), &r.Upload)
		}
		_ = json.Unmarshal([]byte(summary), &r.Summary)
		if rejected != "" {
			_ = json.Unmarshal([]byte(rejected), &r.Rejected)
		}
		// NX-07: ejecuciones históricas escritas antes de la redacción en escritura.
		r.Source, r.PruneSkipped, r.Actor = security.NexoRedactAuditText(r.Source), security.NexoRedactAuditText(r.PruneSkipped), security.NexoRedactAuditText(r.Actor)
		r.Origin, r.Upload = security.NexoRedactAuditMetadata(r.Origin), security.NexoRedactAuditMetadata(r.Upload)
		if red, ok := security.NexoRedactAuditMetadata(map[string]any{"r": r.Rejected})["r"].([]any); ok {
			r.Rejected = red
		}
		r.StartedAt, r.FinishedAt = r.StartedAt.UTC(), r.FinishedAt.UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// EvalRun es una corrida de evaluación (RC-28).
type EvalRun struct {
	ID                string         `json:"id"`
	RanAt             time.Time      `json:"ran_at"`
	Retriever         string         `json:"retriever"`
	Mode              string         `json:"mode"`
	IndexVersion      int            `json:"index_version"`
	Config            map[string]any `json:"config"`
	ConfigSHA256      string         `json:"config_sha256"`
	QuestionSetSHA256 string         `json:"question_set_sha256"`
	QuestionSetName   string         `json:"question_set_name,omitempty"`
	Spaces            []string       `json:"spaces"`
	Accepted          bool           `json:"accepted"`
	Metrics           map[string]any `json:"metrics"`
	ByClass           map[string]any `json:"by_class"`
	Results           any            `json:"results,omitempty"`
	CommitRef         string         `json:"commit,omitempty"`
	Note              string         `json:"note,omitempty"`
	Actor             string         `json:"actor"`
}

// InsertEvalRun registra una corrida.
func InsertEvalRun(ctx context.Context, q Querier, r EvalRun, act Actor) error {
	_, err := q.ExecContext(ctx, `
		INSERT INTO nexo_kb_eval_runs (id, retriever, mode, index_version, config, config_sha256, question_set_sha256, question_set_name, spaces,
			accepted, metrics, by_class, results, commit_ref, note, actor_principal_id, actor_token_id, actor_label)
		VALUES ($1::uuid, $2, $3, $4, $5::jsonb, $6, $7, NULLIF($8, ''), $9::text[], $10, $11::jsonb, $12::jsonb, $13::jsonb, NULLIF($14, ''),
			NULLIF($15, ''), $16::bigint, $17::bigint, $18)`,
		r.ID, r.Retriever, r.Mode, r.IndexVersion, jsonOrEmpty(r.Config), r.ConfigSHA256, r.QuestionSetSHA256, r.QuestionSetName, nonNil(r.Spaces),
		r.Accepted, jsonOrEmpty(r.Metrics), jsonOrEmpty(r.ByClass), jsonOrNil(r.Results), r.CommitRef, r.Note,
		nullBigint(act.PrincipalID), nullBigint(act.TokenID), labelOrUnknown(act.Label))
	return err
}

func jsonOrEmpty(v any) any {
	if j := jsonOrNil(v); j != nil {
		return j
	}
	return "{}"
}

// ListEvalRuns lista corridas (opcionalmente de un conjunto de preguntas o
// solo las aceptadas), de la más reciente a la más antigua, sin el detalle por
// pregunta.
func ListEvalRuns(ctx context.Context, q Querier, setSHA string, acceptedOnly bool, limit int) ([]EvalRun, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT id::text, ran_at, retriever, mode, COALESCE(index_version, 0), config::text, config_sha256, question_set_sha256,
			COALESCE(question_set_name, ''), array_to_json(spaces)::text, accepted, metrics::text, by_class::text, COALESCE(commit_ref, ''),
			COALESCE(note, ''), actor_label
		FROM nexo_kb_eval_runs WHERE ($1 = '' OR question_set_sha256 = $1) AND (NOT $2 OR accepted)
		ORDER BY ran_at DESC LIMIT $3`, setSHA, acceptedOnly, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EvalRun{}
	for rows.Next() {
		var r EvalRun
		var cfg, spaces, metrics, byClass string
		if err := rows.Scan(&r.ID, &r.RanAt, &r.Retriever, &r.Mode, &r.IndexVersion, &cfg, &r.ConfigSHA256, &r.QuestionSetSHA256, &r.QuestionSetName,
			&spaces, &r.Accepted, &metrics, &byClass, &r.CommitRef, &r.Note, &r.Actor); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(cfg), &r.Config)
		_ = json.Unmarshal([]byte(spaces), &r.Spaces)
		_ = json.Unmarshal([]byte(metrics), &r.Metrics)
		_ = json.Unmarshal([]byte(byClass), &r.ByClass)
		r.RanAt = r.RanAt.UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}
