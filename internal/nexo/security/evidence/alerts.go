package evidence

// S4-03 (RS-32, design §5.3 y §8.4): alertas.
//
//   - nexo_alerts (solo inserción para la aplicación) y nexo_alert_acks (el
//     reconocimiento es una fila aparte: no hace falta UPDATE).
//   - Evaluador: una pasada por minuto en una sola réplica a la vez
//     (pg_try_advisory_xact_lock). Cada regla es una consulta sobre las
//     tablas de eventos de la ventana reciente que inserta las alertas nuevas
//     (INSERT ... ON CONFLICT DO NOTHING) respetando un tiempo de espera por
//     regla y sujeto, así una ráfaga sostenida no genera una alerta por
//     minuto.
//   - Cada alerta dice de qué capa viene: plataforma (identidades, tokens,
//     roles de plataforma), proyectos (sincronización de Engram) o
//     conocimiento (espacios nexo_kb_*).
//   - Webhook opcional (NEXO_ALERT_WEBHOOK_URL), ver webhook.go.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/redact"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
)

// Reglas (design §5.3 más las del arreglo A1/A2).
const (
	RuleActivationRepeated    = "activation_repeated"
	RuleActivationSameIP      = "activation_same_ip" // NX-07: canje desde la IP del emisor en menos de 1 minuto (indicio)
	RuleAuthzDeniedBurst      = "authz_denied_burst"
	RuleAuthFailuresBurst     = "auth_failures_burst"
	RulePlatformRoleChanged   = "platform_role_changed"
	RuleSharedTokenUsed       = "shared_token_used"
	RuleExpiredTokenUsed      = "expired_token_used"
	RuleRevokedTokenUsed      = "revoked_token_used"
	RuleInvalidTokenRepeated  = "invalid_token_repeated"
	RuleAuditChainBroken      = "audit_chain_broken"
	RuleLoggingStalled        = "logging_stalled"
	RuleInactiveDisabled      = "inactive_account_disabled"
	RuleAuditExportFailed     = "audit_export_failed"
	SeverityInfo              = "info"
	SeverityWarning           = "warning"
	SeverityHigh              = "high"
	SeverityCritical          = "critical"
	AlertRulesEnv             = "NEXO_ALERT_RULES"
	alertEvaluatorLockSQL     = `SELECT pg_try_advisory_xact_lock(hashtext('nexo_alert_evaluator'))`
	alertDefaultLookbackExtra = 3 * time.Minute
)

// AllRules lista las reglas (filtro de la API).
var AllRules = []string{
	RuleActivationRepeated, RuleActivationSameIP,
	RuleAuthzDeniedBurst, RuleAuthFailuresBurst, RulePlatformRoleChanged, RuleSharedTokenUsed,
	RuleExpiredTokenUsed, RuleRevokedTokenUsed, RuleInvalidTokenRepeated, RuleAuditChainBroken,
	RuleLoggingStalled, RuleInactiveDisabled, RuleAuditExportFailed,
	RuleKBDocExpiring, RuleKBReviewDue, // NX-10 2b (KP-09, alerts_kb.go)
	RuleSAStale,           // SG-10: cuentas de servicio sin uso (serviceaccounts.go)
	RulePlatformRoleDrift, // NX-05: rol de Engram distinto del rol de Nexo (role_drift.go)
	// SG-06: continuidad del acceso de administracion (continuity_alerts.go).
	RuleSingleSuperadmin, RulePrivilegedTokenExpiring, RuleEmergencyAccessUsed,
}

// RuleConfig es la configuración de una regla.
type RuleConfig struct {
	Enabled   bool          `json:"enabled"`
	Threshold int           `json:"threshold"`
	Window    time.Duration `json:"-"`
	Cooldown  time.Duration `json:"-"`
	Severity  string        `json:"severity"`
}

// RuleSet es la configuración de todas las reglas.
type RuleSet map[string]RuleConfig

