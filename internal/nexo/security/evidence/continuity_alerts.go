package evidence

// Nexo IA · SG-06 — continuidad del acceso de administración.
//
// Tres alertas que cuidan que la plataforma no se quede sin quien la
// administre:
//
//   - single_superadmin (alta): queda un solo superadmin habilitado. Si esa
//     persona pierde su token (vence o se revoca) o la cuenta, nadie puede
//     gestionar admins ni la configuración. Se repite una vez por semana
//     (cooldown) mientras siga así, y de inmediato si cambia quién es el único.
//   - privileged_token_expiring (advertencia a 30 días, alta a 7): un token
//     activo de un superadmin, admin o auditor habilitado vence pronto. Una
//     alerta por token y por aviso (30 y 7 días): la clave lleva el id del
//     token y el aviso, así no se repite cada minuto. Un token revocado o
//     vencido deja de avisar. La renovación sin corte está en el manual
//     (servidor-produccion.md, 7.4).
//   - emergency_access_used (alta): se emitió un token de emergencia con
//     `engram cloud bootstrap emergency-token` (cloudstore/nexo_emergency.go la
//     inserta en la misma transacción que el token).
//
// Las dos primeras las evalúa el evaluador de alertas (EvaluateOnce, una pasada
// por minuto en una sola réplica); no corrigen nada, solo avisan.

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Reglas de continuidad.
const (
	RuleSingleSuperadmin        = "single_superadmin"
	RulePrivilegedTokenExpiring = "privileged_token_expiring"
	RuleEmergencyAccessUsed     = "emergency_access_used"
)

// Avisos de vencimiento de un token privilegiado (días antes).
const (
	PrivilegedTokenWarnDays   = 30
	PrivilegedTokenUrgentDays = 7
)

// PrivilegedRoles son los roles de plataforma cuyos tokens importan para la
// continuidad: quien administra, audita o puede reasignar el acceso.
var PrivilegedRoles = []string{"superadmin", "admin", "auditor"}

// IsPrivilegedRole dice si un rol de plataforma es de los vigilados.
func IsPrivilegedRole(role string) bool {
	for _, r := range PrivilegedRoles {
		if r == role {
			return true
		}
	}
	return false
}

func severityRank(s string) int {
	switch s {
	case SeverityInfo:
		return 0
	case SeverityWarning:
		return 1
	case SeverityHigh:
		return 2
	case SeverityCritical:
		return 3
	}
	return 0
}

// runSingleSuperadmin avisa si hay uno o ningún superadmin habilitado.
func runSingleSuperadmin(ctx context.Context, tx *sql.Tx, cfg RuleConfig, now time.Time) ([]Alert, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT p.id::text, COALESCE(u.username, p.display_name, '')
		FROM nexo_role_bindings b
		JOIN cloud_principals p ON p.id = b.principal_id
		LEFT JOIN cloud_human_users u ON u.principal_id = p.id
		WHERE b.scope_type = 'platform' AND b.scope_key = '*' AND b.role = 'superadmin' AND p.enabled
		ORDER BY p.id`)
	if err != nil {
		return nil, fmt.Errorf("evidence: regla %s: %w", RuleSingleSuperadmin, err)
	}
	type sa struct{ id, name string }
	var admins []sa
	for rows.Next() {
		var s sa
		if err := rows.Scan(&s.id, &s.name); err != nil {
			_ = rows.Close()
			return nil, err
		}
		admins = append(admins, s)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(admins) > 1 {
		return nil, nil
	}
	if len(admins) == 0 {
		// Servidor recien instalado, antes de `engram cloud bootstrap admin`: todavia
		// no hay ninguna persona, y avisar de que falta un superadmin seria ruido.
		var anyone bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM cloud_human_users)`).Scan(&anyone); err != nil {
			return nil, fmt.Errorf("evidence: regla %s: %w", RuleSingleSuperadmin, err)
		}
		if !anyone {
			return nil, nil
		}
	}
	day := now.UTC().Format(time.DateOnly)
	a := NewAlert{Rule: RuleSingleSuperadmin, Severity: cfg.Severity, Layer: LayerPlatform, Cooldown: cfg.Cooldown}
	if len(admins) == 0 {
		a.Subject = "platform"
		a.DedupKey = fmt.Sprintf("%s|none|%s", RuleSingleSuperadmin, day)
		a.Summary = "no hay ningún superadmin habilitado: nadie puede gestionar admins ni la configuración de la plataforma; recupere el acceso con `engram cloud bootstrap emergency-token` solo si existe un superadmin habilitado, o con la base de datos"
		a.Details = map[string]any{"superadmins_enabled": 0}
	} else {
		only := admins[0]
		a.Subject = "principal:" + only.id
		a.DedupKey = fmt.Sprintf("%s|%s|%s", RuleSingleSuperadmin, only.id, day)
		a.Summary = fmt.Sprintf("queda un solo superadmin habilitado (%s, principal:%s): si pierde su token o su cuenta nadie podrá administrar la plataforma; cree un segundo superadmin (manual de producción, 7.3)",
			only.name, only.id)
		a.Details = map[string]any{"superadmins_enabled": 1, "principal_id": only.id, "username": only.name}
	}
	ins, err := InsertAlert(ctx, tx, a)
	if err != nil || ins == nil {
		return nil, err
	}
	return []Alert{*ins}, nil
}

