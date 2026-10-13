// Package store contiene las migraciones y el SQL de las tablas nexo_kb_*
// (NX-10, design §5 y §14.4). Recibe conexiones o transacciones ya abiertas:
// no depende de cloudstore ni de cloudserver.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"
)

// TSConfig es la configuración de búsqueda en español que crea la migración.
const TSConfig = "nexo_es"

// IndexKindPGFTS es el índice de texto de Postgres registrado en
// nexo_kb_index_versions.
const IndexKindPGFTS = "pgfts"

// IndexVersionPGFTS es la versión vigente del índice pgfts (cambiarla obliga a
// reindexar: tsv se recalcula desde las secciones).
const IndexVersionPGFTS = 1

var migrationQueries = []string{
	// ─── Espacios (la visibilidad, el estado y los miembros están en
	// nexo_scopes y nexo_role_bindings, NX-05) ──────────────────────────────
	`CREATE TABLE IF NOT EXISTS nexo_kb_spaces (
		id          UUID PRIMARY KEY,
		scope_type  TEXT NOT NULL DEFAULT 'space' CHECK (scope_type = 'space'),
		slug        TEXT NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9][a-z0-9-]{1,62}$'),
		name        TEXT NOT NULL DEFAULT '',
		description TEXT NOT NULL DEFAULT '',
		categories  TEXT[] NOT NULL DEFAULT ARRAY['estandar','convencion','lineamiento','glosario','regla_negocio','catalogo'],
		settings    JSONB NOT NULL DEFAULT '{}'::jsonb,
		created_by_principal_id BIGINT REFERENCES cloud_principals(id) ON DELETE SET NULL,
		created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		FOREIGN KEY (scope_type, slug) REFERENCES nexo_scopes (scope_type, scope_key)
	)`,

	// ─── Documentos ──────────────────────────────────────────────────────────
	`CREATE TABLE IF NOT EXISTS nexo_kb_documents (
		id              UUID PRIMARY KEY,
		space_id        UUID NOT NULL REFERENCES nexo_kb_spaces(id),
		doc_key         TEXT NOT NULL CHECK (doc_key <> ''),
		title           TEXT NOT NULL,
		category        TEXT NOT NULL,
		tags            TEXT[] NOT NULL DEFAULT '{}',
		owner_principal_id BIGINT REFERENCES cloud_principals(id) ON DELETE SET NULL,
		owner_label     TEXT NOT NULL DEFAULT '',
		status          TEXT NOT NULL CHECK (status IN ('active', 'retired')),
		valid_from      TIMESTAMPTZ NOT NULL,
		valid_until     TIMESTAMPTZ,
		current_version INT NOT NULL CHECK (current_version > 0),
		superseded_by   UUID REFERENCES nexo_kb_documents(id),
		origin_kind     TEXT,
		origin_ref      TEXT,
		created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		retired_at      TIMESTAMPTZ,
		retirement_note TEXT,
		UNIQUE (space_id, doc_key),
		CHECK (superseded_by IS NULL OR superseded_by <> id)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_nexo_kb_documents_origin ON nexo_kb_documents (space_id, origin_kind, origin_ref) WHERE status = 'active'`,
	`CREATE INDEX IF NOT EXISTS idx_nexo_kb_documents_list ON nexo_kb_documents (space_id, status, doc_key)`,

	// ─── Versiones (solo inserción) ──────────────────────────────────────────
	// Sin FK a cloud_principals: un ON DELETE SET NULL sería un UPDATE y la
	// tabla es inmutable (mismo criterio que nexo_authz_events).
	`CREATE TABLE IF NOT EXISTS nexo_kb_versions (
		id                UUID PRIMARY KEY,
		document_id       UUID NOT NULL REFERENCES nexo_kb_documents(id),
		space_id          UUID NOT NULL REFERENCES nexo_kb_spaces(id),
		version           INT NOT NULL CHECK (version > 0),
		title             TEXT NOT NULL,
		category          TEXT NOT NULL,
		tags              TEXT[] NOT NULL DEFAULT '{}',
		owner_label       TEXT NOT NULL DEFAULT '',
		content_md        TEXT NOT NULL,
		content_sha256    TEXT NOT NULL,
		meta_sha256       TEXT NOT NULL,
		source_format     TEXT NOT NULL,
		source_mime       TEXT,
		source_sha256     TEXT,
		source_size_bytes BIGINT NOT NULL DEFAULT 0 CHECK (source_size_bytes >= 0),
		source_blob_ref   TEXT,
		origin            JSONB NOT NULL DEFAULT '{}'::jsonb,
		change_note       TEXT,
		valid_from        TIMESTAMPTZ NOT NULL,
		valid_until       TIMESTAMPTZ,
		section_count     INT NOT NULL DEFAULT 0,
		published_by_principal_id BIGINT,
		published_by_token_id     BIGINT,
		published_by_label        TEXT NOT NULL,
		published_by_email        TEXT,
		published_by_verified     BOOLEAN NOT NULL DEFAULT FALSE,
		published_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		UNIQUE (document_id, version),
		CHECK (valid_until IS NULL OR valid_until > valid_from)
	)`,

	// ─── Secciones (bitemporales, design §14) ────────────────────────────────
	`CREATE TABLE IF NOT EXISTS nexo_kb_sections (
		id              UUID PRIMARY KEY,
		space_id        UUID NOT NULL,
		document_id     UUID NOT NULL REFERENCES nexo_kb_documents(id),
		version_id      UUID NOT NULL REFERENCES nexo_kb_versions(id),
		ordinal         INT NOT NULL,
		anchor          TEXT NOT NULL,
		heading_path    TEXT[] NOT NULL DEFAULT '{}',
		body            TEXT NOT NULL,
		token_count     INT NOT NULL DEFAULT 0,
		source_location JSONB NOT NULL DEFAULT '{}'::jsonb,
		extraction      TEXT NOT NULL DEFAULT 'text',
		category        TEXT NOT NULL,
		tags            TEXT[] NOT NULL DEFAULT '{}',
		valid_from      TIMESTAMPTZ NOT NULL,
		valid_until     TIMESTAMPTZ,
		recorded_from   TIMESTAMPTZ NOT NULL,
		recorded_until  TIMESTAMPTZ,
		is_current      BOOLEAN NOT NULL,
		tsv             TSVECTOR NOT NULL,
		UNIQUE (version_id, anchor),
		CHECK (recorded_until IS NULL OR recorded_until >= recorded_from)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_nexo_kb_sections_tsv_current ON nexo_kb_sections USING GIN (tsv) WHERE is_current`,
	`CREATE INDEX IF NOT EXISTS idx_nexo_kb_sections_tsv_all ON nexo_kb_sections USING GIN (tsv)`,
	`CREATE INDEX IF NOT EXISTS idx_nexo_kb_sections_space_current ON nexo_kb_sections (space_id, is_current)`,
	`CREATE INDEX IF NOT EXISTS idx_nexo_kb_sections_doc ON nexo_kb_sections (document_id, version_id, ordinal)`,
	`CREATE INDEX IF NOT EXISTS idx_nexo_kb_sections_doc_current ON nexo_kb_sections (document_id) WHERE is_current`,

	// ─── Propuestas (la lógica llega en la entrega 2b) ───────────────────────
	`CREATE TABLE IF NOT EXISTS nexo_kb_proposals (
		id                 UUID PRIMARY KEY,
		space_id           UUID NOT NULL REFERENCES nexo_kb_spaces(id),
		target_document_id UUID REFERENCES nexo_kb_documents(id),
		base_version       INT,
		doc_key            TEXT,
		title              TEXT NOT NULL,
		category           TEXT,
		content_md         TEXT NOT NULL,
		rationale          TEXT,
		status             TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'approved', 'discarded', 'withdrawn')),
		proposed_by_principal_id BIGINT,
		proposed_by_token_id     BIGINT,
		proposed_by_label        TEXT NOT NULL DEFAULT '',
		proposed_by_email        TEXT,
		proposed_by_client_claim TEXT,
		reviewed_by_principal_id BIGINT,
		reviewed_by_token_id     BIGINT,
		reviewed_by_label        TEXT,
		review_note        TEXT,
		decided_at         TIMESTAMPTZ,
		result_version_id  UUID REFERENCES nexo_kb_versions(id),
		created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,
	`CREATE INDEX IF NOT EXISTS idx_nexo_kb_proposals_space ON nexo_kb_proposals (space_id, status, created_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_nexo_kb_proposals_author ON nexo_kb_proposals (proposed_by_principal_id, created_at DESC)`,

	// ─── Eventos (solo inserción) ────────────────────────────────────────────
	`CREATE TABLE IF NOT EXISTS nexo_kb_events (
		id                 BIGSERIAL PRIMARY KEY,
		occurred_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		space_id           UUID,
		space_slug         TEXT NOT NULL DEFAULT '',
		action             TEXT NOT NULL,
		target_kind        TEXT,
		target_id          TEXT,
		actor_principal_id BIGINT,
		actor_token_id     BIGINT,
		actor_label        TEXT NOT NULL,
		actor_email        TEXT,
		actor_verified     BOOLEAN NOT NULL DEFAULT FALSE,
		client_ip          TEXT,
		user_agent         TEXT,
		client_claim       TEXT,
		details            JSONB
	)`,
	`CREATE INDEX IF NOT EXISTS idx_nexo_kb_events_space ON nexo_kb_events (space_id, id DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_nexo_kb_events_slug ON nexo_kb_events (space_slug, id DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_nexo_kb_events_action ON nexo_kb_events (action, id DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_nexo_kb_events_actor ON nexo_kb_events (actor_principal_id, id DESC)`,

	// ─── Feed de cambios para copias locales (se consume en 2b) ──────────────
	`CREATE TABLE IF NOT EXISTS nexo_kb_changes (
		seq         BIGSERIAL PRIMARY KEY,
		space_id    UUID NOT NULL,
		document_id UUID,
		kind        TEXT NOT NULL,
		occurred_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,
	`CREATE INDEX IF NOT EXISTS idx_nexo_kb_changes_space ON nexo_kb_changes (space_id, seq)`,

	// ─── Versiones de índices derivados (design §14.3) ───────────────────────
	`CREATE TABLE IF NOT EXISTS nexo_kb_index_versions (
		index_kind TEXT NOT NULL,
		version    INT NOT NULL,
		config     JSONB NOT NULL DEFAULT '{}'::jsonb,
		status     TEXT NOT NULL CHECK (status IN ('building', 'active', 'retired')),
		built_at   TIMESTAMPTZ,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		PRIMARY KEY (index_kind, version)
	)`,

	// ─── Inmutabilidad de versiones y eventos (como nexo_authz_events) ───────
	// NX-07 (S4-05): en nexo_kb_events el único DELETE admitido es el de la
	// depuración por retención (nexo_purge_evidence, cuenta dueña, filas de más
	// de 365 días; ver internal/nexo/security/evidence). nexo_kb_versions no se
	// depura nunca.
	`CREATE OR REPLACE FUNCTION nexo_kb_immutable() RETURNS trigger AS $$
	BEGIN
		IF TG_OP = 'DELETE' AND TG_TABLE_NAME = 'nexo_kb_events' AND current_setting('nexo.purge_in_progress', true) = TG_TABLE_NAME THEN
			IF nexo_evidence_purge_allowed(TG_TABLE_NAME, TG_RELID, OLD.occurred_at) THEN
				RETURN OLD;
			END IF;
		END IF;
		RAISE EXCEPTION '% es de solo inserción (% no permitido)', TG_TABLE_NAME, TG_OP
			USING ERRCODE = 'insufficient_privilege';
	END
	$$ LANGUAGE plpgsql`,
	`DO $$ BEGIN
		IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'nexo_kb_versions_no_update_delete' AND tgrelid = 'nexo_kb_versions'::regclass) THEN
			CREATE TRIGGER nexo_kb_versions_no_update_delete BEFORE UPDATE OR DELETE ON nexo_kb_versions
				FOR EACH ROW EXECUTE FUNCTION nexo_kb_immutable();
		END IF;
		IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'nexo_kb_versions_no_truncate' AND tgrelid = 'nexo_kb_versions'::regclass) THEN
			CREATE TRIGGER nexo_kb_versions_no_truncate BEFORE TRUNCATE ON nexo_kb_versions
				FOR EACH STATEMENT EXECUTE FUNCTION nexo_kb_immutable();
		END IF;
		IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'nexo_kb_events_no_update_delete' AND tgrelid = 'nexo_kb_events'::regclass) THEN
			CREATE TRIGGER nexo_kb_events_no_update_delete BEFORE UPDATE OR DELETE ON nexo_kb_events
				FOR EACH ROW EXECUTE FUNCTION nexo_kb_immutable();
		END IF;
		IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'nexo_kb_events_no_truncate' AND tgrelid = 'nexo_kb_events'::regclass) THEN
			CREATE TRIGGER nexo_kb_events_no_truncate BEFORE TRUNCATE ON nexo_kb_events
				FOR EACH STATEMENT EXECUTE FUNCTION nexo_kb_immutable();
		END IF;
	END $$`,
}

// Migrate crea las tablas nexo_kb_*, sus índices, los triggers de
// inmutabilidad y la configuración de búsqueda nexo_es. Idempotente. Se
// ejecuta dentro de la transacción de migraciones Nexo (advisory lock) con la
// cuenta que migra (la dueña en el modo de dos cuentas, NX-07), después de
// authz.Migrate (nexo_kb_spaces referencia nexo_scopes).
func Migrate(ctx context.Context, tx *sql.Tx) error {
	for _, q := range migrationQueries {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("kb migrate: %w", err)
		}
	}
	if err := migrateR2(ctx, tx); err != nil { // R2: pgvector y cola de vectorización (vectors.go)
		return err
	}
	if err := migrate2b(ctx, tx); err != nil { // entrega 2b (migrate2b.go)
		return err
	}
	unaccent, err := ensureTextSearchConfig(ctx, tx)
	if err != nil {
		return fmt.Errorf("kb migrate: text search: %w", err)
	}
	cfg := fmt.Sprintf(`{"ts_config": %q, "ts_config_unaccent": %q, "base": "spanish", "unaccent": %t, "accent_folding": %q, "tsv": "original || sin tildes", "weights": {"A": "heading_path", "B": "body"}, "rank": "ts_rank_cd(tsv, query, 1)"}`,
		TSConfig, map[bool]string{true: TSConfigUnaccent, false: ""}[unaccent], unaccent, map[bool]string{true: "postgres-unaccent", false: "go-fold"}[unaccent])
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO nexo_kb_index_versions (index_kind, version, config, status, built_at)
		VALUES ($1, $2, $3::jsonb, 'active', NOW())
		ON CONFLICT (index_kind, version) DO UPDATE SET config = EXCLUDED.config, updated_at = NOW()
		WHERE nexo_kb_index_versions.config IS DISTINCT FROM EXCLUDED.config`, IndexKindPGFTS, IndexVersionPGFTS, cfg); err != nil {
		return fmt.Errorf("kb migrate: index versions: %w", err)
	}
	return nil
}

// EnvDisableUnaccent fuerza el modo sin unaccent (plegado de tildes en Go)
// al crear la configuración por primera vez. Útil si la extensión existe pero
// no se quiere depender de ella.
const EnvDisableUnaccent = "NEXO_KB_DISABLE_UNACCENT"

// TSConfigUnaccent es la variante sin tildes (unaccent + spanish_stem).
const TSConfigUnaccent = "nexo_es_unaccent"

func tsConfigExists(ctx context.Context, q queryRower, name string) (bool, error) {
	var exists bool
	err := q.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM pg_ts_config c JOIN pg_namespace n ON n.oid = c.cfgnamespace
		               WHERE c.cfgname = $1 AND n.nspname = current_schema())`, name).Scan(&exists)
	return exists, err
}

// ensureTextSearchConfig crea las configuraciones de búsqueda en el esquema
// actual (design "Desviaciones", D-I2):
//
//   - nexo_es: copia de spanish. Lematiza con las tildes originales (el
//     lematizador Snowball reconoce sufijos acentuados como "-ación": aplicar
//     unaccent antes rompe "validación"/"validaciones").
//   - nexo_es_unaccent: unaccent + spanish_stem, solo si la extensión unaccent
//     existe o se puede crear. Indexa la variante sin tildes del mismo texto,
//     para quien escribe sin tildes ("validacion").
//
// Sin unaccent, la variante sin tildes se calcula en Go (ingest.Fold) con
// nexo_es. Una vez creadas, las configuraciones no se cambian (cambiar de modo
// exige reindexar): el modo se lee del catálogo.
func ensureTextSearchConfig(ctx context.Context, tx *sql.Tx) (bool, error) {
	exists, err := tsConfigExists(ctx, tx, TSConfig)
	if err != nil {
		return false, err
	}
	if exists {
		return tsConfigExists(ctx, tx, TSConfigUnaccent)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TEXT SEARCH CONFIGURATION `+TSConfig+` (COPY = pg_catalog.spanish)`); err != nil {
		return false, err
	}
	if v := strings.TrimSpace(os.Getenv(EnvDisableUnaccent)); v == "1" || strings.EqualFold(v, "true") {
		return false, nil
	}
	schema, err := unaccentSchema(ctx, tx)
	if err != nil {
		return false, err
	}
	if schema == "" {
		// Intentar crear la extensión (requiere permiso; en PG13+ es
		// "trusted" y basta CREATE en la base). Un fallo no aborta la
		// migración: se usa el modo sin unaccent.
		if _, err := tx.ExecContext(ctx, `SAVEPOINT nexo_kb_unaccent`); err != nil {
			return false, err
		}
		target := "public"
		var hasPublic bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = 'public') AND has_schema_privilege('public', 'CREATE')`).Scan(&hasPublic); err != nil || !hasPublic {
			target = ""
		}
		stmt := `CREATE EXTENSION IF NOT EXISTS unaccent`
		if target != "" {
			stmt += ` SCHEMA ` + target
		}
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			if _, rerr := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT nexo_kb_unaccent`); rerr != nil {
				return false, rerr
			}
			log.Printf("[nexo] kb: extensión unaccent no disponible (%v); las tildes se quitan en la aplicación", err)
		} else if _, err := tx.ExecContext(ctx, `RELEASE SAVEPOINT nexo_kb_unaccent`); err != nil {
			return false, err
		}
		if schema, err = unaccentSchema(ctx, tx); err != nil {
			return false, err
		}
	}
	if schema == "" {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `CREATE TEXT SEARCH CONFIGURATION `+TSConfigUnaccent+` (COPY = pg_catalog.spanish)`); err != nil {
		return false, err
	}
	dict := quoteIdent(schema) + ".unaccent"
	if _, err := tx.ExecContext(ctx, `ALTER TEXT SEARCH CONFIGURATION `+TSConfigUnaccent+` ALTER MAPPING FOR hword, hword_part, word WITH `+dict+`, pg_catalog.spanish_stem`); err != nil {
		return false, err
	}
	return true, nil
}

func unaccentSchema(ctx context.Context, tx *sql.Tx) (string, error) {
	var schema sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT n.nspname FROM pg_ts_dict d JOIN pg_namespace n ON n.oid = d.dictnamespace WHERE d.dictname = 'unaccent' ORDER BY (n.nspname = 'public') DESC LIMIT 1`).Scan(&schema)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return schema.String, err
}

type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func configUsesUnaccent(ctx context.Context, q queryRower) (bool, error) {
	return tsConfigExists(ctx, q, TSConfigUnaccent)
}

func quoteIdent(s string) string {
	out := `"`
	for _, r := range s {
		if r == '"' {
			out += `""`
		} else {
			out += string(r)
		}
	}
	return out + `"`
}
