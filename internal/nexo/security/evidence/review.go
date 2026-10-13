package evidence

// S4-04 (RS-33, design §5.4 y §8.4): revisión de accesos (ISO 27002 5.18,
// NIST AC-2 y AC-2(3)).
//
//   - Report: por persona, rol de plataforma y roles por ámbito (proyectos y
//     espacios) con fecha y quién los asignó, permisos de proyecto de Engram,
//     tokens con caducidad, alcance y último uso, días de inactividad y
//     marcas (inactiva, token sin caducidad, token nunca usado). Nunca incluye
//     hashes ni prefijos de token.
//   - CompleteReview: registra que la revisión se hizo (quién, cuándo, notas y
//     el hash del reporte revisado) en nexo_access_reviews (solo inserción).
//   - DisableInactive: con NEXO_DISABLE_INACTIVE_DAYS deshabilita cuentas sin
//     uso, con evento y alerta; nunca al último superadmin habilitado.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Variables de la revisión de accesos.
const (
	// DisableInactiveEnv: días sin uso tras los cuales una cuenta se
	// deshabilita (vacío = desactivado, DS-4).
	DisableInactiveEnv = "NEXO_DISABLE_INACTIVE_DAYS"
	// ReviewInactiveDays es el umbral por defecto para marcar una cuenta como
	// inactiva en el reporte (sin deshabilitarla).
	ReviewInactiveDays = 90
	// ActionDisabledInactive es la acción del evento en nexo_authz_events.
	ActionDisabledInactive = "principal.disabled_inactive"
	// MinDisableInactiveDays evita deshabilitar por error con un plazo absurdo.
	MinDisableInactiveDays = 7
)

