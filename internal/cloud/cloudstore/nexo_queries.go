package cloudstore

// Nexo IA · NX-03 — consultas de trazabilidad para la API de administración.
//
// Todas devuelven de lo más reciente a lo más antiguo, por páginas de hasta
// NexoQueryMaxLimit filas, con paginación por llave (keyset), nunca OFFSET
// (RF-35, RNF-05):
//   - auditoría y actividad: cursor = id de la última fila devuelta;
//   - historial: cursor = seq de la última versión devuelta;
//   - presencia: cursor opaco con (last_seen_at, presence_key).
// Se pide una fila de más para saber si hay otra página. Nunca se devuelven
// valores de tokens (solo id, prefijo y nombre).

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Límites de página (RF-35).
const (
	NexoQueryDefaultLimit = 100
	NexoQueryMaxLimit     = 200
)

// ErrNexoInvalidCursor indica un cursor que no se puede interpretar.
var ErrNexoInvalidCursor = errors.New("cloudstore: cursor inválido")

// NexoNormalizeLimit aplica el valor por defecto y el máximo.
func NexoNormalizeLimit(limit int) int {
	if limit <= 0 {
		return NexoQueryDefaultLimit
	}
	if limit > NexoQueryMaxLimit {
		return NexoQueryMaxLimit
	}
	return limit
}

// NexoActivityOperations son las operaciones que pueden figurar en el
// registro de actividad.
var NexoActivityOperations = []string{NexoOpPushChunk, NexoOpPushMutations, NexoOpPullChunk, NexoOpPullMutations}

// ─── Tipos de consulta y resultado ───────────────────────────────────────────

// NexoAuditQuery filtra cloud_auth_audit_log.
type NexoAuditQuery struct {
	From    time.Time // inclusivo; cero = sin límite
	To      time.Time // exclusivo; cero = sin límite
	Action  string    // exacto; con '*' final = prefijo (p. ej. "nexo.*")
	Actor   string    // id de principal, usuario, email, nombre mostrado o actor_source
	Outcome string    // success | denied | ...
	Cursor  string    // id de la última fila de la página anterior
	Limit   int
}

// NexoAuditRecord es un evento de la auditoría de seguridad.
type NexoAuditRecord struct {
	ID                string         `json:"id"`
	OccurredAt        time.Time      `json:"occurred_at"`
	ActorPrincipalID  string         `json:"actor_principal_id,omitempty"`
	ActorUsername     string         `json:"actor_username,omitempty"`
	ActorDisplayName  string         `json:"actor_display_name,omitempty"`
	ActorEmail        string         `json:"actor_email,omitempty"`
	ActorSource       string         `json:"actor_source"`
	TargetPrincipalID string         `json:"target_principal_id,omitempty"`
	TargetUsername    string         `json:"target_username,omitempty"`
	Project           string         `json:"project,omitempty"`
	Action            string         `json:"action"`
	Outcome           string         `json:"outcome"`
	ReasonCode        string         `json:"reason_code,omitempty"`
	ClientIP          string         `json:"client_ip,omitempty"`
	UserAgent         string         `json:"user_agent,omitempty"`
	Metadata          map[string]any `json:"metadata,omitempty"`
}

// NexoActivityQuery filtra nexo_activity_log.
type NexoActivityQuery struct {
	From      time.Time
	To        time.Time
	Actor     string // id de principal, usuario, email, nombre mostrado o "no-identificado"
	Token     string // id, nombre o prefijo del token
	Project   string
	Operation string
	Cursor    string
	Limit     int
}

// NexoActivityRecord es una conexión registrada.
type NexoActivityRecord struct {
	ID               string    `json:"id"`
	OccurredAt       time.Time `json:"occurred_at"`
	Actor            string    `json:"actor"`
	ActorEmail       string    `json:"actor_email,omitempty"`
	ActorPrincipalID string    `json:"actor_principal_id,omitempty"`
	ActorUsername    string    `json:"actor_username,omitempty"`
	ActorVerified    bool      `json:"actor_verified"`
	TokenID          string    `json:"token_id,omitempty"`
	TokenPrefix      string    `json:"token_prefix,omitempty"`
	TokenName        string    `json:"token_name,omitempty"`
	Operation        string    `json:"operation"`
	Project          string    `json:"project,omitempty"`
	ItemCount        int       `json:"item_count"`
	ClientIP         string    `json:"client_ip,omitempty"`
	UserAgent        string    `json:"user_agent,omitempty"`
	ClientClaim      string    `json:"client_claim,omitempty"`
}

