package authz

// Implementación Postgres del Store (lecturas del motor, eventos y listados).
// Cada decisión cuesta una consulta indexada (RNF-R1).

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/redact"
)

// ErrInvalidCursor indica un cursor de paginación ilegible.
var ErrInvalidCursor = errors.New("authz: invalid cursor")

// PGStore lee y escribe las tablas nexo_scopes, nexo_role_bindings y
// nexo_authz_events (y lee cloud_principals, cloud_human_users y
// cloud_project_grants de Engram, sin modificarlas salvo el rol de Engram al
// cambiar un rol de plataforma; ver Service.SetPlatformRole).
type PGStore struct {
	db *sql.DB
}

// NewPGStore crea el store sobre la base de Engram Cloud.
func NewPGStore(db *sql.DB) *PGStore { return &PGStore{db: db} }

// NewPostgres crea un motor sobre Postgres.
func NewPostgres(db *sql.DB, opts ...Option) *Engine { return NewEngine(NewPGStore(db), opts...) }

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// PlatformFacts implementa Store.
func (p *PGStore) PlatformFacts(ctx context.Context, principalID int64) (PlatformFacts, error) {
	var f PlatformFacts
	var binding sql.NullString
	err := p.db.QueryRowContext(ctx, `
		SELECT p.kind, p.role, p.enabled, b.role
		FROM cloud_principals p
		LEFT JOIN nexo_role_bindings b ON b.principal_id = p.id AND b.scope_type = 'platform' AND b.scope_key = '*'
		WHERE p.id = $1`, principalID).Scan(&f.Kind, &f.EngramRole, &f.Enabled, &binding)
	if errors.Is(err, sql.ErrNoRows) {
		return PlatformFacts{}, nil
	}
	if err != nil {
		return PlatformFacts{}, err
	}
	f.Found = true
	f.Binding = Role(binding.String)
	return f, nil
}

// ScopeFacts implementa Store: una sola consulta por principal y ámbito.
func (p *PGStore) ScopeFacts(ctx context.Context, principalID int64, s Scope) (ScopeFacts, error) {
	var f ScopeFacts
	var vis, def, status, binding sql.NullString
	err := p.db.QueryRowContext(ctx, `
		SELECT p.enabled, p.kind, s.visibility, s.default_role, s.status, b.role,
		       CASE WHEN $2 = 'project'
		            THEN EXISTS (SELECT 1 FROM cloud_project_grants g WHERE g.principal_id = p.id AND g.project = $3)
		            ELSE FALSE END
		FROM cloud_principals p
		LEFT JOIN nexo_scopes s ON s.scope_type = $2 AND s.scope_key = $3
		LEFT JOIN nexo_role_bindings b ON b.principal_id = p.id AND b.scope_type = $2 AND b.scope_key = $3
		WHERE p.id = $1`, principalID, string(s.Type), s.Key).Scan(&f.Enabled, &f.Kind, &vis, &def, &status, &binding, &f.EngramGrant)
	if errors.Is(err, sql.ErrNoRows) {
		return ScopeFacts{}, nil
	}
	if err != nil {
		return ScopeFacts{}, err
	}
	f.Found = true
	f.Exists = status.Valid
	f.Visibility, f.DefaultRole, f.Status, f.Binding = vis.String, Role(def.String), status.String, Role(binding.String)
	return f, nil
}

// VisibleScopeFacts implementa Store: unión de asignaciones, permisos de
// Engram (proyectos) y ámbitos abiertos activos, en una consulta.
func (p *PGStore) VisibleScopeFacts(ctx context.Context, principalID int64, t ScopeType) ([]ScopeFactsRow, error) {
	rows, err := p.db.QueryContext(ctx, `
		WITH me AS (SELECT id, enabled, kind FROM cloud_principals WHERE id = $1),
		b AS (SELECT scope_key, role FROM nexo_role_bindings WHERE principal_id = $1 AND scope_type = $2),
		g AS (SELECT project AS scope_key FROM cloud_project_grants WHERE principal_id = $1 AND $2 = 'project'),
		k AS (
			SELECT scope_key FROM b
			UNION SELECT scope_key FROM g
			UNION SELECT scope_key FROM nexo_scopes WHERE scope_type = $2 AND visibility = 'open' AND status = 'active'
		)
		SELECT k.scope_key, me.enabled, me.kind, s.visibility, s.default_role, s.status, b.role, (g.scope_key IS NOT NULL)
		FROM k CROSS JOIN me
		LEFT JOIN b ON b.scope_key = k.scope_key
		LEFT JOIN g ON g.scope_key = k.scope_key
		LEFT JOIN nexo_scopes s ON s.scope_type = $2 AND s.scope_key = k.scope_key
		ORDER BY k.scope_key`, principalID, string(t))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ScopeFactsRow{}
	for rows.Next() {
		var r ScopeFactsRow
		var vis, def, status, binding sql.NullString
		if err := rows.Scan(&r.Key, &r.Enabled, &r.Kind, &vis, &def, &status, &binding, &r.EngramGrant); err != nil {
			return nil, err
		}
		r.Found = true
		r.Exists = status.Valid
		r.Visibility, r.DefaultRole, r.Status, r.Binding = vis.String, Role(def.String), status.String, Role(binding.String)
		out = append(out, r)
	}
	return out, rows.Err()
}