func migrateReviewsAndExports(ctx context.Context, tx *sql.Tx) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS nexo_access_reviews (
			id                    BIGSERIAL PRIMARY KEY,
			occurred_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			reviewer_principal_id BIGINT,
			reviewer_token_id     BIGINT,
			reviewer_label        TEXT NOT NULL,
			notes                 TEXT,
			summary               JSONB NOT NULL DEFAULT '{}'::jsonb,
			report_sha256         TEXT NOT NULL,
			client_ip             TEXT,
			user_agent            TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_nexo_access_reviews_occurred ON nexo_access_reviews (occurred_at DESC)`,
		`CREATE TABLE IF NOT EXISTS nexo_audit_exports (
			id           BIGSERIAL PRIMARY KEY,
			occurred_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			table_name   TEXT NOT NULL,
			day          DATE NOT NULL,
			file_name    TEXT NOT NULL,
			rows         BIGINT NOT NULL,
			first_seq    BIGINT,
			last_seq     BIGINT,
			last_hash    TEXT,
			file_sha256  TEXT NOT NULL,
			hmac_sha256  TEXT NOT NULL,
			key_id       TEXT NOT NULL,
			created_by   TEXT NOT NULL,
			UNIQUE (table_name, day)
		)`,
	}
	for _, q := range stmts {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("evidence: revisión y exportación: %w", err)
		}
	}
	return nil
}

// ─── Reporte ─────────────────────────────────────────────────────────────────

// ReviewBinding es un rol en un ámbito.
type ReviewBinding struct {
	ScopeType string    `json:"scope_type"`
	ScopeKey  string    `json:"scope_key"`
	Layer     Layer     `json:"layer"`
	Role      string    `json:"role"`
	Since     time.Time `json:"since"`
	GrantedBy string    `json:"granted_by,omitempty"`
}

// ReviewToken es un token sin datos secretos.
type ReviewToken struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Scope      string     `json:"scope"`
	CreatedAt  time.Time  `json:"created_at"`
	CreatedBy  string     `json:"created_by,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at"`
	NoExpiry   bool       `json:"no_expiry"`
	Expired    bool       `json:"expired"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

// ReviewPerson es una persona (o cuenta de servicio) en el reporte.
type ReviewPerson struct {
	PrincipalID    string          `json:"principal_id"`
	Username       string          `json:"username,omitempty"`
	Email          string          `json:"email,omitempty"`
	DisplayName    string          `json:"display_name"`
	Kind           string          `json:"kind"`
	Enabled        bool            `json:"enabled"`
	CreatedAt      time.Time       `json:"created_at"`
	EngramRole     string          `json:"engram_role"`
	PlatformRole   string          `json:"platform_role"`
	PlatformSince  *time.Time      `json:"platform_role_since,omitempty"`
	PlatformBy     string          `json:"platform_role_granted_by,omitempty"`
	Scopes         []ReviewBinding `json:"scope_roles"`
	ProjectGrants  []ReviewBinding `json:"engram_project_grants"`
	Tokens         []ReviewToken   `json:"tokens"`
	LastActivityAt *time.Time      `json:"last_activity_at"`
	InactiveDays   int             `json:"inactive_days"`
	Flags          []string        `json:"flags"`
}

// ReviewSummary son los totales del reporte.
type ReviewSummary struct {
	People         int `json:"people"`
	Enabled        int `json:"enabled"`
	Inactive       int `json:"inactive"`
	ActiveTokens   int `json:"active_tokens"`
	TokensNoExpiry int `json:"tokens_no_expiry"`
	TokensNeverUse int `json:"tokens_never_used"`
	ExpiringIn30d  int `json:"tokens_expiring_30d"`
	PlatformStaff  int `json:"platform_staff"`
}

// ReviewRecord es una revisión registrada.
type ReviewRecord struct {
	ID           int64          `json:"id"`
	OccurredAt   time.Time      `json:"occurred_at"`
	ReviewerID   string         `json:"reviewer_principal_id,omitempty"`
	Reviewer     string         `json:"reviewer"`
	Notes        string         `json:"notes,omitempty"`
	Summary      map[string]any `json:"summary"`
	ReportSHA256 string         `json:"report_sha256"`
}

// AccessReview es el reporte completo.
type AccessReview struct {
	GeneratedAt     time.Time      `json:"generated_at"`
	InactiveDays    int            `json:"inactive_threshold_days"`
	DisableInactive int            `json:"disable_inactive_days"` // 0 = desactivado
	Summary         ReviewSummary  `json:"summary"`
	People          []ReviewPerson `json:"people"`
	LastReview      *ReviewRecord  `json:"last_review"`
	RecentReviews   []ReviewRecord `json:"recent_reviews"`
}

func layerOf(scopeType string) Layer {
	switch scopeType {
	case "project":
		return LayerProjects
	case "space":
		return LayerKnowledge
	}
	return LayerPlatform
}

// BuildAccessReview arma el reporte. inactiveDays = umbral de la marca
// "inactive" (0 = ReviewInactiveDays).
func BuildAccessReview(ctx context.Context, db *sql.DB, inactiveDays int, now time.Time) (AccessReview, error) {
	if inactiveDays <= 0 {
		inactiveDays = ReviewInactiveDays
	}
	rep := AccessReview{GeneratedAt: now.UTC(), InactiveDays: inactiveDays, DisableInactive: DisableInactiveDays()}
	people := map[string]*ReviewPerson{}
	var order []string

	rows, err := db.QueryContext(ctx, `
		SELECT p.id::text, COALESCE(u.username, ''), COALESCE(u.email, ''), p.display_name, p.kind, p.enabled, p.created_at, p.role,
			GREATEST(p.created_at,
				(SELECT max(t.last_used_at) FROM cloud_principal_tokens t WHERE t.principal_id = p.id),
				(SELECT max(t.created_at) FROM cloud_principal_tokens t WHERE t.principal_id = p.id),
				(SELECT max(s.last_seen_at) FROM nexo_actor_presence s WHERE s.actor_principal_id = p.id::text)) AS last_activity
		FROM cloud_principals p LEFT JOIN cloud_human_users u ON u.principal_id = p.id
		ORDER BY p.id`)
	if err != nil {
		return rep, fmt.Errorf("evidence: revisión: personas: %w", err)
	}
	for rows.Next() {
		var p ReviewPerson
		var last sql.NullTime
		if err := rows.Scan(&p.PrincipalID, &p.Username, &p.Email, &p.DisplayName, &p.Kind, &p.Enabled, &p.CreatedAt, &p.EngramRole, &last); err != nil {
			_ = rows.Close()
			return rep, fmt.Errorf("evidence: revisión: personas: %w", err)
		}
		p.Scopes, p.ProjectGrants, p.Tokens, p.Flags = []ReviewBinding{}, []ReviewBinding{}, []ReviewToken{}, []string{}
		if last.Valid {
			v := last.Time
			p.LastActivityAt = &v
			p.InactiveDays = int(now.Sub(v).Hours() / 24)
		}
		// Rol de plataforma por compatibilidad: admin de Engram = admin (NX-05).
		p.PlatformRole = "miembro"
		if p.EngramRole == "admin" {
			p.PlatformRole = "admin"
		}
		people[p.PrincipalID] = &p
		order = append(order, p.PrincipalID)
	}
	if err := rows.Close(); err != nil {
		return rep, err
	}

	rows, err = db.QueryContext(ctx, `
		SELECT b.principal_id::text, b.scope_type, b.scope_key, b.role, b.updated_at, COALESCE(g.display_name, '')
		FROM nexo_role_bindings b LEFT JOIN cloud_principals g ON g.id = b.granted_by_principal_id
		ORDER BY b.principal_id, b.scope_type, b.scope_key`)
	if err != nil {
		return rep, fmt.Errorf("evidence: revisión: roles: %w", err)
	}
	for rows.Next() {
		var pid string
		var b ReviewBinding
		if err := rows.Scan(&pid, &b.ScopeType, &b.ScopeKey, &b.Role, &b.Since, &b.GrantedBy); err != nil {
			_ = rows.Close()
			return rep, fmt.Errorf("evidence: revisión: roles: %w", err)
		}
		p := people[pid]
		if p == nil {
			continue
		}
		b.Layer = layerOf(b.ScopeType)
		if b.ScopeType == "platform" {
			since := b.Since
			p.PlatformRole, p.PlatformSince, p.PlatformBy = b.Role, &since, b.GrantedBy
			continue
		}
		p.Scopes = append(p.Scopes, b)
	}
	if err := rows.Close(); err != nil {
		return rep, err
	}

	rows, err = db.QueryContext(ctx, `
		SELECT g.principal_id::text, g.project, g.created_at, COALESCE(b.display_name, '')
		FROM cloud_project_grants g LEFT JOIN cloud_principals b ON b.id = g.granted_by_principal_id
		ORDER BY g.principal_id, g.project`)
	if err != nil {
		return rep, fmt.Errorf("evidence: revisión: permisos de proyecto: %w", err)
	}
	for rows.Next() {
		var pid string
		b := ReviewBinding{ScopeType: "project", Layer: LayerProjects, Role: "publicador (permiso de Engram)"}
		if err := rows.Scan(&pid, &b.ScopeKey, &b.Since, &b.GrantedBy); err != nil {
			_ = rows.Close()
			return rep, fmt.Errorf("evidence: revisión: permisos de proyecto: %w", err)
		}
		if p := people[pid]; p != nil {
			p.ProjectGrants = append(p.ProjectGrants, b)
		}
	}
	if err := rows.Close(); err != nil {
		return rep, err
	}

	rows, err = db.QueryContext(ctx, `
		SELECT t.principal_id::text, t.id::text, t.name, t.nexo_scope, t.created_at, COALESCE(c.display_name, ''),
			t.nexo_expires_at, (t.nexo_expires_at IS NOT NULL AND t.nexo_expires_at <= NOW()), t.last_used_at
		FROM cloud_principal_tokens t LEFT JOIN cloud_principals c ON c.id = t.created_by_principal_id
		WHERE t.revoked_at IS NULL
		ORDER BY t.principal_id, t.id`)
	if err != nil {
		return rep, fmt.Errorf("evidence: revisión: tokens: %w", err)
	}
	for rows.Next() {
		var pid string
		var t ReviewToken
		var exp, used sql.NullTime
		if err := rows.Scan(&pid, &t.ID, &t.Name, &t.Scope, &t.CreatedAt, &t.CreatedBy, &exp, &t.Expired, &used); err != nil {
			_ = rows.Close()
			return rep, fmt.Errorf("evidence: revisión: tokens: %w", err)
		}
		if exp.Valid {
			v := exp.Time
			t.ExpiresAt = &v
		} else {
			t.NoExpiry = true
		}
		if used.Valid {
			v := used.Time
			t.LastUsedAt = &v
		}
		if p := people[pid]; p != nil {
			p.Tokens = append(p.Tokens, t)
		}
	}
	if err := rows.Close(); err != nil {
		return rep, err
	}

	for _, id := range order {
		p := people[id]
		if p.Enabled {
			rep.Summary.Enabled++
			if p.LastActivityAt != nil && p.InactiveDays >= inactiveDays {
				p.Flags = append(p.Flags, "inactive")
				rep.Summary.Inactive++
			}
		} else {
			p.Flags = append(p.Flags, "disabled")
		}
		if p.PlatformRole != "miembro" {
			rep.Summary.PlatformStaff++
		}
		noExp, never := false, false
		for _, t := range p.Tokens {
			if t.Expired {
				continue
			}
			rep.Summary.ActiveTokens++
			if t.NoExpiry {
				rep.Summary.TokensNoExpiry++
				noExp = true
			}
			if t.LastUsedAt == nil {
				rep.Summary.TokensNeverUse++
				never = true
			}
			if t.ExpiresAt != nil && t.ExpiresAt.Sub(now) < 30*24*time.Hour {
				rep.Summary.ExpiringIn30d++
			}
		}
		if noExp {
			p.Flags = append(p.Flags, "token_no_expiry")
		}
		if never {
			p.Flags = append(p.Flags, "token_never_used")
		}
		rep.People = append(rep.People, *p)
	}
	rep.Summary.People = len(rep.People)
	if rep.People == nil {
		rep.People = []ReviewPerson{}
	}

	recent, err := ListReviews(ctx, db, 10)
	if err != nil {
		return rep, err
	}
	rep.RecentReviews = recent
	if len(recent) > 0 {
		r := recent[0]
		rep.LastReview = &r
	}
	return rep, nil
}

// ReportDigest es el sha256 del contenido revisado (personas, roles y
// tokens; sin la hora de generación ni las revisiones anteriores).
func ReportDigest(rep AccessReview) string {
	view := struct {
		Summary ReviewSummary  `json:"summary"`
		People  []ReviewPerson `json:"people"`
	}{rep.Summary, rep.People}
	// Los días de inactividad cambian con el reloj: no forman parte del
	// contenido revisado.
	people := make([]ReviewPerson, len(view.People))
	copy(people, view.People)
	for i := range people {
		people[i].InactiveDays = 0
	}
	view.People = people
	raw, _ := json.Marshal(view)
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("%x", sum[:])
}

// ListReviews devuelve las últimas revisiones registradas.
func ListReviews(ctx context.Context, db *sql.DB, limit int) ([]ReviewRecord, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, occurred_at, COALESCE(reviewer_principal_id::text, ''), reviewer_label, COALESCE(notes, ''), summary, report_sha256
		FROM nexo_access_reviews ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("evidence: revisiones: %w", err)
	}
	defer rows.Close()
	out := []ReviewRecord{}
	for rows.Next() {
		var r ReviewRecord
		var sum []byte
		if err := rows.Scan(&r.ID, &r.OccurredAt, &r.ReviewerID, &r.Reviewer, &r.Notes, &sum, &r.ReportSHA256); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(sum, &r.Summary)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Reviewer es quien registra la revisión.
type Reviewer struct {
	PrincipalID, TokenID, Label, ClientIP, UserAgent string
}

// CompleteReview registra la revisión con el reporte vigente.
func CompleteReview(ctx context.Context, db *sql.DB, rep AccessReview, who Reviewer, notes string) (ReviewRecord, error) {
	notes = strings.TrimSpace(notes)
	if len([]rune(notes)) > 4000 {
		notes = string([]rune(notes)[:4000])
	}
	summary, _ := json.Marshal(map[string]any{
		"people": rep.Summary.People, "enabled": rep.Summary.Enabled, "inactive": rep.Summary.Inactive,
		"active_tokens": rep.Summary.ActiveTokens, "tokens_no_expiry": rep.Summary.TokensNoExpiry,
		"tokens_never_used": rep.Summary.TokensNeverUse, "platform_staff": rep.Summary.PlatformStaff,
		"inactive_threshold_days": rep.InactiveDays, "generated_at": rep.GeneratedAt,
	})
	digest := ReportDigest(rep)
	var r ReviewRecord
	var sum []byte
	err := db.QueryRowContext(ctx, `
		INSERT INTO nexo_access_reviews (reviewer_principal_id, reviewer_token_id, reviewer_label, notes, summary, report_sha256, client_ip, user_agent)
		VALUES (NULLIF($1, '')::bigint, NULLIF($2, '')::bigint, $3, NULLIF($4, ''), $5::jsonb, $6, NULLIF($7, ''), NULLIF($8, ''))
		RETURNING id, occurred_at, COALESCE(reviewer_principal_id::text, ''), reviewer_label, COALESCE(notes, ''), summary, report_sha256`,
		who.PrincipalID, who.TokenID, labelOr(who.Label), notes, string(summary), digest, who.ClientIP, who.UserAgent).
		Scan(&r.ID, &r.OccurredAt, &r.ReviewerID, &r.Reviewer, &r.Notes, &sum, &r.ReportSHA256)
	if err != nil {
		return r, fmt.Errorf("evidence: registrar revisión: %w", err)
	}
	_ = json.Unmarshal(sum, &r.Summary)
	return r, nil
}

// ─── Deshabilitar inactivos (DS-4) ───────────────────────────────────────────

// DisableInactiveDays lee NEXO_DISABLE_INACTIVE_DAYS (0 = desactivado).
func DisableInactiveDays() int {
	raw := strings.TrimSpace(os.Getenv(DisableInactiveEnv))
	if raw == "" || raw == "0" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < MinDisableInactiveDays {
		log.Printf("[nexo] %s=%q inválido (mínimo %d): no se deshabilitan cuentas inactivas", DisableInactiveEnv, raw, MinDisableInactiveDays)
		return 0
	}
	return n
}

// DisabledAccount es una cuenta deshabilitada por inactividad.
type DisabledAccount struct {
	PrincipalID    string    `json:"principal_id"`
	DisplayName    string    `json:"display_name"`
	LastActivityAt time.Time `json:"last_activity_at"`
	InactiveDays   int       `json:"inactive_days"`
}

// DisableInactive deshabilita las cuentas habilitadas sin actividad en days
// días. Nunca deshabilita al último superadmin habilitado (lo salta; además
// lo impide el trigger de NX-05). Cada cuenta deshabilitada deja el evento
// principal.disabled_inactive (nexo_authz_events), user.disable en
// cloud_auth_audit_log y una alerta.
func DisableInactive(ctx context.Context, db *sql.DB, days int, now time.Time, rule RuleConfig) ([]DisabledAccount, []Alert, error) {
	if days <= 0 {
		return nil, nil, nil
	}
	rows, err := db.QueryContext(ctx, `
		SELECT p.id::text, p.display_name, x.last_activity
		FROM cloud_principals p
		CROSS JOIN LATERAL (SELECT GREATEST(p.created_at,
				(SELECT max(t.last_used_at) FROM cloud_principal_tokens t WHERE t.principal_id = p.id),
				(SELECT max(t.created_at) FROM cloud_principal_tokens t WHERE t.principal_id = p.id),
				(SELECT max(s.last_seen_at) FROM nexo_actor_presence s WHERE s.actor_principal_id = p.id::text)) AS last_activity) x
		WHERE p.enabled AND x.last_activity < $1
		ORDER BY p.id`, now.Add(-time.Duration(days)*24*time.Hour))
	if err != nil {
		return nil, nil, fmt.Errorf("evidence: inactivos: %w", err)
	}
	type cand struct {
		id, name string
		last     time.Time
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.name, &c.last); err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		cands = append(cands, c)
	}
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}
	var done []DisabledAccount
	var alerts []Alert
	for _, c := range cands {
		acc, ok, err := disableOne(ctx, db, c.id, c.name, c.last, days, now)
		if err != nil {
			log.Printf("[nexo] seguridad: no se pudo deshabilitar la cuenta inactiva %s: %v", c.id, err)
			continue
		}
		if !ok {
			continue
		}
		done = append(done, acc)
		if rule.Enabled {
			a, err := InsertAlert(ctx, db, NewAlert{
				Rule: RuleInactiveDisabled, Severity: rule.Severity, Layer: LayerPlatform, Subject: "principal:" + acc.PrincipalID,
				DedupKey: fmt.Sprintf("%s|%s|%s", RuleInactiveDisabled, acc.PrincipalID, now.UTC().Format("20060102")),
				Summary:  fmt.Sprintf("cuenta %s (principal:%s) deshabilitada por %d días sin uso (umbral %d)", acc.DisplayName, acc.PrincipalID, acc.InactiveDays, days),
				Details:  map[string]any{"principal_id": acc.PrincipalID, "inactive_days": acc.InactiveDays, "threshold_days": days, "last_activity_at": acc.LastActivityAt.UTC().Format(time.RFC3339)},
			})
			if err != nil {
				return done, alerts, err
			}
			if a != nil {
				alerts = append(alerts, *a)
			}
		}
	}
	sort.Slice(done, func(i, j int) bool { return done[i].PrincipalID < done[j].PrincipalID })
	return done, alerts, nil
}