// DefaultRules: valores por defecto (design §5.3). logging_stalled viene
// desactivada (solo sirve donde se espera sincronización continua).
func DefaultRules() RuleSet {
	return RuleSet{
		RuleActivationRepeated:   {Enabled: true, Threshold: 2, Severity: SeverityHigh},
		RuleActivationSameIP:     {Enabled: true, Threshold: 1, Severity: SeverityHigh},
		RuleAuthzDeniedBurst:     {Enabled: true, Threshold: 5, Window: time.Minute, Cooldown: 10 * time.Minute, Severity: SeverityHigh},
		RuleAuthFailuresBurst:    {Enabled: true, Threshold: 10, Window: 5 * time.Minute, Cooldown: 30 * time.Minute, Severity: SeverityHigh},
		RulePlatformRoleChanged:  {Enabled: true, Threshold: 1, Window: 10 * time.Minute, Severity: SeverityHigh},
		RuleSharedTokenUsed:      {Enabled: true, Threshold: 1, Window: 10 * time.Minute, Cooldown: time.Hour, Severity: SeverityWarning},
		RuleExpiredTokenUsed:     {Enabled: true, Threshold: 1, Window: 10 * time.Minute, Cooldown: 24 * time.Hour, Severity: SeverityInfo},
		RuleRevokedTokenUsed:     {Enabled: true, Threshold: 1, Window: 10 * time.Minute, Cooldown: 24 * time.Hour, Severity: SeverityWarning},
		RuleInvalidTokenRepeated: {Enabled: true, Threshold: 10, Window: 30 * time.Minute, Cooldown: 24 * time.Hour, Severity: SeverityWarning},
		RuleAuditChainBroken:     {Enabled: true, Threshold: 1, Window: time.Hour, Cooldown: 6 * time.Hour, Severity: SeverityCritical},
		RuleLoggingStalled:       {Enabled: false, Threshold: 1, Window: 30 * time.Minute, Cooldown: 24 * time.Hour, Severity: SeverityHigh},
		RuleInactiveDisabled:     {Enabled: true, Threshold: 1, Severity: SeverityWarning},
		RuleAuditExportFailed:    {Enabled: true, Threshold: 1, Cooldown: 6 * time.Hour, Severity: SeverityHigh},
		// NX-10 2b (KP-09): ventana de aviso de vencimiento (window_seconds) y
		// revisión según settings.review_every_days de cada espacio.
		RuleKBDocExpiring: {Enabled: true, Threshold: 1, Window: 30 * 24 * time.Hour, Severity: SeverityInfo},
		RuleKBReviewDue:   {Enabled: true, Threshold: 1, Severity: SeverityInfo},
		// SG-10: el plazo sale de NEXO_SA_STALE_DAYS; la regla permite apagarla
		// o cambiar la severidad con NEXO_ALERT_RULES.
		RuleSAStale: {Enabled: true, Threshold: 1, Severity: SeverityWarning},
		// NX-05: la columna role de Engram se deriva del rol de Nexo; una
		// desalineación es señal de que alguien evitó los triggers.
		RulePlatformRoleDrift: {Enabled: true, Threshold: 1, Severity: SeverityHigh},
		// SG-06: un solo superadmin (recordatorio semanal), token privilegiado
		// por vencer (aviso a 30 y 7 dias; a 7 sube a alta) y acceso de emergencia.
		RuleSingleSuperadmin:        {Enabled: true, Threshold: 1, Cooldown: 7 * 24 * time.Hour, Severity: SeverityHigh},
		RulePrivilegedTokenExpiring: {Enabled: true, Threshold: 1, Severity: SeverityWarning},
		RuleEmergencyAccessUsed:     {Enabled: true, Threshold: 1, Severity: SeverityHigh},
	}
}

type ruleJSON struct {
	Enabled         *bool   `json:"enabled"`
	Threshold       *int    `json:"threshold"`
	WindowSeconds   *int    `json:"window_seconds"`
	CooldownSeconds *int    `json:"cooldown_seconds"`
	Severity        *string `json:"severity"`
}

// ParseRules interpreta NEXO_ALERT_RULES: "" (por defecto), "off" (todas
// desactivadas) o JSON por regla, por ejemplo
// {"authz_denied_burst":{"threshold":3},"logging_stalled":{"enabled":true,"window_seconds":1800}}.
// Ante un valor inválido devuelve los valores por defecto y el error.
func ParseRules(raw string) (RuleSet, error) {
	rules := DefaultRules()
	raw = strings.TrimSpace(raw)
	switch strings.ToLower(raw) {
	case "":
		return rules, nil
	case "off", "false", "0", "disabled":
		for k, r := range rules {
			r.Enabled = false
			rules[k] = r
		}
		return rules, nil
	}
	var in map[string]ruleJSON
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return DefaultRules(), fmt.Errorf("%s: JSON inválido: %w", AlertRulesEnv, err)
	}
	for name, v := range in {
		r, ok := rules[name]
		if !ok {
			return DefaultRules(), fmt.Errorf("%s: regla desconocida %q", AlertRulesEnv, name)
		}
		if v.Enabled != nil {
			r.Enabled = *v.Enabled
		}
		if v.Threshold != nil {
			r.Threshold = *v.Threshold
		}
		if v.WindowSeconds != nil {
			r.Window = time.Duration(*v.WindowSeconds) * time.Second
		}
		if v.CooldownSeconds != nil {
			r.Cooldown = time.Duration(*v.CooldownSeconds) * time.Second
		}
		if v.Severity != nil {
			r.Severity = *v.Severity
		}
		switch r.Severity {
		case SeverityInfo, SeverityWarning, SeverityHigh, SeverityCritical:
		default:
			return DefaultRules(), fmt.Errorf("%s: %s: severity debe ser info, warning, high o critical", AlertRulesEnv, name)
		}
		maxWindow := 7 * 24 * time.Hour
		if name == RuleKBDocExpiring {
			maxWindow = 366 * 24 * time.Hour // NX-10 2b: aviso de vencimiento en días
		}
		if r.Threshold < 1 || r.Window < 0 || r.Cooldown < 0 || r.Window > maxWindow {
			return DefaultRules(), fmt.Errorf("%s: %s: threshold >= 1, window_seconds y cooldown_seconds >= 0", AlertRulesEnv, name)
		}
		rules[name] = r
	}
	return rules, nil
}

