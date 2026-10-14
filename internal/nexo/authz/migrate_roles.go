package authz

// Migración de la unificación de roles de plataforma (design §11). Se ejecuta
// al final de Migrate, idempotente.
//
//  1. Funciones y triggers que derivan la columna role de Engram del rol de
//     Nexo (ver platformroles.go). Se crean siempre (CREATE OR REPLACE).
//  2. Corrección ÚNICA para instalaciones existentes, con marca en
//     nexo_platform_role_unification: antes de la unificación, "admin" de
//     Engram valía admin y el primer admin valía superadmin sin fila. La
//     corrección materializa esos roles como asignaciones (con evento
//     binding.granted, actor "sistema") y alinea la columna de todo el
//     mundo (evento engram_role.synced). Después de la marca, el arranque no
//     vuelve a corregir nada: una desalineación nueva solo se informa
//     (alerta platform_role_drift) y se corrige con
//     `engram nexo authz reconcile-roles --apply`.

import (
	"context"
	"database/sql"
	"fmt"
)

// PlatformRoleUnificationVersion identifica la corrección única.
const PlatformRoleUnificationVersion = 1

var platformRoleSchemaQueries = []string{
	// Rol de Engram que le corresponde a una persona según su asignación de
	// plataforma. Una cuenta de servicio nunca es admin (R-10).
	`CREATE OR REPLACE FUNCTION nexo_platform_engram_role(p_id BIGINT) RETURNS TEXT AS $$
		SELECT CASE WHEN EXISTS (
			SELECT 1 FROM nexo_role_bindings b JOIN cloud_principals p ON p.id = b.principal_id
			WHERE b.principal_id = p_id AND p.kind = 'human'
			  AND b.scope_type = 'platform' AND b.scope_key = '*' AND b.role IN ('superadmin', 'admin'))
		THEN 'admin' ELSE 'member' END
	$$ LANGUAGE sql STABLE`,

	// Cualquier INSERT o UPDATE de la columna queda con el valor derivado.
	`CREATE OR REPLACE FUNCTION nexo_principal_role_derive() RETURNS trigger AS $$
	BEGIN
		NEW.role := nexo_platform_engram_role(NEW.id);
		RETURN NEW;
	END
	$$ LANGUAGE plpgsql`,

	// Cambiar la asignación de plataforma recalcula la columna en la misma
	// transacción.
	`CREATE OR REPLACE FUNCTION nexo_role_bindings_sync_engram() RETURNS trigger AS $$
	DECLARE
		v_pid BIGINT;
		v_scope TEXT;
	BEGIN
		IF TG_OP = 'DELETE' THEN
			v_pid := OLD.principal_id; v_scope := OLD.scope_type;
		ELSE
			v_pid := NEW.principal_id; v_scope := NEW.scope_type;
		END IF;
		IF v_scope = 'platform' THEN
			UPDATE cloud_principals SET role = nexo_platform_engram_role(v_pid), updated_at = NOW()
			WHERE id = v_pid AND role IS DISTINCT FROM nexo_platform_engram_role(v_pid);
		END IF;
		RETURN NULL;
	END
	$$ LANGUAGE plpgsql`,

	`DO $$ BEGIN
		IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'nexo_principal_role_derive' AND tgrelid = 'cloud_principals'::regclass) THEN
			CREATE TRIGGER nexo_principal_role_derive BEFORE INSERT OR UPDATE OF role ON cloud_principals
				FOR EACH ROW EXECUTE FUNCTION nexo_principal_role_derive();
		END IF;
		IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'nexo_role_bindings_sync_engram' AND tgrelid = 'nexo_role_bindings'::regclass) THEN
			CREATE TRIGGER nexo_role_bindings_sync_engram AFTER INSERT OR UPDATE OF role OR DELETE ON nexo_role_bindings
				FOR EACH ROW EXECUTE FUNCTION nexo_role_bindings_sync_engram();
		END IF;
	END $$`,

	`CREATE TABLE IF NOT EXISTS nexo_platform_role_unification (
		id            INTEGER PRIMARY KEY CHECK (id = 1),
		version       INTEGER NOT NULL,
		applied_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		materialized  INTEGER NOT NULL DEFAULT 0,
		synced        INTEGER NOT NULL DEFAULT 0
	)`,
}