func disableOne(ctx context.Context, db *sql.DB, id, name string, last time.Time, days int, now time.Time) (DisabledAccount, bool, error) {
	acc := DisabledAccount{PrincipalID: id, DisplayName: name, LastActivityAt: last, InactiveDays: int(now.Sub(last).Hours() / 24)}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return acc, false, err
	}
	defer func() { _ = tx.Rollback() }()
	// Mismo candado que las reglas del último superadmin (NX-05, R-8).
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('nexo_authz_superadmin_guard'))`); err != nil {
		return acc, false, err
	}
	var lastSuper bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM nexo_role_bindings b WHERE b.principal_id = $1::bigint AND b.scope_type = 'platform' AND b.role = 'superadmin')
		   AND NOT EXISTS (SELECT 1 FROM nexo_role_bindings b JOIN cloud_principals p ON p.id = b.principal_id
				WHERE b.scope_type = 'platform' AND b.scope_key = '*' AND b.role = 'superadmin' AND p.enabled AND b.principal_id <> $1::bigint)`, id).Scan(&lastSuper); err != nil {
		return acc, false, err
	}
	if lastSuper {
		log.Printf("[nexo] seguridad: la cuenta inactiva %s es el último superadmin habilitado: no se deshabilita", id)
		return acc, false, nil
	}
	res, err := tx.ExecContext(ctx, `UPDATE cloud_principals SET enabled = FALSE, updated_at = NOW() WHERE id = $1::bigint AND enabled`, id)
	if err != nil {
		return acc, false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return acc, false, nil
	}
	details, _ := json.Marshal(map[string]any{"inactive_days": acc.InactiveDays, "threshold_days": days,
		"last_activity_at": last.UTC().Format(time.RFC3339), "rule": "NEXO_DISABLE_INACTIVE_DAYS"})
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO nexo_authz_events (action, scope_type, scope_key, target_principal_id, actor_label, details)
		VALUES ($1, 'platform', '*', $2::bigint, 'sistema', $3::jsonb)`, ActionDisabledInactive, id, string(details)); err != nil {
		return acc, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO cloud_auth_audit_log (actor_source, target_principal_id, action, outcome, reason_code, metadata)
		VALUES ('nexo_security', $1::bigint, 'user.disable', 'success', 'inactive', $2::jsonb)`, id, string(details)); err != nil {
		return acc, false, err
	}
	if err := tx.Commit(); err != nil {
		return acc, false, err
	}
	return acc, true, nil
}
