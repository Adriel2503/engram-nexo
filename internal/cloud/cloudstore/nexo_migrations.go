package cloudstore

// Nexo IA · migraciones propias del fork (NX-01, NX-02).
//
// Reglas: solo aditivas e idempotentes (ADD COLUMN IF NOT EXISTS,
// CREATE ... IF NOT EXISTS, CREATE OR REPLACE). Nada se renombra ni se borra.
// Se ejecutan al final de migrate(), dentro de una transacción con un
// advisory lock para que varios procesos que arrancan a la vez (o paquetes de
// tests en paralelo) no choquen al crear funciones, triggers o vistas.

import (
	"context"
	"fmt"
)

// nexoMigrationLockKey es una llave fija para pg_advisory_xact_lock.
const nexoMigrationLockKey int64 = 0x4e45584f // "NEXO"

var nexoMigrationQueries = []string{
	// ─── NX-01: autoría por cambio ───────────────────────────────────────────
	`ALTER TABLE cloud_mutations ADD COLUMN IF NOT EXISTS actor TEXT`,
	`ALTER TABLE cloud_mutations ADD COLUMN IF NOT EXISTS actor_email TEXT`,
	`ALTER TABLE cloud_mutations ADD COLUMN IF NOT EXISTS actor_principal_id TEXT`,
	`ALTER TABLE cloud_mutations ADD COLUMN IF NOT EXISTS actor_token_id TEXT`,
	`ALTER TABLE cloud_mutations ADD COLUMN IF NOT EXISTS actor_verified BOOLEAN NOT NULL DEFAULT FALSE`,
	`ALTER TABLE cloud_mutations ADD COLUMN IF NOT EXISTS actor_client_claim TEXT`,
	`CREATE INDEX IF NOT EXISTS idx_cloud_mutations_actor_principal ON cloud_mutations (actor_principal_id, seq)`,
	`CREATE INDEX IF NOT EXISTS idx_cloud_mutations_entity_key ON cloud_mutations (project, entity_key, seq)`,
	`ALTER TABLE cloud_chunks ADD COLUMN IF NOT EXISTS actor TEXT`,
	`ALTER TABLE cloud_chunks ADD COLUMN IF NOT EXISTS actor_email TEXT`,
	`ALTER TABLE cloud_chunks ADD COLUMN IF NOT EXISTS actor_principal_id TEXT`,
	`ALTER TABLE cloud_chunks ADD COLUMN IF NOT EXISTS actor_token_id TEXT`,
	`ALTER TABLE cloud_chunks ADD COLUMN IF NOT EXISTS actor_verified BOOLEAN NOT NULL DEFAULT FALSE`,
	`ALTER TABLE cloud_chunks ADD COLUMN IF NOT EXISTS actor_client_claim TEXT`,
	// El trigger copia el actor publicado por nexoApplyMutationActor
	// (variables locales de la transacción). Sin actor publicado, la fila
	// queda sin autor (RF-08). El email se copia aquí, en el INSERT, con una
	// subconsulta a cloud_human_users (RF-03b).
	`CREATE OR REPLACE FUNCTION nexo_stamp_actor() RETURNS trigger AS $$
	DECLARE
		v_actor TEXT := NULLIF(current_setting('nexo.actor', true), '');
	BEGIN
		IF v_actor IS NULL OR NEW.actor IS NOT NULL THEN
			RETURN NEW;
		END IF;
		NEW.actor := v_actor;
		NEW.actor_principal_id := NULLIF(current_setting('nexo.actor_principal_id', true), '');
		NEW.actor_token_id := NULLIF(current_setting('nexo.actor_token_id', true), '');
		NEW.actor_verified := COALESCE(current_setting('nexo.actor_verified', true) = 'true', FALSE);
		NEW.actor_client_claim := NULLIF(current_setting('nexo.actor_client_claim', true), '');
		IF NEW.actor_principal_id IS NOT NULL THEN
			NEW.actor_email := (SELECT u.email FROM cloud_human_users u WHERE u.principal_id::text = NEW.actor_principal_id);
		END IF;
		RETURN NEW;
	END
	$$ LANGUAGE plpgsql`,
	`DO $$ BEGIN
		IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'nexo_stamp_actor_mutations' AND tgrelid = 'cloud_mutations'::regclass) THEN
			CREATE TRIGGER nexo_stamp_actor_mutations BEFORE INSERT ON cloud_mutations
				FOR EACH ROW EXECUTE FUNCTION nexo_stamp_actor();
		END IF;
		IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'nexo_stamp_actor_chunks' AND tgrelid = 'cloud_chunks'::regclass) THEN
			CREATE TRIGGER nexo_stamp_actor_chunks BEFORE INSERT ON cloud_chunks
				FOR EACH ROW EXECUTE FUNCTION nexo_stamp_actor();
		END IF;
	END $$`,
	// Historial de un recuerdo (RF-07). Principal.ID y TokenID son los id
	// BIGINT de cloud_principals / cloud_principal_tokens como texto (T-03).
	`CREATE OR REPLACE VIEW nexo_mutation_history AS
	SELECT m.seq, m.project, m.entity, m.entity_key, m.op, m.occurred_at,
	       m.actor, m.actor_email,
	       m.actor_verified, m.actor_principal_id,
	       u.username, u.email AS current_email,
	       t.token_prefix, t.name AS token_name,
	       m.actor_client_claim,
	       m.payload->>'title' AS title,
	       m.actor_token_id
	FROM cloud_mutations m
	LEFT JOIN cloud_human_users u ON u.principal_id::text = m.actor_principal_id
	LEFT JOIN cloud_principal_tokens t ON t.id::text = m.actor_token_id`,

	// ─── NX-02: registro de conexiones ───────────────────────────────────────
	`CREATE TABLE IF NOT EXISTS nexo_activity_log (
		id                 BIGSERIAL PRIMARY KEY,
		occurred_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		actor              TEXT NOT NULL,
		actor_email        TEXT,
		actor_principal_id TEXT,
		actor_token_id     TEXT,
		actor_verified     BOOLEAN NOT NULL DEFAULT FALSE,
		operation          TEXT NOT NULL,
		project            TEXT,
		item_count         INTEGER NOT NULL DEFAULT 0,
		client_ip          TEXT,
		user_agent         TEXT,
		client_claim       TEXT
	)`,
	`CREATE INDEX IF NOT EXISTS idx_nexo_activity_occurred ON nexo_activity_log (occurred_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_nexo_activity_principal ON nexo_activity_log (actor_principal_id, occurred_at DESC)`,
	`CREATE TABLE IF NOT EXISTS nexo_actor_presence (
		presence_key       TEXT PRIMARY KEY,
		actor              TEXT NOT NULL,
		actor_email        TEXT,
		actor_principal_id TEXT,
		actor_token_id     TEXT,
		actor_verified     BOOLEAN NOT NULL DEFAULT FALSE,
		first_seen_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		last_seen_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		last_ip            TEXT,
		last_user_agent    TEXT,
		last_client_claim  TEXT,
		last_project       TEXT,
		push_count         BIGINT NOT NULL DEFAULT 0,
		pull_count         BIGINT NOT NULL DEFAULT 0
	)`,
	// Inmutabilidad (RF-13): UPDATE, DELETE y TRUNCATE se rechazan. Solo la
	// depuración por antigüedad (PurgeActivityOlderThan) puede borrar, y solo
	// dentro de su transacción con set_config('nexo.allow_retention','on',true).
	`CREATE OR REPLACE FUNCTION nexo_activity_log_immutable() RETURNS trigger AS $$
	BEGIN
		IF TG_OP = 'DELETE' AND current_setting('nexo.allow_retention', true) = 'on' THEN
			RETURN OLD;
		END IF;
		RAISE EXCEPTION 'nexo_activity_log es de solo inserción (% no permitido)', TG_OP
			USING ERRCODE = 'insufficient_privilege';
	END
	$$ LANGUAGE plpgsql`,
	`DO $$ BEGIN
		IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'nexo_activity_log_no_update_delete' AND tgrelid = 'nexo_activity_log'::regclass) THEN
			CREATE TRIGGER nexo_activity_log_no_update_delete BEFORE UPDATE OR DELETE ON nexo_activity_log
				FOR EACH ROW EXECUTE FUNCTION nexo_activity_log_immutable();
		END IF;
		IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'nexo_activity_log_no_truncate' AND tgrelid = 'nexo_activity_log'::regclass) THEN
			CREATE TRIGGER nexo_activity_log_no_truncate BEFORE TRUNCATE ON nexo_activity_log
				FOR EACH STATEMENT EXECUTE FUNCTION nexo_activity_log_immutable();
		END IF;
	END $$`,
}

// nexoMigrate aplica las migraciones de Nexo. Se llama al final de migrate().
func (cs *CloudStore) nexoMigrate(ctx context.Context) error {
	tx, err := cs.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cloudstore: nexo migrate begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, nexoMigrationLockKey); err != nil {
		return fmt.Errorf("cloudstore: nexo migrate lock: %w", err)
	}
	for _, q := range nexoMigrationQueries {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("cloudstore: nexo migrate: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cloudstore: nexo migrate commit: %w", err)
	}
	return nil
}
