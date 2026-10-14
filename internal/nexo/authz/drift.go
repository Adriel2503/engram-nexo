package authz

// Desalineación entre el rol de Nexo y la columna role de Engram, y su
// corrección (docs/nexo/roles/design.md §11.5).
//
// Con los triggers de migrate_roles.go instalados no debería existir ninguna
// (la base no deja la columna distinta del rol). Puede aparecer si alguien
// desactiva los triggers o restaura una copia vieja; entonces:
//   - la alerta platform_role_drift y la marca "role_drift" de la revisión de
//     accesos la muestran (internal/nexo/security/evidence);
//   - `engram nexo authz reconcile-roles` la lista, y con --apply la corrige:
//     el rol de Nexo manda, la columna se recalcula y cada cambio queda como
//     evento engram_role.synced.

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// RoleDrift es una persona o cuenta cuya columna de Engram no coincide con la
// que le corresponde por su rol de Nexo.
type RoleDrift struct {
	PrincipalID string `json:"principal_id"`
	Username    string `json:"username,omitempty"`
	DisplayName string `json:"display_name"`
	Kind        string `json:"kind"`
	Enabled     bool   `json:"enabled"`
	NexoRole    Role   `json:"nexo_role"`   // rol de plataforma de Nexo (manda)
	EngramRole  string `json:"engram_role"` // columna actual
	Expected    string `json:"expected"`    // columna que le corresponde
}

// PlatformRoleDriftSQL lista las desalineaciones. La reutiliza la revisión de
// accesos y la alerta para no repetir la regla de derivación.
const PlatformRoleDriftSQL = `
	SELECT p.id::text, COALESCE(h.username, sa.slug, ''), p.display_name, p.kind, p.enabled,
	       CASE WHEN p.kind = 'human' AND b.role IS NOT NULL THEN b.role ELSE 'miembro' END AS nexo_role,
	       p.role,
	       CASE WHEN p.kind = 'human' AND b.role IN ('superadmin', 'admin') THEN 'admin' ELSE 'member' END AS expected
	FROM cloud_principals p
	LEFT JOIN cloud_human_users h ON h.principal_id = p.id
	LEFT JOIN nexo_service_accounts sa ON sa.principal_id = p.id
	LEFT JOIN nexo_role_bindings b ON b.principal_id = p.id AND b.scope_type = 'platform' AND b.scope_key = '*'
	WHERE p.role IS DISTINCT FROM CASE WHEN p.kind = 'human' AND b.role IN ('superadmin', 'admin') THEN 'admin' ELSE 'member' END
	ORDER BY p.id`

type rowsQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func listDriftTx(ctx context.Context, q rowsQueryer) ([]RoleDrift, error) {
	rows, err := q.QueryContext(ctx, PlatformRoleDriftSQL)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []RoleDrift{}
	for rows.Next() {
		var d RoleDrift
		var role string
		if err := rows.Scan(&d.PrincipalID, &d.Username, &d.DisplayName, &d.Kind, &d.Enabled, &role, &d.EngramRole, &d.Expected); err != nil {
			return nil, err
		}
		d.NexoRole = Role(role)
		out = append(out, d)
	}
	return out, rows.Err()
}

// PlatformRoleDrift lista las desalineaciones actuales.
func (s *Service) PlatformRoleDrift(ctx context.Context) ([]RoleDrift, error) {
	return listDriftTx(ctx, s.db)
}

// ReconcileResult es el resultado de ReconcilePlatformRoles.
type ReconcileResult struct {
	Applied bool        `json:"applied"`
	Drift   []RoleDrift `json:"drift"`
	Fixed   int         `json:"fixed"`
}

// ReconcilePlatformRoles detecta las desalineaciones y, con apply, las
// corrige: la columna de Engram se recalcula desde el rol de Nexo, nunca al
// revés. Cada corrección deja un evento engram_role.synced con el actor
// (vacío = sistema, por ejemplo la CLI). Sin apply no cambia nada. Es
// idempotente.
func (s *Service) ReconcilePlatformRoles(ctx context.Context, actor Identity, apply bool, via string) (ReconcileResult, error) {
	res := ReconcileResult{Applied: apply}
	if !apply {
		drift, err := s.PlatformRoleDrift(ctx)
		res.Drift = drift
		return res, err
	}
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, guardLockSQL); err != nil {
			return err
		}
		drift, err := listDriftTx(ctx, tx)
		if err != nil {
			return err
		}
		res.Drift = drift
		for _, d := range drift {
			// Se vuelve a leer la fila bloqueada: si otra transacción ya la corrigió, no se toca.
			var current string
			if err := tx.QueryRowContext(ctx, `SELECT role FROM cloud_principals WHERE id = $1 FOR UPDATE`, d.PrincipalID).Scan(&current); err != nil {
				return err
			}
			if current == d.Expected {
				continue
			}
			if _, err := tx.ExecContext(ctx, `UPDATE cloud_principals SET role = $2, updated_at = NOW() WHERE id = $1`, d.PrincipalID, d.Expected); err != nil {
				return mapDBError(err)
			}
			details := map[string]any{"reason": "reconcile", "engram_role_before": current, "engram_role_after": d.Expected}
			if strings.TrimSpace(via) != "" {
				details["via"] = via
			}
			if err := insertEvent(ctx, tx, Event{
				Action: ActionEngramRoleSynced, Scope: PlatformScope(), TargetPrincipalID: d.PrincipalID,
				RoleBefore: d.NexoRole, RoleAfter: d.NexoRole, Actor: actor, Details: details,
			}); err != nil {
				return err
			}
			res.Fixed++
		}
		return nil
	})
	if err != nil {
		return ReconcileResult{}, fmt.Errorf("authz: reconcile platform roles: %w", err)
	}
	return res, nil
}