// RecordEvent implementa Store.
func (p *PGStore) RecordEvent(ctx context.Context, ev Event) error {
	return insertEvent(ctx, p.db, ev)
}

// insertEvent inserta un evento. El email del actor se copia en el mismo
// INSERT desde cloud_human_users (patrón NX-01).
func insertEvent(ctx context.Context, exec execer, ev Event) error {
	var details any
	if len(ev.Details) > 0 {
		// NX-07: detalles sin formatos de credencial, incluso si vienen de texto externo.
		raw, err := json.Marshal(redact.Metadata(ev.Details))
		if err != nil {
			return fmt.Errorf("authz: encode event details: %w", err)
		}
		details = string(raw)
	}
	actor := ev.Actor
	label := redact.External(actor.Label, 0)
	var actorPID, actorTID any
	if actor.Managed() {
		actorPID = parseBigint(actor.PrincipalID)
		actorTID = parseBigint(actor.TokenID)
	}
	switch {
	case actor == (Identity{}):
		label = SystemActorLabel
	case label == "" && actor.Managed():
		label = actor.PrincipalID
	case label == "" || !actor.Managed():
		label = "no-identificado"
	}
	_, err := exec.ExecContext(ctx, `
		INSERT INTO nexo_authz_events (action, scope_type, scope_key, target_principal_id, role_before, role_after,
			actor_principal_id, actor_token_id, actor_label, actor_email, actor_verified, client_ip, user_agent, client_claim, details)
		VALUES ($1, $2, $3, $4, $5, $6, $7::bigint, $8, $9,
			(SELECT email FROM cloud_human_users WHERE principal_id = $7::bigint), $10, $11, $12, $13, $14::jsonb)`,
		ev.Action, string(ev.Scope.Type), ev.Scope.Key, parseBigint(ev.TargetPrincipalID),
		nullText(string(ev.RoleBefore)), nullText(string(ev.RoleAfter)),
		actorPID, actorTID, label, actor.Managed(),
		nullText(actor.ClientIP), nullText(redact.External(actor.UserAgent, 0)), nullText(truncate(redact.External(actor.ClientClaim, 0), 128)), details)
	if err != nil {
		return fmt.Errorf("authz: insert event: %w", err)
	}
	return nil
}

