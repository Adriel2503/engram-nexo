package store

// Nexo IA · NX-10 entrega 2b — migraciones aditivas e idempotentes (corren en
// la misma transacción de migraciones Nexo, con advisory lock y la cuenta
// dueña). Ninguna tabla nueva referencia tablas de proyectos de Engram; solo
// nexo_kb_* (y cloud_principals por id sin FK en las de solo inserción).

import (
	"context"
	"database/sql"
	"fmt"
)

var migrationQueries2b = []string{
	// RC-05: hash por sección (reutilizar índices derivados de lo que no
	// cambió y comparar secciones entre versiones, RC-27). Las filas de 2a
	// quedan NULL y se comparan con el hash calculado al vuelo.
	`ALTER TABLE nexo_kb_sections ADD COLUMN IF NOT EXISTS body_sha256 TEXT`,
	// RC-27: "qué cambió desde X" recorre las secciones por espacio y reloj.
	`CREATE INDEX IF NOT EXISTS idx_nexo_kb_sections_space_recorded ON nexo_kb_sections (space_id, recorded_from)`,
	`CREATE INDEX IF NOT EXISTS idx_nexo_kb_sections_space_valid ON nexo_kb_sections (space_id, valid_from)`,
	// C-09 feed: secciones vigentes o programadas de un documento.
	`CREATE INDEX IF NOT EXISTS idx_nexo_kb_sections_doc_open ON nexo_kb_sections (document_id) WHERE recorded_until IS NULL`,

	// KP-08: propuestas con vigencia; etiquetas.
	`ALTER TABLE nexo_kb_proposals ADD COLUMN IF NOT EXISTS valid_from TIMESTAMPTZ`,
	`ALTER TABLE nexo_kb_proposals ADD COLUMN IF NOT EXISTS valid_until TIMESTAMPTZ`,
	`ALTER TABLE nexo_kb_proposals ADD COLUMN IF NOT EXISTS tags TEXT[] NOT NULL DEFAULT '{}'`,
	`ALTER TABLE nexo_kb_proposals ADD COLUMN IF NOT EXISTS reviewed_by_email TEXT`,
	`ALTER TABLE nexo_kb_proposals ADD COLUMN IF NOT EXISTS proposed_by_ip TEXT`,

	// KP-02: registro de ejecuciones de publicación (lotes y subidas). Solo
	// inserción: una fila al terminar cada ejecución.
	`CREATE TABLE IF NOT EXISTS nexo_kb_publish_runs (
		id              UUID PRIMARY KEY,
		space_id        UUID NOT NULL REFERENCES nexo_kb_spaces(id),
		space_slug      TEXT NOT NULL,
		kind            TEXT NOT NULL CHECK (kind IN ('batch', 'upload', 'proposal')),
		dry_run         BOOLEAN NOT NULL DEFAULT FALSE,
		started_at      TIMESTAMPTZ NOT NULL,
		finished_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		source          TEXT,
		origin          JSONB NOT NULL DEFAULT '{}'::jsonb,
		upload          JSONB,
		summary         JSONB NOT NULL DEFAULT '{}'::jsonb,
		rejected        JSONB,
		pruned          INT NOT NULL DEFAULT 0,
		prune_skipped   TEXT,
		skipped         INT NOT NULL DEFAULT 0,
		actor_principal_id BIGINT,
		actor_token_id     BIGINT,
		actor_label        TEXT NOT NULL,
		client_ip       TEXT,
		user_agent      TEXT
	)`,
	`CREATE INDEX IF NOT EXISTS idx_nexo_kb_publish_runs_space ON nexo_kb_publish_runs (space_id, finished_at DESC)`,

	// RC-28: corridas de evaluación de la búsqueda. Solo inserción.
	`CREATE TABLE IF NOT EXISTS nexo_kb_eval_runs (
		id                  UUID PRIMARY KEY,
		ran_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		retriever           TEXT NOT NULL,
		mode                TEXT NOT NULL DEFAULT 'fts',
		index_version       INT,
		config              JSONB NOT NULL DEFAULT '{}'::jsonb,
		config_sha256       TEXT NOT NULL,
		question_set_sha256 TEXT NOT NULL,
		question_set_name   TEXT,
		spaces              TEXT[] NOT NULL DEFAULT '{}',
		accepted            BOOLEAN NOT NULL DEFAULT FALSE,
		metrics             JSONB NOT NULL,
		by_class            JSONB NOT NULL DEFAULT '{}'::jsonb,
		results             JSONB,
		commit_ref          TEXT,
		note                TEXT,
		actor_principal_id  BIGINT,
		actor_token_id      BIGINT,
		actor_label         TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_nexo_kb_eval_runs_set ON nexo_kb_eval_runs (question_set_sha256, ran_at DESC)`,

	`DO $$ BEGIN
		IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'nexo_kb_publish_runs_no_update_delete' AND tgrelid = 'nexo_kb_publish_runs'::regclass) THEN
			CREATE TRIGGER nexo_kb_publish_runs_no_update_delete BEFORE UPDATE OR DELETE ON nexo_kb_publish_runs
				FOR EACH ROW EXECUTE FUNCTION nexo_kb_immutable();
		END IF;
		IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'nexo_kb_eval_runs_no_update_delete' AND tgrelid = 'nexo_kb_eval_runs'::regclass) THEN
			CREATE TRIGGER nexo_kb_eval_runs_no_update_delete BEFORE UPDATE OR DELETE ON nexo_kb_eval_runs
				FOR EACH ROW EXECUTE FUNCTION nexo_kb_immutable();
		END IF;
	END $$`,
}

// migrate2b aplica las migraciones de la entrega 2b.
func migrate2b(ctx context.Context, tx *sql.Tx) error {
	for _, q := range migrationQueries2b {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("kb migrate 2b: %w", err)
		}
	}
	return nil
}