// NexoPresenceQuery filtra nexo_actor_presence.
type NexoPresenceQuery struct {
	Actor  string
	Since  time.Time // last_seen_at >= Since; cero = sin límite
	Cursor string
	Limit  int
}

// NexoPresenceRecord es la presencia de una persona + token.
type NexoPresenceRecord struct {
	PresenceKey      string    `json:"presence_key"`
	Actor            string    `json:"actor"`
	ActorEmail       string    `json:"actor_email,omitempty"`
	ActorPrincipalID string    `json:"actor_principal_id,omitempty"`
	ActorUsername    string    `json:"actor_username,omitempty"`
	ActorVerified    bool      `json:"actor_verified"`
	TokenID          string    `json:"token_id,omitempty"`
	TokenPrefix      string    `json:"token_prefix,omitempty"`
	TokenName        string    `json:"token_name,omitempty"`
	FirstSeenAt      time.Time `json:"first_seen_at"`
	LastSeenAt       time.Time `json:"last_seen_at"`
	LastIP           string    `json:"last_ip,omitempty"`
	LastUserAgent    string    `json:"last_user_agent,omitempty"`
	LastClientClaim  string    `json:"last_client_claim,omitempty"`
	LastProject      string    `json:"last_project,omitempty"`
	PushCount        int64     `json:"push_count"`
	PullCount        int64     `json:"pull_count"`
}

// NexoHistoryQuery pide las versiones de un recuerdo (entity_key).
type NexoHistoryQuery struct {
	EntityKey      string
	Project        string // opcional
	Entity         string // opcional (observation, session, prompt, ...)
	IncludePayload bool   // RF-34: el contenido completo solo si se pide
	Cursor         string
	Limit          int
}

// NexoHistoryRecord es una versión de un recuerdo.
type NexoHistoryRecord struct {
	Seq              string          `json:"seq"`
	Version          int64           `json:"version"`
	Project          string          `json:"project"`
	Entity           string          `json:"entity"`
	EntityKey        string          `json:"entity_key"`
	Op               string          `json:"op"`
	OccurredAt       time.Time       `json:"occurred_at"`
	Actor            string          `json:"actor,omitempty"`
	ActorEmail       string          `json:"actor_email,omitempty"`
	ActorVerified    bool            `json:"actor_verified"`
	ActorPrincipalID string          `json:"actor_principal_id,omitempty"`
	ActorUsername    string          `json:"actor_username,omitempty"`
	CurrentEmail     string          `json:"current_email,omitempty"`
	TokenID          string          `json:"token_id,omitempty"`
	TokenPrefix      string          `json:"token_prefix,omitempty"`
	TokenName        string          `json:"token_name,omitempty"`
	ClientClaim      string          `json:"client_claim,omitempty"`
	Title            string          `json:"title,omitempty"`
	Payload          json.RawMessage `json:"payload,omitempty"`
}

// ─── Utilidades ──────────────────────────────────────────────────────────────

type nexoWhere struct {
	clauses []string
	args    []any
}

// arg agrega un argumento y devuelve su marcador ($n).
func (w *nexoWhere) arg(v any) string {
	w.args = append(w.args, v)
	return "$" + strconv.Itoa(len(w.args))
}

func (w *nexoWhere) add(clause string) { w.clauses = append(w.clauses, clause) }

func (w *nexoWhere) sql() string {
	if len(w.clauses) == 0 {
		return ""
	}
	return "WHERE " + strings.Join(w.clauses, " AND ")
}

// nexoParseIDCursor interpreta un cursor numérico (id o seq).
func nexoParseIDCursor(cursor string) (int64, bool, error) {
	cursor = strings.TrimSpace(cursor)
	if cursor == "" {
		return 0, false, nil
	}
	n, err := strconv.ParseInt(cursor, 10, 64)
	if err != nil || n <= 0 {
		return 0, false, ErrNexoInvalidCursor
	}
	return n, true, nil
}