// ListEvents devuelve eventos del más reciente al más antiguo.
func (p *PGStore) ListEvents(ctx context.Context, q EventQuery) ([]EventRecord, string, error) {
	limit := NormalizeLimit(q.Limit)
	where := []string{"TRUE"}
	args := []any{}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "$?", "$"+strconv.Itoa(len(args))))
	}
	if q.ScopeType != "" {
		add("e.scope_type = $?", q.ScopeType)
	}
	if q.ScopeKey != "" {
		add("e.scope_key = $?", q.ScopeKey)
	}
	if a := strings.TrimSpace(q.Action); a != "" {
		if strings.HasSuffix(a, "*") {
			add("e.action LIKE $?", strings.TrimSuffix(a, "*")+"%")
		} else {
			add("e.action = $?", a)
		}
	}
	if pr := strings.TrimSpace(q.Principal); pr != "" {
		if n, err := strconv.ParseInt(pr, 10, 64); err == nil {
			add("(e.actor_principal_id = $? OR e.target_principal_id = $?)", n)
		} else {
			add("(e.actor_principal_id = (SELECT principal_id FROM cloud_human_users WHERE username = $?) OR e.target_principal_id = (SELECT principal_id FROM cloud_human_users WHERE username = $?))", pr)
		}
	}
	if !q.From.IsZero() {
		add("e.occurred_at >= $?", q.From)
	}
	if !q.To.IsZero() {
		add("e.occurred_at < $?", q.To)
	}
	if c := strings.TrimSpace(q.Cursor); c != "" {
		n, err := strconv.ParseInt(c, 10, 64)
		if err != nil || n <= 0 {
			return nil, "", ErrInvalidCursor
		}
		add("e.id < $?", n)
	}
	args = append(args, limit+1)
	rows, err := p.db.QueryContext(ctx, `
		SELECT e.id::text, e.occurred_at, e.action, e.scope_type, e.scope_key,
		       COALESCE(e.target_principal_id::text, ''), COALESCE(u.username, ''),
		       COALESCE(e.role_before, ''), COALESCE(e.role_after, ''),
		       COALESCE(e.actor_principal_id::text, ''), COALESCE(e.actor_token_id::text, ''),
		       e.actor_label, COALESCE(e.actor_email, ''), e.actor_verified,
		       COALESCE(e.client_ip, ''), COALESCE(e.user_agent, ''), COALESCE(e.client_claim, ''), e.details
		FROM nexo_authz_events e
		LEFT JOIN cloud_human_users u ON u.principal_id = e.target_principal_id
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY e.id DESC
		LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := []EventRecord{}
	for rows.Next() {
		var r EventRecord
		var details []byte
		if err := rows.Scan(&r.ID, &r.OccurredAt, &r.Action, &r.ScopeType, &r.ScopeKey, &r.TargetPrincipalID, &r.TargetUsername,
			&r.RoleBefore, &r.RoleAfter, &r.ActorPrincipalID, &r.ActorTokenID, &r.Actor, &r.ActorEmail, &r.ActorVerified,
			&r.ClientIP, &r.UserAgent, &r.ClientClaim, &details); err != nil {
			return nil, "", err
		}
		if len(details) > 0 {
			_ = json.Unmarshal(details, &r.Details)
		}
		// NX-07: filas históricas escritas antes de la redacción en escritura.
		redact.Struct(&r)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = out[len(out)-1].ID
	}
	return out, next, nil
}

// Member es una asignación en un ámbito (o el rol de plataforma de una persona).
type Member struct {
	PrincipalID string     `json:"principal_id"`
	Kind        string     `json:"kind"`
	Username    string     `json:"username,omitempty"`
	DisplayName string     `json:"display_name"`
	Email       string     `json:"email,omitempty"`
	Enabled     bool       `json:"enabled"`
	Role        Role       `json:"role"`
	Source      string     `json:"source"`
	GrantedBy   string     `json:"granted_by_principal_id,omitempty"`
	UpdatedAt   *time.Time `json:"updated_at,omitempty"`
}

// MemberQuery pagina listados de miembros (cursor = id de principal).
type MemberQuery struct {
	Role   Role
	Cursor string
	Limit  int
}

func memberCursor(c string) (int64, error) {
	c = strings.TrimSpace(c)
	if c == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(c, 10, 64)
	if err != nil || n < 0 {
		return 0, ErrInvalidCursor
	}
	return n, nil
}

// ListPlatformMembers devuelve a todas las personas y cuentas de servicio con
// su rol de plataforma de Nexo (sin asignación: miembro).
func (p *PGStore) ListPlatformMembers(ctx context.Context, q MemberQuery) ([]Member, string, error) {
	cursor, err := memberCursor(q.Cursor)
	if err != nil {
		return nil, "", err
	}
	limit := NormalizeLimit(q.Limit)
	rows, err := p.db.QueryContext(ctx, `
		WITH m AS (
			SELECT p.id, p.kind, p.display_name, p.enabled, COALESCE(h.username, sa.slug, '') AS username, COALESCE(h.email, '') AS email,
			       CASE WHEN p.kind = 'service_account' THEN 'miembro'
			            WHEN b.role IS NOT NULL THEN b.role
			            ELSE 'miembro' END AS role,
			       CASE WHEN p.kind = 'service_account' THEN 'none'
			            WHEN b.role IS NOT NULL THEN 'binding'
			            ELSE 'none' END AS source,
			       COALESCE(b.granted_by_principal_id::text, '') AS granted_by, b.updated_at
			FROM cloud_principals p
			LEFT JOIN cloud_human_users h ON h.principal_id = p.id
			LEFT JOIN nexo_service_accounts sa ON sa.principal_id = p.id -- SG-10: el slug hace de usuario
			LEFT JOIN nexo_role_bindings b ON b.principal_id = p.id AND b.scope_type = 'platform' AND b.scope_key = '*'
			WHERE p.id > $1
		)
		SELECT id::text, kind, display_name, enabled, username, email, role, source, granted_by, updated_at
		FROM m WHERE ($2 = '' OR role = $2)
		ORDER BY id LIMIT $3`, cursor, string(q.Role), limit+1)
	if err != nil {
		return nil, "", err
	}
	return scanMembers(rows, limit)
}

// ListScopeMembers devuelve las asignaciones explícitas de un ámbito y, en
// proyectos, los permisos de Engram sin asignación (publicador por
// compatibilidad). El rol implícito de un ámbito abierto no se lista: lo
// tienen todos.
func (p *PGStore) ListScopeMembers(ctx context.Context, s Scope, q MemberQuery) ([]Member, string, error) {
	cursor, err := memberCursor(q.Cursor)
	if err != nil {
		return nil, "", err
	}
	limit := NormalizeLimit(q.Limit)
	rows, err := p.db.QueryContext(ctx, `
		WITH m AS (
			SELECT b.principal_id, b.role, 'binding' AS source, b.granted_by_principal_id, b.updated_at
			FROM nexo_role_bindings b WHERE b.scope_type = $1 AND b.scope_key = $2
			UNION ALL
			SELECT g.principal_id, 'publicador', 'engram_grant', g.granted_by_principal_id, g.created_at
			FROM cloud_project_grants g
			WHERE $1 = 'project' AND g.project = $2
			  AND NOT EXISTS (SELECT 1 FROM nexo_role_bindings x WHERE x.principal_id = g.principal_id AND x.scope_type = 'project' AND x.scope_key = $2)
		)
		SELECT m.principal_id::text, p.kind, p.display_name, p.enabled, COALESCE(h.username, sa.slug, ''), COALESCE(h.email, ''),
		       m.role, m.source, COALESCE(m.granted_by_principal_id::text, ''), m.updated_at
		FROM m JOIN cloud_principals p ON p.id = m.principal_id
		LEFT JOIN cloud_human_users h ON h.principal_id = p.id
		LEFT JOIN nexo_service_accounts sa ON sa.principal_id = p.id -- SG-10: el slug hace de usuario
		WHERE m.principal_id > $3 AND ($4 = '' OR m.role = $4)
		ORDER BY m.principal_id LIMIT $5`, string(s.Type), s.Key, cursor, string(q.Role), limit+1)
	if err != nil {
		return nil, "", err
	}
	return scanMembers(rows, limit)
}

func scanMembers(rows *sql.Rows, limit int) ([]Member, string, error) {
	defer rows.Close()
	out := []Member{}
	for rows.Next() {
		var m Member
		var role string
		var updated sql.NullTime
		if err := rows.Scan(&m.PrincipalID, &m.Kind, &m.DisplayName, &m.Enabled, &m.Username, &m.Email, &role, &m.Source, &m.GrantedBy, &updated); err != nil {
			return nil, "", err
		}
		m.Role = Role(role)
		if updated.Valid {
			t := updated.Time
			m.UpdatedAt = &t
		}
		redact.Struct(&m) // NX-07: usuario, correo y nombre históricos sin credenciales
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = out[len(out)-1].PrincipalID
	}
	return out, next, nil
}

// ScopeInfo es la configuración de un ámbito.
type ScopeInfo struct {
	Scope       Scope     `json:"scope"`
	Visibility  string    `json:"visibility"`
	DefaultRole Role      `json:"default_role"`
	Status      string    `json:"status"`
	CreatedBy   string    `json:"created_by_principal_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

const scopeColumns = `scope_type, scope_key, visibility, default_role, status, COALESCE(created_by_principal_id::text, ''), created_at, updated_at`

func scanScope(row *sql.Row) (ScopeInfo, bool, error) {
	var si ScopeInfo
	var t, def string
	err := row.Scan(&t, &si.Scope.Key, &si.Visibility, &def, &si.Status, &si.CreatedBy, &si.CreatedAt, &si.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ScopeInfo{}, false, nil
	}
	if err != nil {
		return ScopeInfo{}, false, err
	}
	si.Scope.Type = ScopeType(t)
	si.DefaultRole = Role(def)
	return si, true, nil
}

// GetScope lee un ámbito.
func (p *PGStore) GetScope(ctx context.Context, s Scope) (ScopeInfo, bool, error) {
	return scanScope(p.db.QueryRowContext(ctx, `SELECT `+scopeColumns+` FROM nexo_scopes WHERE scope_type = $1 AND scope_key = $2`, string(s.Type), s.Key))
}

func parseBigint(s string) any {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n <= 0 {
		return nil
	}
	return n
}

func nullText(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

func truncate(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n])
}
