package authz

// Migraciones de NX-05 (design §2 y §6). Idempotentes; se ejecutan dentro de
// la transacción de migraciones Nexo (cloudstore/nexo_migrations.go), que ya
// tiene el advisory lock de migración.

import (
	"context"
	"database/sql"
	"fmt"
)

// guardLockSQL serializa todo lo que puede dejar a la plataforma sin
// superadmin (R-8). Lo toman el arranque, el servicio y los triggers.
const guardLockSQL = `SELECT pg_advisory_xact_lock(hashtext('nexo_authz_superadmin_guard'))`

var migrationQueries = []string{
	// ─── Ámbitos ─────────────────────────────────────────────────────────────
	`CREATE TABLE IF NOT EXISTS nexo_scopes (
		scope_type   TEXT NOT NULL CHECK (scope_type IN ('platform', 'project', 'space')),
		scope_key    TEXT NOT NULL CHECK (scope_key <> ''),
		visibility   TEXT NOT NULL DEFAULT 'restricted' CHECK (visibility IN ('open', 'restricted')),
		default_role TEXT NOT NULL DEFAULT 'lector' CHECK (default_role IN ('lector', 'proponente')),
		status       TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
		created_by_principal_id BIGINT REFERENCES cloud_principals(id) ON DELETE SET NULL,
		created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		PRIMARY KEY (scope_type, scope_key),
		CONSTRAINT nexo_scopes_platform_key CHECK ((scope_type = 'platform') = (scope_key = '*'))
	)`,
	`CREATE INDEX IF NOT EXISTS idx_nexo_scopes_open ON nexo_scopes (scope_type, visibility, status)`,
	`INSERT INTO nexo_scopes (scope_type, scope_key, visibility, status) VALUES ('platform', '*', 'restricted', 'active') ON CONFLICT DO NOTHING`,

	// ─── Asignaciones ────────────────────────────────────────────────────────
	`CREATE TABLE IF NOT EXISTS nexo_role_bindings (
		principal_id BIGINT NOT NULL REFERENCES cloud_principals(id) ON DELETE CASCADE,
		scope_type   TEXT NOT NULL,
		scope_key    TEXT NOT NULL,
		role         TEXT NOT NULL CHECK (role IN ('superadmin', 'admin', 'auditor', 'lector', 'proponente', 'publicador', 'responsable')),
		granted_by_principal_id BIGINT REFERENCES cloud_principals(id) ON DELETE SET NULL,
		created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		PRIMARY KEY (principal_id, scope_type, scope_key),
		FOREIGN KEY (scope_type, scope_key) REFERENCES nexo_scopes (scope_type, scope_key)
	)`,
	// La PK empieza por principal_id: cubre el índice (principal_id) de
	// VisibleScopes. Este cubre listar miembros y contar responsables.
	`CREATE INDEX IF NOT EXISTS idx_nexo_role_bindings_scope ON nexo_role_bindings (scope_type, scope_key, role)`,

	// Combinaciones válidas rol/nivel y cuentas de servicio (R-10).
	`CREATE OR REPLACE FUNCTION nexo_role_bindings_validate() RETURNS trigger AS $$
	DECLARE
		v_kind TEXT;
	BEGIN
		IF NEW.scope_type = 'platform' THEN
			IF NEW.role NOT IN ('superadmin', 'admin', 'auditor') THEN
				RAISE EXCEPTION 'nexo_invalid_role: % no es un rol de plataforma', NEW.role USING ERRCODE = 'check_violation';
			END IF;
		ELSIF NEW.role NOT IN ('lector', 'proponente', 'publicador', 'responsable') THEN
			RAISE EXCEPTION 'nexo_invalid_role: % no es un rol de %', NEW.role, NEW.scope_type USING ERRCODE = 'check_violation';
		END IF;
		SELECT kind INTO v_kind FROM cloud_principals WHERE id = NEW.principal_id;
		IF v_kind = 'service_account' AND (NEW.scope_type = 'platform' OR NEW.role = 'responsable') THEN
			RAISE EXCEPTION 'nexo_service_account_role: una cuenta de servicio no puede tener el rol % (R-10)', NEW.role USING ERRCODE = 'check_violation';
		END IF;
		RETURN NEW;
	END
	$$ LANGUAGE plpgsql`,

	// R-8 en la base: no se quita ni degrada la asignación del último
	// superadmin habilitado, por ninguna vía.
	`CREATE OR REPLACE FUNCTION nexo_role_bindings_guard() RETURNS trigger AS $$
	BEGIN
		IF OLD.scope_type = 'platform' AND OLD.role = 'superadmin' AND (TG_OP = 'DELETE' OR NEW.role <> 'superadmin') THEN
			PERFORM pg_advisory_xact_lock(hashtext('nexo_authz_superadmin_guard'));
			IF EXISTS (SELECT 1 FROM cloud_principals WHERE id = OLD.principal_id AND enabled)
			   AND NOT EXISTS (
				SELECT 1 FROM nexo_role_bindings b JOIN cloud_principals p ON p.id = b.principal_id
				WHERE b.scope_type = 'platform' AND b.scope_key = '*' AND b.role = 'superadmin'
				  AND p.enabled AND b.principal_id <> OLD.principal_id) THEN
				RAISE EXCEPTION 'nexo_last_superadmin: no se puede quitar ni degradar al último superadmin habilitado (R-8)' USING ERRCODE = 'check_violation';
			END IF;
		END IF;
		IF TG_OP = 'DELETE' THEN
			RETURN OLD;
		END IF;
		RETURN NEW;
	END
	$$ LANGUAGE plpgsql`,

	// R-8 sobre cloud_principals: deshabilitar, cambiar el rol de Engram o
	// borrar al último superadmin habilitado se rechaza (cubre /admin/*, el
	// panel, la CLI y SQL directo).
	`CREATE OR REPLACE FUNCTION nexo_authz_guard_principal() RETURNS trigger AS $$
	BEGIN
		IF NOT OLD.enabled THEN
			IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
			RETURN NEW;
		END IF;
		IF TG_OP = 'UPDATE' AND NEW.enabled AND NEW.role = 'admin' THEN
			RETURN NEW;
		END IF;
		IF EXISTS (SELECT 1 FROM nexo_role_bindings WHERE principal_id = OLD.id AND scope_type = 'platform' AND role = 'superadmin') THEN
			PERFORM pg_advisory_xact_lock(hashtext('nexo_authz_superadmin_guard'));
			IF NOT EXISTS (
				SELECT 1 FROM nexo_role_bindings b JOIN cloud_principals p ON p.id = b.principal_id
				WHERE b.scope_type = 'platform' AND b.scope_key = '*' AND b.role = 'superadmin'
				  AND p.enabled AND b.principal_id <> OLD.id) THEN
				RAISE EXCEPTION 'nexo_last_superadmin: no se puede deshabilitar, degradar ni borrar al último superadmin habilitado (R-8)' USING ERRCODE = 'check_violation';
			END IF;
		END IF;
		IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
		RETURN NEW;
	END
	$$ LANGUAGE plpgsql`,

	`DO $$ BEGIN
		IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'nexo_role_bindings_validate' AND tgrelid = 'nexo_role_bindings'::regclass) THEN
			CREATE TRIGGER nexo_role_bindings_validate BEFORE INSERT OR UPDATE ON nexo_role_bindings
				FOR EACH ROW EXECUTE FUNCTION nexo_role_bindings_validate();
		END IF;
		IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'nexo_role_bindings_guard' AND tgrelid = 'nexo_role_bindings'::regclass) THEN
			CREATE TRIGGER nexo_role_bindings_guard BEFORE UPDATE OF role OR DELETE ON nexo_role_bindings
				FOR EACH ROW EXECUTE FUNCTION nexo_role_bindings_guard();
		END IF;
		IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'nexo_authz_guard_principal' AND tgrelid = 'cloud_principals'::regclass) THEN
			CREATE TRIGGER nexo_authz_guard_principal BEFORE UPDATE OF enabled, role OR DELETE ON cloud_principals
				FOR EACH ROW EXECUTE FUNCTION nexo_authz_guard_principal();
		END IF;
	END $$`,

	// ─── Eventos (solo inserción) ────────────────────────────────────────────
	`CREATE TABLE IF NOT EXISTS nexo_authz_events (
		id                  BIGSERIAL PRIMARY KEY,
		occurred_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		action              TEXT NOT NULL,
		scope_type          TEXT NOT NULL,
		scope_key           TEXT NOT NULL,
		target_principal_id BIGINT,
		role_before         TEXT,
		role_after          TEXT,
		actor_principal_id  BIGINT,
		actor_token_id      BIGINT,
		actor_label         TEXT NOT NULL,
		actor_email         TEXT,
		actor_verified      BOOLEAN NOT NULL DEFAULT FALSE,
		client_ip           TEXT,
		user_agent          TEXT,
		client_claim        TEXT,
		details             JSONB
	)`,
	`CREATE INDEX IF NOT EXISTS idx_nexo_authz_events_scope ON nexo_authz_events (scope_type, scope_key, id DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_nexo_authz_events_actor ON nexo_authz_events (actor_principal_id, id DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_nexo_authz_events_target ON nexo_authz_events (target_principal_id, id DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_nexo_authz_events_action ON nexo_authz_events (action, id DESC)`,
	// NX-07 (S4-05): el único DELETE admitido es el de la depuración por
	// retención (nexo_purge_evidence, de la cuenta dueña, filas de más de 365
	// días); la comprobación nexo_evidence_purge_allowed la crea
	// internal/nexo/security/evidence en la misma transacción de migración.
	`CREATE OR REPLACE FUNCTION nexo_authz_events_immutable() RETURNS trigger AS $$
	BEGIN
		IF TG_OP = 'DELETE' AND current_setting('nexo.purge_in_progress', true) = TG_TABLE_NAME THEN
			IF nexo_evidence_purge_allowed(TG_TABLE_NAME, TG_RELID, OLD.occurred_at) THEN
				RETURN OLD;
			END IF;
		END IF;
		RAISE EXCEPTION 'nexo_authz_events es de solo inserción (% no permitido)', TG_OP
			USING ERRCODE = 'insufficient_privilege';
	END
	$$ LANGUAGE plpgsql`,
	`DO $$ BEGIN
		IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'nexo_authz_events_no_update_delete' AND tgrelid = 'nexo_authz_events'::regclass) THEN
			CREATE TRIGGER nexo_authz_events_no_update_delete BEFORE UPDATE OR DELETE ON nexo_authz_events
				FOR EACH ROW EXECUTE FUNCTION nexo_authz_events_immutable();
		END IF;
		IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'nexo_authz_events_no_truncate' AND tgrelid = 'nexo_authz_events'::regclass) THEN
			CREATE TRIGGER nexo_authz_events_no_truncate BEFORE TRUNCATE ON nexo_authz_events
				FOR EACH STATEMENT EXECUTE FUNCTION nexo_authz_events_immutable();
		END IF;
	END $$`,

}

// Migrate crea las tablas, triggers y la fila de plataforma, y asigna el
// superadmin de arranque si corresponde. Idempotente.
func Migrate(ctx context.Context, tx *sql.Tx) error {
	for _, q := range migrationQueries {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("authz migrate: %w", err)
		}
	}
	// SG-10: tabla lateral de las cuentas de servicio (serviceaccounts.go).
	for _, q := range serviceAccountMigrations {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("authz migrate service accounts: %w", err)
		}
	}
	// Unificación de roles de plataforma (migrate_roles.go): triggers que derivan
	// la columna de Engram y corrección única de instalaciones existentes. Reemplaza
	// al arranque del superadmin por candidato (diseño §6.3), que ahora ocurre una
	// sola vez dentro de la corrección.
	if err := migratePlatformRoles(ctx, tx); err != nil {
		return err
	}
	return nil
}