// String resume las reglas activas para el log de arranque.
func (rs RuleSet) String() string {
	var on []string
	for _, name := range AllRules {
		r := rs[name]
		if !r.Enabled {
			continue
		}
		if r.Window > 0 && r.Threshold > 1 {
			on = append(on, fmt.Sprintf("%s(%d/%s)", name, r.Threshold, r.Window))
		} else {
			on = append(on, name)
		}
	}
	if len(on) == 0 {
		return "ninguna"
	}
	return strings.Join(on, " ")
}

// ─── Migración ───────────────────────────────────────────────────────────────

func migrateAlerts(ctx context.Context, tx *sql.Tx) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS nexo_alerts (
			id          BIGSERIAL PRIMARY KEY,
			occurred_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			rule        TEXT NOT NULL,
			severity    TEXT NOT NULL CHECK (severity IN ('info', 'warning', 'high', 'critical')),
			layer       TEXT NOT NULL CHECK (layer IN ('plataforma', 'proyectos', 'conocimiento')),
			subject     TEXT NOT NULL DEFAULT '',
			dedup_key   TEXT NOT NULL UNIQUE,
			summary     TEXT NOT NULL,
			details     JSONB
		)`,
		`CREATE INDEX IF NOT EXISTS idx_nexo_alerts_occurred ON nexo_alerts (occurred_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_nexo_alerts_rule_subject ON nexo_alerts (rule, layer, subject, occurred_at DESC)`,
		`CREATE TABLE IF NOT EXISTS nexo_alert_acks (
			id                           BIGSERIAL PRIMARY KEY,
			occurred_at                  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			alert_id                     BIGINT NOT NULL UNIQUE REFERENCES nexo_alerts(id),
			acknowledged_by_principal_id BIGINT,
			acknowledged_by_token_id     BIGINT,
			acknowledged_by_label        TEXT NOT NULL,
			note                         TEXT,
			client_ip                    TEXT,
			user_agent                   TEXT
		)`,
	}
	for _, q := range stmts {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("evidence: alertas: %w", err)
		}
	}
	return nil
}

// ─── Modelo ──────────────────────────────────────────────────────────────────

// Alert es una alerta.
type Alert struct {
	authz.NexoScopeSource `json:"-"`     // NX-07: ámbito original, privado
	ID                    int64          `json:"id"`
	OccurredAt            time.Time      `json:"occurred_at"`
	Rule                  string         `json:"rule"`
	Severity              string         `json:"severity"`
	Layer                 Layer          `json:"layer"`
	Subject               string         `json:"subject"`
	Summary               string         `json:"summary"`
	Details               map[string]any `json:"details,omitempty"`
	Ack                   *AlertAck      `json:"ack"`
}

// AlertAck es el reconocimiento de una alerta.
type AlertAck struct {
	OccurredAt  time.Time `json:"acknowledged_at"`
	PrincipalID string    `json:"acknowledged_by_principal_id,omitempty"`
	Label       string    `json:"acknowledged_by"`
	Note        string    `json:"note,omitempty"`
	// NoteWithheld: la nota libre existe pero no se entrega sin lectura del ámbito de la alerta (R-6).
	NoteWithheld bool `json:"note_withheld,omitempty"`
}

// WithholdNote retira el texto libre de la nota del reconocimiento.
func (a *AlertAck) WithholdNote() {
	if strings.TrimSpace(a.Note) != "" {
		a.Note, a.NoteWithheld = "", true
	}
}

// NewAlert es una alerta a insertar directamente (reglas que no salen de una
// consulta: verificación de la cadena, exportación, inactivos).
type NewAlert struct {
	Rule     string
	Severity string
	Layer    Layer
	Subject  string
	DedupKey string
	Summary  string
	Details  map[string]any
	Cooldown time.Duration // 0 = sin espera (solo la clave única)
}

// InsertAlert inserta una alerta salvo que haya otra de la misma regla, capa
// y sujeto dentro del tiempo de espera o con la misma clave. Devuelve la
// alerta (nil si no se insertó).
func InsertAlert(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, a NewAlert) (*Alert, error) {
	// NX-07: sujeto, clave, resumen y detalles pueden traer texto externo (claves de documento,
	// responsables, proyectos): se quitan formatos de credencial antes de guardar.
	a.Subject = security.NexoRedactExternal(a.Subject, 0)
	a.DedupKey = security.NexoRedactExternal(a.DedupKey, 0)
	a.Summary = security.NexoRedactExternal(a.Summary, 0)
	a.Details = security.NexoRedactAuditMetadata(a.Details)
	if a.DedupKey == "" {
		a.DedupKey = a.Rule + "|" + string(a.Layer) + "|" + a.Subject + "|" + time.Now().UTC().Format("20060102T150405.000000")
	}
	var details []byte
	if a.Details != nil {
		raw, err := json.Marshal(a.Details)
		if err != nil {
			return nil, err
		}
		details = raw
	}
	var out Alert
	var det []byte
	err := q.QueryRowContext(ctx, `
		INSERT INTO nexo_alerts (rule, severity, layer, subject, dedup_key, summary, details)
		SELECT $1, $2, $3, $4, $5, $6, $7::jsonb
		WHERE $8::bigint = 0 OR NOT EXISTS (
			SELECT 1 FROM nexo_alerts a WHERE a.rule = $1 AND a.layer = $3 AND a.subject = $4
			  AND a.occurred_at > now() - make_interval(secs => $8::bigint))
		ON CONFLICT (dedup_key) DO NOTHING
		RETURNING id, occurred_at, rule, severity, layer, subject, summary, details`,
		a.Rule, a.Severity, string(a.Layer), a.Subject, a.DedupKey, a.Summary, nullJSON(details), int64(a.Cooldown.Seconds())).
		Scan(&out.ID, &out.OccurredAt, &out.Rule, &out.Severity, &out.Layer, &out.Subject, &out.Summary, &det)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("evidence: insertar alerta: %w", err)
	}
	if len(det) > 0 {
		_ = json.Unmarshal(det, &out.Details)
	}
	return &out, nil
}

func nullJSON(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}

// ─── Evaluador ───────────────────────────────────────────────────────────────

// burstRule es una regla de conteo en ventana deslizante. source devuelve
// (occurred_at, subject, layer, weight, extra) de los eventos desde $1.
type burstRule struct {
	name    string
	source  string
	summary string // formato: %[1]s sujeto, %[2]d conteo, %[3]d segundos de ventana, %[4]s capa
}

// Peso de una fila de cloud_auth_audit_log: una fila resumen del agrupador
// (A2) vale lo que contó; una fila normal vale 1.
const weightRepeatedSQL = `CASE WHEN a.metadata ? 'nexo_repeated' THEN COALESCE((a.metadata->>'nexo_repeated')::bigint, 0) ELSE 1 END`

var burstRules = []burstRule{
	{
		name: RuleAuthzDeniedBurst,
		source: `SELECT e.occurred_at,
				CASE WHEN e.actor_principal_id IS NOT NULL THEN 'principal:' || e.actor_principal_id ELSE 'ip:' || COALESCE(e.client_ip, '?') END,
				CASE e.scope_type WHEN 'project' THEN 'proyectos' WHEN 'space' THEN 'conocimiento' ELSE 'plataforma' END,
				1::bigint,
				jsonb_build_object('source', 'nexo_authz_events', 'id', e.id, 'actor', e.actor_label, 'client_ip', e.client_ip,
					'scope', e.scope_type || ':' || e.scope_key, 'capability', e.details->>'capability', 'reason', e.details->>'reason')
			FROM nexo_authz_events e WHERE e.action = 'authz.denied' AND e.occurred_at >= $1
			UNION ALL
			SELECT a.occurred_at,
				CASE WHEN a.actor_principal_id IS NOT NULL THEN 'principal:' || a.actor_principal_id ELSE 'ip:' || COALESCE(a.metadata->>'client_ip', '?') END,
				CASE WHEN a.action = 'sync.authorize' THEN 'proyectos' ELSE 'plataforma' END,
				1::bigint,
				jsonb_build_object('source', 'cloud_auth_audit_log', 'id', a.id, 'action', a.action, 'reason', a.reason_code,
					'project', a.project, 'client_ip', a.metadata->>'client_ip')
			FROM cloud_auth_audit_log a WHERE a.action IN ('sync.authorize', 'dashboard.authorize') AND a.outcome = 'denied' AND a.occurred_at >= $1`,
		summary: "%[2]d denegaciones de %[1]s en %[3]d s (capa %[4]s)",
	},
	{
		// Con el agrupador (A2) cada fila completa es un token distinto (o el
		// mismo después de 10 minutos): cuenta tokens distintos, no intentos.
		name: RuleAuthFailuresBurst,
		source: `SELECT a.occurred_at, 'ip:' || COALESCE(a.metadata->>'client_ip', '?'), 'plataforma',
				CASE WHEN a.metadata ? 'nexo_repeated' THEN 0 ELSE 1 END::bigint,
				jsonb_build_object('source', 'cloud_auth_audit_log', 'id', a.id, 'action', a.action, 'reason', a.reason_code,
					'client_ip', a.metadata->>'client_ip')
			FROM cloud_auth_audit_log a
			WHERE a.action IN ('sync.auth', 'dashboard.login') AND a.outcome = 'denied'
			  AND a.reason_code IN ('unknown_token', 'malformed_bearer', 'invalid_token') AND a.occurred_at >= $1`,
		summary: "%[2]d fallos de autenticación (tokens distintos) desde %[1]s en %[3]d s (capa %[4]s)",
	},
	{
		name: RuleSharedTokenUsed,
		source: `SELECT l.occurred_at, 'ip:' || COALESCE(l.client_ip, '?'), 'proyectos', 1::bigint,
				jsonb_build_object('source', 'nexo_activity_log', 'id', l.id, 'operation', l.operation, 'project', l.project,
					'client_ip', l.client_ip, 'client_claim', l.client_claim)
			FROM nexo_activity_log l WHERE l.actor = 'no-identificado' AND l.occurred_at >= $1`,
		summary: "uso del token compartido (no-identificado) desde %[1]s: %[2]d operaciones en %[3]d s (capa %[4]s)",
	},
	{
		name: RuleExpiredTokenUsed,
		source: `SELECT a.occurred_at,
				'token:' || COALESCE(a.metadata->>'credential_id', a.metadata->>'credential_fp', '?') || '|ip:' || COALESCE(a.metadata->>'client_ip', '?'),
				'plataforma', ` + weightRepeatedSQL + `,
				jsonb_build_object('source', 'cloud_auth_audit_log', 'id', a.id, 'action', a.action,
					'principal_id', a.metadata->>'nexo_principal_id', 'client_ip', a.metadata->>'client_ip', 'user_agent', a.metadata->>'user_agent')
			FROM cloud_auth_audit_log a
			WHERE a.action IN ('sync.auth', 'dashboard.login') AND a.outcome = 'denied' AND a.reason_code = 'token_expired' AND a.occurred_at >= $1`,
		summary: "uso de un token vencido (%[1]s): %[2]d intentos en %[3]d s (capa %[4]s)",
	},
	{
		name: RuleRevokedTokenUsed,
		source: `SELECT a.occurred_at,
				'principal:' || COALESCE(a.metadata->>'nexo_principal_id', '?') || '|ip:' || COALESCE(a.metadata->>'client_ip', '?'),
				'plataforma', ` + weightRepeatedSQL + `,
				jsonb_build_object('source', 'cloud_auth_audit_log', 'id', a.id, 'action', a.action, 'reason', a.reason_code,
					'credential_id', a.metadata->>'credential_id', 'client_ip', a.metadata->>'client_ip', 'user_agent', a.metadata->>'user_agent')
			FROM cloud_auth_audit_log a
			WHERE a.action IN ('sync.auth', 'dashboard.login') AND a.outcome = 'denied'
			  AND a.reason_code IN ('token_revoked', 'principal_disabled') AND a.occurred_at >= $1`,
		summary: "uso de un token revocado o de una persona deshabilitada (%[1]s): %[2]d intentos en %[3]d s (capa %[4]s)",
	},
	{
		// Una PC mal configurada: el mismo token inválido desde la misma IP.
		name: RuleInvalidTokenRepeated,
		source: `SELECT a.occurred_at,
				'fp:' || (a.metadata->>'credential_fp') || '|ip:' || COALESCE(a.metadata->>'client_ip', '?'),
				'plataforma', ` + weightRepeatedSQL + `,
				jsonb_build_object('source', 'cloud_auth_audit_log', 'id', a.id, 'action', a.action, 'reason', a.reason_code,
					'client_ip', a.metadata->>'client_ip', 'user_agent', a.metadata->>'user_agent', 'user_agents', a.metadata->'nexo_user_agents')
			FROM cloud_auth_audit_log a
			WHERE a.action IN ('sync.auth', 'dashboard.login') AND a.outcome = 'denied'
			  AND a.reason_code IN ('unknown_token', 'malformed_bearer', 'invalid_token') AND a.metadata ? 'credential_fp' AND a.occurred_at >= $1`,
		summary: "token inválido repetido (%[1]s): %[2]d intentos en %[3]d s; probable PC mal configurada (capa %[4]s)",
	},
}

// runBurst evalúa una regla de ráfaga e inserta las alertas nuevas.
func runBurst(ctx context.Context, tx *sql.Tx, br burstRule, cfg RuleConfig, now time.Time) ([]Alert, error) {
	since := now.Add(-(cfg.Window + alertDefaultLookbackExtra))
	q := `
		WITH ev(occurred_at, subject, layer, weight, extra) AS (` + br.source + `),
		w AS (
			SELECT subject, layer, occurred_at, extra,
				sum(weight) OVER (PARTITION BY subject, layer ORDER BY occurred_at
					RANGE BETWEEN make_interval(secs => $2::bigint) PRECEDING AND CURRENT ROW) AS n
			FROM ev),
		hit AS (
			SELECT subject, layer, max(n) AS n,
				min(occurred_at) FILTER (WHERE n >= $3) AS first_at, max(occurred_at) AS last_at,
				(array_agg(extra ORDER BY occurred_at DESC))[1] AS sample
			FROM w GROUP BY subject, layer HAVING max(n) >= $3)
		SELECT subject, layer, n, first_at, last_at, sample FROM hit ORDER BY first_at`
	rows, err := tx.QueryContext(ctx, q, since, int64(cfg.Window.Seconds()), cfg.Threshold)
	if err != nil {
		return nil, fmt.Errorf("evidence: regla %s: %w", br.name, err)
	}
	type hit struct {
		subject, layer string
		n              int64
		first, last    time.Time
		sample         []byte
	}
	var hits []hit
	for rows.Next() {
		var h hit
		if err := rows.Scan(&h.subject, &h.layer, &h.n, &h.first, &h.last, &h.sample); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("evidence: regla %s: %w", br.name, err)
		}
		hits = append(hits, h)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	var out []Alert
	for _, h := range hits {
		var sample map[string]any
		_ = json.Unmarshal(h.sample, &sample)
		a, err := InsertAlert(ctx, tx, NewAlert{
			Rule:     br.name,
			Severity: cfg.Severity,
			Layer:    Layer(h.layer),
			Subject:  h.subject,
			DedupKey: fmt.Sprintf("%s|%s|%s|%s", br.name, h.layer, h.subject, h.first.UTC().Format("20060102T150405.000000")),
			Summary:  fmt.Sprintf(br.summary, h.subject, h.n, int64(cfg.Window.Seconds()), h.layer),
			Details: map[string]any{
				"count": h.n, "threshold": cfg.Threshold, "window_seconds": int64(cfg.Window.Seconds()),
				"first_at": h.first.UTC().Format(time.RFC3339Nano), "last_at": h.last.UTC().Format(time.RFC3339Nano), "sample": sample,
			},
			Cooldown: cfg.Cooldown,
		})
		if err != nil {
			return nil, err
		}
		if a != nil {
			out = append(out, *a)
		}
	}
	return out, nil
}

// runRoleChanges: una alerta por cada cambio de rol de plataforma.
func runRoleChanges(ctx context.Context, tx *sql.Tx, cfg RuleConfig, now time.Time) ([]Alert, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT e.id, e.occurred_at, e.action, COALESCE(e.target_principal_id::text, ''), COALESCE(e.role_before, ''),
			COALESCE(e.role_after, ''), e.actor_label, COALESCE(e.client_ip, '')
		FROM nexo_authz_events e
		WHERE e.scope_type = 'platform' AND e.action IN ('binding.granted', 'binding.changed', 'binding.revoked')
		  AND e.occurred_at >= $1
		ORDER BY e.id`, now.Add(-(cfg.Window + alertDefaultLookbackExtra)))
	if err != nil {
		return nil, fmt.Errorf("evidence: regla %s: %w", RulePlatformRoleChanged, err)
	}
	type ev struct {
		id                                       int64
		at                                       time.Time
		action, target, before, after, actor, ip string
	}
	var evs []ev
	for rows.Next() {
		var e ev
		if err := rows.Scan(&e.id, &e.at, &e.action, &e.target, &e.before, &e.after, &e.actor, &e.ip); err != nil {
			_ = rows.Close()
			return nil, err
		}
		evs = append(evs, e)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	var out []Alert
	for _, e := range evs {
		before, after := e.before, e.after
		if before == "" {
			before = "ninguno"
		}
		if after == "" {
			after = "ninguno"
		}
		a, err := InsertAlert(ctx, tx, NewAlert{
			Rule:     RulePlatformRoleChanged,
			Severity: cfg.Severity,
			Layer:    LayerPlatform,
			Subject:  "principal:" + e.target,
			DedupKey: fmt.Sprintf("%s|event:%d", RulePlatformRoleChanged, e.id),
			Summary:  fmt.Sprintf("cambio de rol de plataforma de principal:%s: %s -> %s (por %s)", e.target, before, after, e.actor),
			Details: map[string]any{"event_id": e.id, "action": e.action, "role_before": e.before, "role_after": e.after,
				"actor": e.actor, "client_ip": e.ip, "at": e.at.UTC().Format(time.RFC3339Nano)},
		})
		if err != nil {
			return nil, err
		}
		if a != nil {
			out = append(out, *a)
		}
	}
	return out, nil
}