// migratePlatformRoles crea las funciones y los triggers y, la primera vez,
// aplica la corrección única.
func migratePlatformRoles(ctx context.Context, tx *sql.Tx) error {
	// Los triggers se crean al final de la corrección: así la corrección
	// controla la columna sin que el trigger la reescriba antes de tiempo.
	for _, q := range platformRoleSchemaQueries[:3] { // funciones
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("authz migrate roles: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, platformRoleSchemaQueries[4]); err != nil { // tabla de la marca
		return fmt.Errorf("authz migrate roles: %w", err)
	}
	var applied bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM nexo_platform_role_unification WHERE id = 1)`).Scan(&applied); err != nil {
		return fmt.Errorf("authz migrate roles: %w", err)
	}
	var materialized, synced int
	if !applied {
		var err error
		if materialized, synced, err = unifyExistingRoles(ctx, tx); err != nil {
			return fmt.Errorf("authz migrate roles: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, platformRoleSchemaQueries[3]); err != nil { // triggers
		return fmt.Errorf("authz migrate roles: %w", err)
	}
	if !applied {
		if _, err := tx.ExecContext(ctx, `INSERT INTO nexo_platform_role_unification (id, version, materialized, synced) VALUES (1, $1, $2, $3) ON CONFLICT DO NOTHING`,
			PlatformRoleUnificationVersion, materialized, synced); err != nil {
			return fmt.Errorf("authz migrate roles: %w", err)
		}
	}
	return nil
}

// unifyExistingRoles es la corrección única (ver el comentario del archivo).
func unifyExistingRoles(ctx context.Context, tx *sql.Tx) (materialized, synced int, err error) {
	if _, err = tx.ExecContext(ctx, guardLockSQL); err != nil {
		return 0, 0, err
	}
	// 1. Personas con role=admin de Engram y sin asignación de plataforma:
	// el primer admin habilitado, si no hay superadmin habilitado, queda
	// superadmin (lo que decía el diseño §6.3); el resto, admin.
	rows, err := tx.QueryContext(ctx, `
		SELECT p.id::text, p.enabled FROM cloud_principals p
		WHERE p.kind = 'human' AND p.role = 'admin'
		  AND NOT EXISTS (SELECT 1 FROM nexo_role_bindings x WHERE x.principal_id = p.id AND x.scope_type = 'platform')
		ORDER BY p.enabled DESC, p.id`)
	if err != nil {
		return 0, 0, err
	}
	type cand struct {
		id      string
		enabled bool
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if err = rows.Scan(&c.id, &c.enabled); err != nil {
			_ = rows.Close()
			return 0, 0, err
		}
		cands = append(cands, c)
	}
	if err = rows.Close(); err != nil {
		return 0, 0, err
	}
	var hasSuper bool
	if err = tx.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM nexo_role_bindings b JOIN cloud_principals p ON p.id = b.principal_id
			WHERE b.scope_type = 'platform' AND b.scope_key = '*' AND b.role = 'superadmin' AND p.enabled)`).Scan(&hasSuper); err != nil {
		return 0, 0, err
	}
	for _, c := range cands {
		role, rule := RoleAdmin, "role=admin de Engram sin asignación: se materializa como admin"
		if c.enabled && !hasSuper {
			role, rule, hasSuper = RoleSuperadmin, "primer admin de Engram sin superadmin habilitado: se materializa como superadmin (diseño §6.3)", true
		}
		if err = upsertBinding(ctx, tx, c.id, PlatformScope(), role, ""); err != nil {
			return 0, 0, err
		}
		if err = insertEvent(ctx, tx, Event{
			Action: ActionBindingGranted, Scope: PlatformScope(), TargetPrincipalID: c.id,
			RoleBefore: RoleAdmin, RoleAfter: role,
			Details: map[string]any{"reason": "unificacion_roles", "rule": rule, "engram_role_before": "admin"},
		}); err != nil {
			return 0, 0, err
		}
		materialized++
	}
	// 2. La columna de todo el mundo queda derivada del rol de Nexo
	// (cuentas de servicio con role=admin incluidas, que no pueden ser admin).
	drift, err := listDriftTx(ctx, tx)
	if err != nil {
		return 0, 0, err
	}
	for _, d := range drift {
		if _, err = tx.ExecContext(ctx, `UPDATE cloud_principals SET role = $2, updated_at = NOW() WHERE id = $1`, d.PrincipalID, d.Expected); err != nil {
			return 0, 0, mapDBError(err)
		}
		if err = insertEvent(ctx, tx, Event{
			Action: ActionEngramRoleSynced, Scope: PlatformScope(), TargetPrincipalID: d.PrincipalID,
			RoleBefore: d.NexoRole, RoleAfter: d.NexoRole,
			Details: map[string]any{"reason": "unificacion_roles", "engram_role_before": d.EngramRole, "engram_role_after": d.Expected},
		}); err != nil {
			return 0, 0, err
		}
		synced++
	}
	return materialized, synced, nil
}