func nexoIsDigits(s string) bool {
	if s == "" || len(s) > 18 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// nexoPrincipalIDsByName devuelve una subconsulta con los id (BIGINT) de los
// principales cuyo usuario, email o nombre mostrado coincide (sin distinguir
// mayúsculas).
func nexoPrincipalIDsByName(placeholder string) string {
	return `SELECT p.id FROM cloud_principals p LEFT JOIN cloud_human_users u ON u.principal_id = p.id
		WHERE lower(u.username) = lower(` + placeholder + `) OR lower(u.email) = lower(` + placeholder + `) OR lower(p.display_name) = lower(` + placeholder + `)`
}

func nexoEscapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

func (cs *CloudStore) nexoReady() error {
	if cs == nil || cs.db == nil {
		return fmt.Errorf("cloudstore: not initialized")
	}
	return nil
}

// ─── Auditoría de seguridad (RF-31) ──────────────────────────────────────────

// ListAuthAudit consulta cloud_auth_audit_log con filtros y cursor. Devuelve
// las filas y el cursor de la página siguiente ("" si no hay más).
func (cs *CloudStore) ListAuthAudit(ctx context.Context, q NexoAuditQuery) ([]NexoAuditRecord, string, error) {
	if err := cs.nexoReady(); err != nil {
		return nil, "", err
	}
	limit := NexoNormalizeLimit(q.Limit)
	var w nexoWhere
	if id, ok, err := nexoParseIDCursor(q.Cursor); err != nil {
		return nil, "", err
	} else if ok {
		w.add("a.id < " + w.arg(id))
	}
	if !q.From.IsZero() {
		w.add("a.occurred_at >= " + w.arg(q.From))
	}
	if !q.To.IsZero() {
		w.add("a.occurred_at < " + w.arg(q.To))
	}
	if action := strings.TrimSpace(q.Action); action != "" {
		if strings.HasSuffix(action, "*") {
			w.add("a.action LIKE " + w.arg(nexoEscapeLike(strings.TrimSuffix(action, "*"))+"%"))
		} else {
			w.add("a.action = " + w.arg(action))
		}
	}
	if outcome := strings.TrimSpace(q.Outcome); outcome != "" {
		w.add("a.outcome = " + w.arg(outcome))
	}
	if actor := strings.TrimSpace(q.Actor); actor != "" {
		if nexoIsDigits(actor) {
			w.add("a.actor_principal_id = " + w.arg(actor) + "::bigint")
		} else {
			p := w.arg(actor)
			w.add("(a.actor_principal_id IN (" + nexoPrincipalIDsByName(p) + ") OR a.actor_source = " + p + ")")
		}
	}
	query := `
		SELECT a.id::text, a.occurred_at, COALESCE(a.actor_principal_id::text, ''), COALESCE(au.username, ''),
			COALESCE(ap.display_name, ''), COALESCE(au.email, ''), a.actor_source,
			COALESCE(a.target_principal_id::text, ''), COALESCE(tu.username, ''), COALESCE(a.project, ''),
			a.action, a.outcome, COALESCE(a.reason_code, ''), a.metadata
		FROM cloud_auth_audit_log a
		LEFT JOIN cloud_principals ap ON ap.id = a.actor_principal_id
		LEFT JOIN cloud_human_users au ON au.principal_id = a.actor_principal_id
		LEFT JOIN cloud_human_users tu ON tu.principal_id = a.target_principal_id
		` + w.sql() + `
		ORDER BY a.id DESC
		LIMIT ` + w.arg(limit+1)
	rows, err := cs.db.QueryContext(ctx, query, w.args...)
	if err != nil {
		return nil, "", fmt.Errorf("cloudstore: nexo list auth audit: %w", err)
	}
	defer rows.Close()
	out := make([]NexoAuditRecord, 0, limit)
	for rows.Next() {
		var rec NexoAuditRecord
		var metadata []byte
		if err := rows.Scan(&rec.ID, &rec.OccurredAt, &rec.ActorPrincipalID, &rec.ActorUsername, &rec.ActorDisplayName,
			&rec.ActorEmail, &rec.ActorSource, &rec.TargetPrincipalID, &rec.TargetUsername, &rec.Project,
			&rec.Action, &rec.Outcome, &rec.ReasonCode, &metadata); err != nil {
			return nil, "", fmt.Errorf("cloudstore: nexo scan auth audit: %w", err)
		}
		if len(metadata) > 0 {
			if err := json.Unmarshal(metadata, &rec.Metadata); err != nil {
				return nil, "", fmt.Errorf("cloudstore: nexo decode auth audit metadata: %w", err)
			}
			if ip, ok := rec.Metadata[NexoAuditMetaClientIP].(string); ok {
				rec.ClientIP = ip
			}
			if ua, ok := rec.Metadata[NexoAuditMetaUserAgent].(string); ok {
				rec.UserAgent = ua
			}
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("cloudstore: nexo iterate auth audit: %w", err)
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = out[limit-1].ID
	}
	return out, next, nil
}

// ─── Actividad (RF-32) ───────────────────────────────────────────────────────

// nexoActorFilter arma el filtro por actor sobre una columna actor_principal_id
// de tipo TEXT y una columna actor_verified.
func nexoActorFilter(w *nexoWhere, actor, principalCol, verifiedCol string) {
	actor = strings.TrimSpace(actor)
	switch {
	case actor == "":
		return
	case strings.EqualFold(actor, NexoActorUnidentified):
		w.add("NOT " + verifiedCol)
	case nexoIsDigits(actor):
		w.add(principalCol + " = " + w.arg(actor))
	default:
		p := w.arg(actor)
		w.add(principalCol + " IN (SELECT id::text FROM (" + nexoPrincipalIDsByName(p) + ") AS nexo_actor_ids)")
	}
}

// ListActivity consulta nexo_activity_log con filtros y cursor.
func (cs *CloudStore) ListActivity(ctx context.Context, q NexoActivityQuery) ([]NexoActivityRecord, string, error) {
	if err := cs.nexoReady(); err != nil {
		return nil, "", err
	}
	limit := NexoNormalizeLimit(q.Limit)
	var w nexoWhere
	if id, ok, err := nexoParseIDCursor(q.Cursor); err != nil {
		return nil, "", err
	} else if ok {
		w.add("l.id < " + w.arg(id))
	}
	if !q.From.IsZero() {
		w.add("l.occurred_at >= " + w.arg(q.From))
	}
	if !q.To.IsZero() {
		w.add("l.occurred_at < " + w.arg(q.To))
	}
	nexoActorFilter(&w, q.Actor, "l.actor_principal_id", "l.actor_verified")
	if token := strings.TrimSpace(q.Token); token != "" {
		if nexoIsDigits(token) {
			w.add("l.actor_token_id = " + w.arg(token))
		} else {
			p := w.arg(token)
			w.add("l.actor_token_id IN (SELECT id::text FROM cloud_principal_tokens WHERE name = " + p + " OR token_prefix = " + p + ")")
		}
	}
	if project := strings.TrimSpace(q.Project); project != "" {
		p := w.arg(project)
		w.add("(l.project = " + p + " OR " + p + " = ANY(string_to_array(l.project, ',')))")
	}
	if op := strings.TrimSpace(q.Operation); op != "" {
		w.add("l.operation = " + w.arg(op))
	}
	query := `
		SELECT l.id::text, l.occurred_at, l.actor, COALESCE(l.actor_email, ''), COALESCE(l.actor_principal_id, ''),
			COALESCE(u.username, ''), l.actor_verified, COALESCE(l.actor_token_id, ''), COALESCE(t.token_prefix, ''),
			COALESCE(t.name, ''), l.operation, COALESCE(l.project, ''), l.item_count, COALESCE(l.client_ip, ''),
			COALESCE(l.user_agent, ''), COALESCE(l.client_claim, '')
		FROM nexo_activity_log l
		LEFT JOIN cloud_human_users u ON u.principal_id::text = l.actor_principal_id
		LEFT JOIN cloud_principal_tokens t ON t.id::text = l.actor_token_id
		` + w.sql() + `
		ORDER BY l.id DESC
		LIMIT ` + w.arg(limit+1)
	rows, err := cs.db.QueryContext(ctx, query, w.args...)
	if err != nil {
		return nil, "", fmt.Errorf("cloudstore: nexo list activity: %w", err)
	}
	defer rows.Close()
	out := make([]NexoActivityRecord, 0, limit)
	for rows.Next() {
		var rec NexoActivityRecord
		if err := rows.Scan(&rec.ID, &rec.OccurredAt, &rec.Actor, &rec.ActorEmail, &rec.ActorPrincipalID,
			&rec.ActorUsername, &rec.ActorVerified, &rec.TokenID, &rec.TokenPrefix, &rec.TokenName,
			&rec.Operation, &rec.Project, &rec.ItemCount, &rec.ClientIP, &rec.UserAgent, &rec.ClientClaim); err != nil {
			return nil, "", fmt.Errorf("cloudstore: nexo scan activity: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("cloudstore: nexo iterate activity: %w", err)
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = out[limit-1].ID
	}
	return out, next, nil
}

// ─── Presencia (RF-33) ───────────────────────────────────────────────────────

// nexoEncodePresenceCursor arma el cursor opaco de presencia.
func nexoEncodePresenceCursor(lastSeen time.Time, key string) string {
	raw := lastSeen.UTC().Format(time.RFC3339Nano) + "|" + key
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func nexoDecodePresenceCursor(cursor string) (time.Time, string, bool, error) {
	cursor = strings.TrimSpace(cursor)
	if cursor == "" {
		return time.Time{}, "", false, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", false, ErrNexoInvalidCursor
	}
	ts, key, found := strings.Cut(string(raw), "|")
	if !found || key == "" {
		return time.Time{}, "", false, ErrNexoInvalidCursor
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return time.Time{}, "", false, ErrNexoInvalidCursor
	}
	return t, key, true, nil
}

// ListPresence consulta nexo_actor_presence, de la conexión más reciente a la
// más antigua.
func (cs *CloudStore) ListPresence(ctx context.Context, q NexoPresenceQuery) ([]NexoPresenceRecord, string, error) {
	if err := cs.nexoReady(); err != nil {
		return nil, "", err
	}
	limit := NexoNormalizeLimit(q.Limit)
	var w nexoWhere
	if ts, key, ok, err := nexoDecodePresenceCursor(q.Cursor); err != nil {
		return nil, "", err
	} else if ok {
		w.add("(p.last_seen_at, p.presence_key) < (" + w.arg(ts) + ", " + w.arg(key) + ")")
	}
	if !q.Since.IsZero() {
		w.add("p.last_seen_at >= " + w.arg(q.Since))
	}
	nexoActorFilter(&w, q.Actor, "p.actor_principal_id", "p.actor_verified")
	query := `
		SELECT p.presence_key, p.actor, COALESCE(p.actor_email, ''), COALESCE(p.actor_principal_id, ''),
			COALESCE(u.username, ''), p.actor_verified, COALESCE(p.actor_token_id, ''), COALESCE(t.token_prefix, ''),
			COALESCE(t.name, ''), p.first_seen_at, p.last_seen_at, COALESCE(p.last_ip, ''),
			COALESCE(p.last_user_agent, ''), COALESCE(p.last_client_claim, ''), COALESCE(p.last_project, ''),
			p.push_count, p.pull_count
		FROM nexo_actor_presence p
		LEFT JOIN cloud_human_users u ON u.principal_id::text = p.actor_principal_id
		LEFT JOIN cloud_principal_tokens t ON t.id::text = p.actor_token_id
		` + w.sql() + `
		ORDER BY p.last_seen_at DESC, p.presence_key DESC
		LIMIT ` + w.arg(limit+1)
	rows, err := cs.db.QueryContext(ctx, query, w.args...)
	if err != nil {
		return nil, "", fmt.Errorf("cloudstore: nexo list presence: %w", err)
	}
	defer rows.Close()
	out := make([]NexoPresenceRecord, 0, limit)
	for rows.Next() {
		var rec NexoPresenceRecord
		if err := rows.Scan(&rec.PresenceKey, &rec.Actor, &rec.ActorEmail, &rec.ActorPrincipalID,
			&rec.ActorUsername, &rec.ActorVerified, &rec.TokenID, &rec.TokenPrefix, &rec.TokenName,
			&rec.FirstSeenAt, &rec.LastSeenAt, &rec.LastIP, &rec.LastUserAgent, &rec.LastClientClaim,
			&rec.LastProject, &rec.PushCount, &rec.PullCount); err != nil {
			return nil, "", fmt.Errorf("cloudstore: nexo scan presence: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("cloudstore: nexo iterate presence: %w", err)
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[limit-1]
		next = nexoEncodePresenceCursor(last.LastSeenAt, last.PresenceKey)
	}
	return out, next, nil
}

// ─── Historial de un recuerdo (RF-34) ────────────────────────────────────────

// MutationHistory devuelve las versiones de un recuerdo desde la vista
// nexo_mutation_history, de la más reciente a la más antigua. version numera
// las versiones de 1 en adelante por proyecto y tipo de entidad. El payload
// completo solo se incluye con IncludePayload.
func (cs *CloudStore) MutationHistory(ctx context.Context, q NexoHistoryQuery) ([]NexoHistoryRecord, string, error) {
	if err := cs.nexoReady(); err != nil {
		return nil, "", err
	}
	entityKey := strings.TrimSpace(q.EntityKey)
	if entityKey == "" {
		return nil, "", fmt.Errorf("cloudstore: nexo history entity key is required")
	}
	limit := NexoNormalizeLimit(q.Limit)
	var inner nexoWhere
	inner.add("h.entity_key = " + inner.arg(entityKey))
	if project := strings.TrimSpace(q.Project); project != "" {
		inner.add("h.project = " + inner.arg(project))
	}
	if entity := strings.TrimSpace(q.Entity); entity != "" {
		inner.add("h.entity = " + inner.arg(entity))
	}
	outer := nexoWhere{args: inner.args}
	if seq, ok, err := nexoParseIDCursor(q.Cursor); err != nil {
		return nil, "", err
	} else if ok {
		outer.add("v.seq < " + outer.arg(seq))
	}
	payloadExpr := "NULL::text"
	if q.IncludePayload {
		payloadExpr = "(SELECT m.payload::text FROM cloud_mutations m WHERE m.seq = v.seq)"
	}
	query := `
		SELECT v.seq::text, v.version, v.project, v.entity, v.entity_key, v.op, v.occurred_at, v.actor, v.actor_email,
			v.actor_verified, v.actor_principal_id, v.username, v.current_email, v.actor_token_id, v.token_prefix,
			v.token_name, v.actor_client_claim, v.title, ` + payloadExpr + `
		FROM (
			SELECT h.seq, ROW_NUMBER() OVER (PARTITION BY h.project, h.entity ORDER BY h.seq) AS version,
				h.project, h.entity, h.entity_key, h.op, h.occurred_at,
				COALESCE(h.actor, '') AS actor, COALESCE(h.actor_email, '') AS actor_email, h.actor_verified,
				COALESCE(h.actor_principal_id, '') AS actor_principal_id, COALESCE(h.username, '') AS username,
				COALESCE(h.current_email, '') AS current_email, COALESCE(h.actor_token_id, '') AS actor_token_id,
				COALESCE(h.token_prefix, '') AS token_prefix, COALESCE(h.token_name, '') AS token_name,
				COALESCE(h.actor_client_claim, '') AS actor_client_claim, COALESCE(h.title, '') AS title
			FROM nexo_mutation_history h
			` + inner.sql() + `
		) v
		` + outer.sql() + `
		ORDER BY v.seq DESC
		LIMIT ` + outer.arg(limit+1)
	rows, err := cs.db.QueryContext(ctx, query, outer.args...)
	if err != nil {
		return nil, "", fmt.Errorf("cloudstore: nexo mutation history: %w", err)
	}
	defer rows.Close()
	out := make([]NexoHistoryRecord, 0)
	for rows.Next() {
		var rec NexoHistoryRecord
		var payload sql.NullString
		if err := rows.Scan(&rec.Seq, &rec.Version, &rec.Project, &rec.Entity, &rec.EntityKey, &rec.Op, &rec.OccurredAt,
			&rec.Actor, &rec.ActorEmail, &rec.ActorVerified, &rec.ActorPrincipalID, &rec.ActorUsername,
			&rec.CurrentEmail, &rec.TokenID, &rec.TokenPrefix, &rec.TokenName, &rec.ClientClaim, &rec.Title,
			&payload); err != nil {
			return nil, "", fmt.Errorf("cloudstore: nexo scan history: %w", err)
		}
		if payload.Valid && payload.String != "" {
			rec.Payload = json.RawMessage(payload.String)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("cloudstore: nexo iterate history: %w", err)
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = out[limit-1].Seq
	}
	return out, next, nil
}