// PrivilegedTokenExpiry es un token activo de una persona con rol de
// plataforma vigilado que vence dentro del plazo de aviso.
type PrivilegedTokenExpiry struct {
	TokenID     string
	TokenName   string
	PrincipalID string
	Username    string
	Role        string
	Scope       string
	ExpiresAt   time.Time
}

// DaysLeft son los días que faltan (redondeados hacia arriba).
func (p PrivilegedTokenExpiry) DaysLeft(now time.Time) int {
	d := p.ExpiresAt.Sub(now)
	if d <= 0 {
		return 0
	}
	return int((d + 24*time.Hour - 1) / (24 * time.Hour))
}

// Stage es el aviso que corresponde: 7 si vence en 7 días o menos, 30 si no.
func (p PrivilegedTokenExpiry) Stage(now time.Time) int {
	if p.ExpiresAt.Sub(now) <= PrivilegedTokenUrgentDays*24*time.Hour {
		return PrivilegedTokenUrgentDays
	}
	return PrivilegedTokenWarnDays
}

type rowsQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// FindExpiringPrivilegedTokens lista los tokens activos (no revocados, no
// vencidos) de superadmins, admins y auditores habilitados que vencen en los
// próximos PrivilegedTokenWarnDays días. Los más próximos primero.
func FindExpiringPrivilegedTokens(ctx context.Context, q rowsQuerier, now time.Time) ([]PrivilegedTokenExpiry, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT t.id::text, t.name, p.id::text, COALESCE(u.username, p.display_name, ''), b.role, t.nexo_scope, t.nexo_expires_at
		FROM cloud_principal_tokens t
		JOIN cloud_principals p ON p.id = t.principal_id AND p.enabled
		JOIN nexo_role_bindings b ON b.principal_id = p.id AND b.scope_type = 'platform' AND b.scope_key = '*'
			AND b.role IN ('superadmin', 'admin', 'auditor')
		LEFT JOIN cloud_human_users u ON u.principal_id = p.id
		WHERE t.revoked_at IS NULL AND t.nexo_expires_at IS NOT NULL
		  AND t.nexo_expires_at > $1 AND t.nexo_expires_at <= $2
		ORDER BY t.nexo_expires_at, t.id`,
		now, now.Add(PrivilegedTokenWarnDays*24*time.Hour))
	if err != nil {
		return nil, fmt.Errorf("evidence: tokens privilegiados por vencer: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []PrivilegedTokenExpiry
	for rows.Next() {
		var e PrivilegedTokenExpiry
		if err := rows.Scan(&e.TokenID, &e.TokenName, &e.PrincipalID, &e.Username, &e.Role, &e.Scope, &e.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// runPrivilegedTokenExpiring inserta un aviso por token y etapa (30 o 7 días).
func runPrivilegedTokenExpiring(ctx context.Context, tx *sql.Tx, cfg RuleConfig, now time.Time) ([]Alert, error) {
	exp, err := FindExpiringPrivilegedTokens(ctx, tx, now)
	if err != nil {
		return nil, err
	}
	var out []Alert
	for _, e := range exp {
		stage := e.Stage(now)
		sev := cfg.Severity
		if stage == PrivilegedTokenUrgentDays && severityRank(sev) < severityRank(SeverityHigh) {
			sev = SeverityHigh // a 7 días el aviso sube a alta
		}
		who := e.Username
		if who == "" {
			who = "principal:" + e.PrincipalID
		}
		a, err := InsertAlert(ctx, tx, NewAlert{
			Rule: RulePrivilegedTokenExpiring, Severity: sev, Layer: LayerPlatform, Subject: "token:" + e.TokenID,
			DedupKey: fmt.Sprintf("%s|%s|%d", RulePrivilegedTokenExpiring, e.TokenID, stage),
			Summary: fmt.Sprintf("el token %q de %s (%s) vence en %d días (%s): emita uno nuevo, pruébelo y revoque el viejo antes de esa fecha (manual de producción, 7.4)",
				e.TokenName, who, e.Role, e.DaysLeft(now), e.ExpiresAt.UTC().Format(time.DateOnly)),
			Details: map[string]any{"token_id": e.TokenID, "token_name": e.TokenName, "principal_id": e.PrincipalID, "username": e.Username,
				"platform_role": e.Role, "scope": e.Scope, "expires_at": e.ExpiresAt.UTC().Format(time.RFC3339), "days_left": e.DaysLeft(now), "notice_days": stage},
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