// runLoggingStalled: el registro de actividad dejó de escribir.
func runLoggingStalled(ctx context.Context, tx *sql.Tx, cfg RuleConfig, now time.Time) ([]Alert, error) {
	var last sql.NullTime
	if err := tx.QueryRowContext(ctx, `SELECT max(occurred_at) FROM nexo_activity_log`).Scan(&last); err != nil {
		return nil, fmt.Errorf("evidence: regla %s: %w", RuleLoggingStalled, err)
	}
	if last.Valid && now.Sub(last.Time) < cfg.Window {
		return nil, nil
	}
	lastTxt := "nunca"
	if last.Valid {
		lastTxt = last.Time.UTC().Format(time.RFC3339)
	}
	a, err := InsertAlert(ctx, tx, NewAlert{
		Rule:     RuleLoggingStalled,
		Severity: cfg.Severity,
		Layer:    LayerProjects,
		Subject:  "nexo_activity_log",
		Summary:  fmt.Sprintf("sin actividad registrada desde %s (umbral %d min)", lastTxt, int(cfg.Window.Minutes())),
		Details:  map[string]any{"last_activity_at": lastTxt, "window_seconds": int64(cfg.Window.Seconds())},
		Cooldown: cfg.Cooldown,
	})
	if err != nil || a == nil {
		return nil, err
	}
	return []Alert{*a}, nil
}

