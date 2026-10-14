package evidence

// Nexo IA · NX-05 (unificación de roles de plataforma) — desalineación entre
// el rol de Nexo y la columna role de Engram.
//
// El rol de plataforma de Nexo (miembro, auditor, admin, superadmin) es la
// única fuente; la columna role de cloud_principals (admin | member) se deriva
// (authz.EngramRoleFor) y la mantienen triggers de la base. Si alguien los
// desactiva, restaura una copia vieja o escribe con una cuenta que los evita,
// la columna puede quedar distinta. Eso se informa en tres lugares:
//
//   - la alerta platform_role_drift (una por persona y por estado: la clave
//     lleva los dos valores, así no se repite cada minuto);
//   - la marca role_drift y el total del resumen en la revisión de accesos
//     (review.go);
//   - `engram nexo authz reconcile-roles` (authz.Service.ReconcilePlatformRoles),
//     que la lista y, con --apply, la corrige: el rol de Nexo manda.
//
// La regla solo detecta: nunca corrige sola ni concede nada (la columna no da
// permisos, el motor solo mira la asignación de Nexo).

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
)

// RulePlatformRoleDrift es la regla de nexo_alerts.
const RulePlatformRoleDrift = "platform_role_drift"

// maxDriftAlertsPerPass acota las alertas nuevas por pasada (una instalación
// con la base restaurada de una copia vieja tendría miles de filas).
const maxDriftAlertsPerPass = 200

// expectedEngramRole es la columna de Engram que corresponde a una persona o
// cuenta según su rol de plataforma de Nexo.
func expectedEngramRole(kind, platformRole string) string {
	if kind != authz.KindHuman {
		return "member"
	}
	return authz.EngramRoleFor(authz.Role(platformRole))
}

// runRoleDrift inserta una alerta por cada desalineación nueva.
func runRoleDrift(ctx context.Context, tx *sql.Tx, cfg RuleConfig, now time.Time) ([]Alert, error) {
	rows, err := tx.QueryContext(ctx, authz.PlatformRoleDriftSQL)
	if err != nil {
		return nil, fmt.Errorf("evidence: regla %s: %w", RulePlatformRoleDrift, err)
	}
	var drift []authz.RoleDrift
	for rows.Next() {
		var d authz.RoleDrift
		var role string
		if err := rows.Scan(&d.PrincipalID, &d.Username, &d.DisplayName, &d.Kind, &d.Enabled, &role, &d.EngramRole, &d.Expected); err != nil {
			_ = rows.Close()
			return nil, err
		}
		d.NexoRole = authz.Role(role)
		drift = append(drift, d)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	var out []Alert
	for _, d := range drift {
		if len(out) >= maxDriftAlertsPerPass {
			break
		}
		a, err := InsertAlert(ctx, tx, NewAlert{
			Rule:     RulePlatformRoleDrift,
			Severity: cfg.Severity,
			Layer:    LayerPlatform,
			Subject:  "principal:" + d.PrincipalID,
			DedupKey: fmt.Sprintf("%s|%s|%s>%s", RulePlatformRoleDrift, d.PrincipalID, d.EngramRole, d.Expected),
			Summary: fmt.Sprintf("el rol de Engram de principal:%s es %q y por su rol de Nexo (%s) debería ser %q; el rol de Nexo manda. Corrija con `engram nexo authz reconcile-roles --apply`",
				d.PrincipalID, d.EngramRole, authz.PlatformRoleLabel(d.NexoRole), d.Expected),
			Details: map[string]any{"principal_id": d.PrincipalID, "username": d.Username, "kind": d.Kind, "nexo_role": string(d.NexoRole),
				"engram_role": d.EngramRole, "expected_engram_role": d.Expected, "at": now.UTC().Format(time.RFC3339Nano)},
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