// EvaluateOnce corre todas las reglas activas en una transacción con el
// candado del evaluador. ran = false si otra réplica tiene el candado.
func EvaluateOnce(ctx context.Context, db *sql.DB, rules RuleSet, now time.Time) (alerts []Alert, ran bool, err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, fmt.Errorf("evidence: evaluador: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var locked bool
	if err := tx.QueryRowContext(ctx, alertEvaluatorLockSQL).Scan(&locked); err != nil {
		return nil, false, fmt.Errorf("evidence: evaluador: %w", err)
	}
	if !locked {
		return nil, false, nil
	}
	for _, br := range burstRules {
		cfg := rules[br.name]
		if !cfg.Enabled {
			continue
		}
		got, err := runBurst(ctx, tx, br, cfg, now)
		if err != nil {
			return nil, true, err
		}
		alerts = append(alerts, got...)
	}
	if cfg := rules[RulePlatformRoleChanged]; cfg.Enabled {
		got, err := runRoleChanges(ctx, tx, cfg, now)
		if err != nil {
			return nil, true, err
		}
		alerts = append(alerts, got...)
	}
	if cfg := rules[RulePlatformRoleDrift]; cfg.Enabled { // NX-05
		got, err := runRoleDrift(ctx, tx, cfg, now)
		if err != nil {
			return nil, true, err
		}
		alerts = append(alerts, got...)
	}
	if cfg := rules[RuleSingleSuperadmin]; cfg.Enabled { // SG-06
		got, err := runSingleSuperadmin(ctx, tx, cfg, now)
		if err != nil {
			return nil, true, err
		}
		alerts = append(alerts, got...)
	}
	if cfg := rules[RulePrivilegedTokenExpiring]; cfg.Enabled { // SG-06
		got, err := runPrivilegedTokenExpiring(ctx, tx, cfg, now)
		if err != nil {
			return nil, true, err
		}
		alerts = append(alerts, got...)
	}
	if cfg := rules[RuleLoggingStalled]; cfg.Enabled {
		got, err := runLoggingStalled(ctx, tx, cfg, now)
		if err != nil {
			return nil, true, err
		}
		alerts = append(alerts, got...)
	}
	if got, err := runKBRules(ctx, tx, rules); err != nil { // NX-10 2b (KP-09)
		return nil, true, err
	} else {
		alerts = append(alerts, got...)
	}
	if err := tx.Commit(); err != nil {
		return nil, true, fmt.Errorf("evidence: evaluador: %w", err)
	}
	sort.Slice(alerts, func(i, j int) bool { return alerts[i].ID < alerts[j].ID })
	return alerts, true, nil
}

// ChainAlerts convierte un informe de verificación en alertas.
func ChainAlerts(ctx context.Context, db *sql.DB, rep Report, cfg RuleConfig) ([]Alert, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	var out []Alert
	for _, t := range rep.Tables {
		if t.OK {
			continue
		}
		detail := map[string]any{"table": t.Table, "altered_rows": t.Altered, "missing_rows": t.MissingRows,
			"problems": t.Problems, "checkpoint_seq": t.CheckpointSeq, "last_seq": t.LastSeq}
		what := fmt.Sprintf("%d filas alteradas, %d faltantes", t.Altered, t.MissingRows)
		key := fmt.Sprintf("%s|%s|%d|%d|%d", RuleAuditChainBroken, t.Table, t.Altered, t.MissingRows, len(t.Problems))
		if t.FirstBroken != nil {
			detail["first_broken"] = t.FirstBroken
			what += fmt.Sprintf("; primera fila alterada id=%d (posición %d, %s)", t.FirstBroken.ID, t.FirstBroken.Seq, t.FirstBroken.Kind)
			key += fmt.Sprintf("|%d", t.FirstBroken.Seq)
		}
		if len(t.Gaps) > 0 {
			detail["first_gap"] = t.Gaps[0]
		}
		a, err := InsertAlert(ctx, db, NewAlert{
			Rule: RuleAuditChainBroken, Severity: cfg.Severity, Layer: t.Layer, Subject: "table:" + t.Table,
			DedupKey: key, Summary: "verificación de la cadena de " + t.Table + " fallida: " + what, Details: detail, Cooldown: cfg.Cooldown,
		})
		if err != nil {
			return out, err
		}
		if a != nil {
			out = append(out, *a)
		}
	}
	return out, nil
}

// ─── Consulta y reconocimiento ───────────────────────────────────────────────

// AlertQuery filtra la lista de alertas (paginación por id descendente).
type AlertQuery struct {
	Rule     string
	Layer    string
	Severity string
	Unacked  bool
	From, To time.Time
	Cursor   string
	Limit    int
}

// ErrInvalidCursor es un cursor ilegible.
var ErrInvalidCursor = errors.New("invalid cursor")

// ListAlerts lista alertas con su reconocimiento.
func ListAlerts(ctx context.Context, db *sql.DB, q AlertQuery) ([]Alert, string, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 200 {
		limit = 200
	}
	var before int64
	if q.Cursor != "" {
		n, err := strconv.ParseInt(q.Cursor, 10, 64)
		if err != nil || n <= 0 {
			return nil, "", ErrInvalidCursor
		}
		before = n
	}
	rows, err := db.QueryContext(ctx, `
		SELECT a.id, a.occurred_at, a.rule, a.severity, a.layer, a.subject, a.summary, a.details,
			k.occurred_at, COALESCE(k.acknowledged_by_principal_id::text, ''), COALESCE(k.acknowledged_by_label, ''), COALESCE(k.note, '')
		FROM nexo_alerts a LEFT JOIN nexo_alert_acks k ON k.alert_id = a.id
		WHERE ($1 = '' OR a.rule = $1) AND ($2 = '' OR a.layer = $2) AND ($3 = '' OR a.severity = $3)
		  AND (NOT $4 OR k.id IS NULL)
		  AND ($5::timestamptz IS NULL OR a.occurred_at >= $5) AND ($6::timestamptz IS NULL OR a.occurred_at < $6)
		  AND ($7::bigint = 0 OR a.id < $7)
		ORDER BY a.id DESC LIMIT $8`,
		q.Rule, q.Layer, q.Severity, q.Unacked, nullTime(q.From), nullTime(q.To), before, limit+1)
	if err != nil {
		return nil, "", fmt.Errorf("evidence: alertas: %w", err)
	}
	defer rows.Close()
	var out []Alert
	for rows.Next() {
		var a Alert
		var det []byte
		var ackAt sql.NullTime
		var ack AlertAck
		if err := rows.Scan(&a.ID, &a.OccurredAt, &a.Rule, &a.Severity, &a.Layer, &a.Subject, &a.Summary, &det,
			&ackAt, &ack.PrincipalID, &ack.Label, &ack.Note); err != nil {
			return nil, "", fmt.Errorf("evidence: alertas: %w", err)
		}
		if len(det) > 0 {
			_ = json.Unmarshal(det, &a.Details)
		}
		// NX-07: alertas históricas escritas antes de la redacción en escritura.
		a = nexoRedactAlert(a) // NX-07
		redact.Struct(&ack)
		if ackAt.Valid {
			ack.OccurredAt = ackAt.Time
			a.Ack = &ack
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = strconv.FormatInt(out[len(out)-1].ID, 10)
	}
	return out, next, nil
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// AckActor es quién reconoce una alerta.
type AckActor struct {
	PrincipalID string
	TokenID     string
	Label       string
	ClientIP    string
	UserAgent   string
}

// Errores del reconocimiento.
var (
	ErrAlertNotFound = errors.New("alert not found")
	ErrAlertAcked    = errors.New("alert already acknowledged")
)

// AckAlert registra el reconocimiento (una vez por alerta).
func AckAlert(ctx context.Context, db *sql.DB, alertID int64, actor AckActor, note string) (AlertAck, error) {
	note = security.NexoRedactExternal(note, 1000) // NX-07: credenciales fuera antes de recortar y guardar
	actor.Label = security.NexoRedactExternal(actor.Label, 0)
	actor.UserAgent = security.NexoRedactExternal(actor.UserAgent, 0)
	var ack AlertAck
	err := db.QueryRowContext(ctx, `
		INSERT INTO nexo_alert_acks (alert_id, acknowledged_by_principal_id, acknowledged_by_token_id, acknowledged_by_label, note, client_ip, user_agent)
		SELECT a.id, NULLIF($2, '')::bigint, NULLIF($3, '')::bigint, $4, NULLIF($5, ''), NULLIF($6, ''), NULLIF($7, '')
		FROM nexo_alerts a WHERE a.id = $1
		ON CONFLICT (alert_id) DO NOTHING
		RETURNING occurred_at, COALESCE(acknowledged_by_principal_id::text, ''), acknowledged_by_label, COALESCE(note, '')`,
		alertID, actor.PrincipalID, actor.TokenID, labelOr(actor.Label), note, actor.ClientIP, actor.UserAgent).
		Scan(&ack.OccurredAt, &ack.PrincipalID, &ack.Label, &ack.Note)
	if errors.Is(err, sql.ErrNoRows) {
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM nexo_alerts WHERE id = $1)`, alertID).Scan(&exists); err != nil {
			return ack, err
		}
		if !exists {
			return ack, ErrAlertNotFound
		}
		return ack, ErrAlertAcked
	}
	if err != nil {
		return ack, fmt.Errorf("evidence: reconocer alerta: %w", err)
	}
	return ack, nil
}

func labelOr(s string) string {
	if strings.TrimSpace(s) == "" {
		return "desconocido"
	}
	return strings.TrimSpace(s)
}

// RulesFromEnv lee NEXO_ALERT_RULES (inválido: valores por defecto, error).
func RulesFromEnv() (RuleSet, error) { return ParseRules(os.Getenv(AlertRulesEnv)) }
