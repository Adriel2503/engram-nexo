package store

// Nexo IA · NX-10 R2 — vectores por sección y cola de vectorización
// (design §20; RC-05, RC-11, RC-16, RC-19, RC-20, RC-23).
//
// Migración aditiva e idempotente (misma transacción de migraciones Nexo, con
// advisory lock y la cuenta dueña):
//
//   - CREATE EXTENSION vector, tolerante: si la extensión no está instalada
//     en el servidor o la cuenta no tiene permiso, la migración sigue y el
//     servidor funciona solo con texto (R2 es una mejora opcional).
//   - nexo_kb_section_vectors: un vector por sección y versión de índice
//     (vector sin dimensión fija + columna dims), con el hash del texto
//     vectorizado para reutilizar vectores de secciones idénticas (RC-05).
//   - Índices HNSW parciales por dimensión sobre la expresión
//     embedding::vector(D) (patrón de WeKnora 000002/000059): la consulta usa
//     la misma expresión o el planificador no usa el índice. Se crean aquí y
//     no al crear una versión de índice porque CREATE INDEX exige ser dueño
//     de la tabla y, con dos cuentas (NX-07), la aplicación no lo es.
//   - nexo_kb_index_jobs (cola, FOR UPDATE SKIP LOCKED) y nexo_kb_index_dead
//     (cartas muertas con la carga intacta), diseño de WeKnora 000041 sin
//     asynq ni Redis.
//
// Las versiones de índice vectorial viven en nexo_kb_index_versions con
// index_kind = 'vector' (proveedor, modelo, dimensiones y composición del
// texto en config) y feed_seq (último cambio de nexo_kb_changes encolado).

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/embed"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/ingest"
)

// IndexKindVector es el índice vectorial en nexo_kb_index_versions.
const IndexKindVector = "vector"

// VectorTextVersion identifica la composición del texto vectorizado (RC-03:
// ruta de títulos, título del documento y ruta de la sección, luego el
// cuerpo). Cambiarla es una versión de índice nueva.
const VectorTextVersion = "ctx1"

// HNSWDims son las dimensiones con índice HNSW creado por la migración
// (Gemini y OpenAI 768/1536, bge-m3 1024, 3072 nativa de gemini-embedding-001
// y text-embedding-3-large), más la configurada en NEXO_KB_EMBED_DIMS. Hasta
// 2000 dimensiones el índice es sobre embedding::vector(D); de 2001 a 4000,
// sobre embedding::halfvec(D) (el tipo vector no se indexa con HNSW por
// encima de 2000; halfvec sí, hasta 4000, con media precisión).
var HNSWDims = []int{768, 1024, 1536, 3072}

var migrationQueriesR2 = []string{
	`ALTER TABLE nexo_kb_index_versions ADD COLUMN IF NOT EXISTS feed_seq BIGINT`,
	`CREATE TABLE IF NOT EXISTS nexo_kb_index_jobs (
		id            BIGSERIAL PRIMARY KEY,
		index_version INT NOT NULL,
		section_id    UUID NOT NULL,
		space_id      UUID NOT NULL,
		priority      SMALLINT NOT NULL DEFAULT 0,
		enqueued_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		claimed_at    TIMESTAMPTZ,
		claimed_by    TEXT,
		fail_count    INT NOT NULL DEFAULT 0,
		last_error    TEXT,
		UNIQUE (index_version, section_id)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_nexo_kb_index_jobs_next ON nexo_kb_index_jobs (index_version, priority, id)`,
	`CREATE TABLE IF NOT EXISTS nexo_kb_index_dead (
		id            BIGSERIAL PRIMARY KEY,
		index_version INT NOT NULL,
		section_id    UUID NOT NULL,
		space_id      UUID NOT NULL,
		payload       JSONB NOT NULL DEFAULT '{}'::jsonb,
		last_error    TEXT NOT NULL DEFAULT '',
		fail_count    INT NOT NULL,
		failed_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,
	`CREATE INDEX IF NOT EXISTS idx_nexo_kb_index_dead_version ON nexo_kb_index_dead (index_version, failed_at DESC)`,
}

// migrateR2 aplica la migración de R2. Nunca falla por falta de pgvector.
func migrateR2(ctx context.Context, tx *sql.Tx) error {
	for _, q := range migrationQueriesR2 {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("kb migrate r2: %w", err)
		}
	}
	schema, version, err := ensureVectorExtension(ctx, tx)
	if err != nil {
		return fmt.Errorf("kb migrate r2: %w", err)
	}
	if schema == "" {
		return nil
	}
	vs := quoteIdent(schema)
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS nexo_kb_section_vectors (
		index_version INT NOT NULL,
		section_id    UUID NOT NULL REFERENCES nexo_kb_sections(id),
		space_id      UUID NOT NULL,
		text_sha256   TEXT NOT NULL,
		dims          INT NOT NULL CHECK (dims > 0),
		embedding     `+vs+`.vector NOT NULL,
		created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		PRIMARY KEY (index_version, section_id)
	)`); err != nil {
		return fmt.Errorf("kb migrate r2: vectors: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_nexo_kb_section_vectors_hash ON nexo_kb_section_vectors (index_version, text_sha256)`); err != nil {
		return fmt.Errorf("kb migrate r2: %w", err)
	}
	dims := slices.Clone(HNSWDims)
	if c, err := embed.ConfigFromEnv(); err == nil && c.Enabled() && !slices.Contains(dims, c.Dims) {
		dims = append(dims, c.Dims)
	}
	for _, d := range dims {
		if d <= 0 || d > embed.MaxDims {
			continue
		}
		typ := embed.StorageType(d)
		q := fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_nexo_kb_section_vectors_hnsw_%d ON nexo_kb_section_vectors
			USING hnsw ((embedding::%s.%s(%d)) %s.%s_cosine_ops) WITH (m = 16, ef_construction = 64) WHERE dims = %d`, d, vs, typ, d, vs, typ, d)
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("kb migrate r2: hnsw %d: %w", d, err)
		}
	}
	log.Printf("[nexo] kb: pgvector %s disponible (esquema %s); índices HNSW para %v dimensiones", version, schema, dims)
	return nil
}

// ensureVectorExtension devuelve el esquema y la versión de la extensión
// vector, creándola si está disponible y la cuenta puede. "" = no hay.
func ensureVectorExtension(ctx context.Context, tx *sql.Tx) (string, string, error) {
	schema, version, err := vectorExtension(ctx, tx)
	if err != nil || schema != "" {
		return schema, version, err
	}
	var available bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_available_extensions WHERE name = 'vector')`).Scan(&available); err != nil {
		return "", "", err
	}
	if !available {
		log.Printf("[nexo] kb: la extensión vector (pgvector) no está instalada en el servidor de Postgres; búsqueda solo por texto")
		return "", "", nil
	}
	if _, err := tx.ExecContext(ctx, `SAVEPOINT nexo_kb_vector`); err != nil {
		return "", "", err
	}
	stmt := `CREATE EXTENSION IF NOT EXISTS vector`
	var hasPublic bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = 'public') AND has_schema_privilege('public', 'CREATE')`).Scan(&hasPublic); err == nil && hasPublic {
		stmt += ` SCHEMA public`
	}
	if _, err := tx.ExecContext(ctx, stmt); err != nil {
		if _, rerr := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT nexo_kb_vector`); rerr != nil {
			return "", "", rerr
		}
		// Otra sesión pudo crearla a la vez (pruebas en paralelo).
		if s, v, err2 := vectorExtension(ctx, tx); err2 == nil && s != "" {
			return s, v, nil
		}
		log.Printf("[nexo] kb: no se pudo crear la extensión vector (%v); búsqueda solo por texto. Créela con una cuenta con permiso: CREATE EXTENSION vector", err)
		return "", "", nil
	}
	if _, err := tx.ExecContext(ctx, `RELEASE SAVEPOINT nexo_kb_vector`); err != nil {
		return "", "", err
	}
	return vectorExtension(ctx, tx)
}

func vectorExtension(ctx context.Context, q queryRower) (string, string, error) {
	var schema, version string
	err := q.QueryRowContext(ctx, `SELECT n.nspname, e.extversion FROM pg_extension e JOIN pg_namespace n ON n.oid = e.extnamespace WHERE e.extname = 'vector'`).Scan(&schema, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	return schema, version, err
}

// ─── Disponibilidad ──────────────────────────────────────────────────────────

// VectorSupport describe pgvector en la base.
type VectorSupport struct {
	Available bool   `json:"available"`
	Schema    string `json:"schema,omitempty"`
	Version   string `json:"version,omitempty"`
	// Iterative: pgvector >= 0.8 admite hnsw.iterative_scan.
	Iterative bool `json:"iterative_scan"`
}

// VectorSupport consulta (y guarda) si la base tiene pgvector y la tabla de
// vectores. Una base sin pgvector se vuelve a consultar cada 5 minutos (la
// extensión se puede instalar con el servidor en marcha y reiniciar).
func (s *Store) VectorSupport(ctx context.Context) (VectorSupport, error) {
	s.modeMu.Lock()
	if s.vec != nil && (s.vec.Available || time.Since(s.vecAt) < 5*time.Minute) {
		v := *s.vec
		s.modeMu.Unlock()
		return v, nil
	}
	s.modeMu.Unlock()
	var vs VectorSupport
	schema, version, err := vectorExtension(ctx, s.DB)
	if err != nil {
		return vs, err
	}
	var table sql.NullString
	if err := s.DB.QueryRowContext(ctx, `SELECT to_regclass('nexo_kb_section_vectors')::text`).Scan(&table); err != nil {
		return vs, err
	}
	if schema != "" && table.Valid {
		vs = VectorSupport{Available: true, Schema: schema, Version: version, Iterative: versionAtLeast(version, 0, 8)}
	}
	s.modeMu.Lock()
	s.vec, s.vecAt = &vs, time.Now()
	s.modeMu.Unlock()
	return vs, nil
}

func versionAtLeast(v string, major, minor int) bool {
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return false
	}
	ma, err1 := strconv.Atoi(parts[0])
	mi, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return false
	}
	return ma > major || (ma == major && mi >= minor)
}

// ─── Versiones del índice vectorial (RC-16) ──────────────────────────────────

// VectorIndex es una versión del índice vectorial.
type VectorIndex struct {
	Version   int            `json:"version"`
	Status    string         `json:"status"`
	Signature string         `json:"signature"`
	Provider  string         `json:"provider"`
	Model     string         `json:"model"`
	Dims      int            `json:"dims"`
	Config    map[string]any `json:"config"`
	FeedSeq   int64          `json:"feed_seq"`
	BuiltAt   *time.Time     `json:"built_at,omitempty"`
}

// VectorSignature identifica un índice: proveedor, modelo, dimensiones,
// prefijos y composición del texto. Dos configuraciones con la misma firma
// producen vectores comparables.
func VectorSignature(c embed.Config) string {
	return strings.Join([]string{c.Provider, c.Model, strconv.Itoa(c.Dims), c.DocPrefix, c.QueryPrefix, VectorTextVersion}, "|")
}

func scanVectorIndex(row interface{ Scan(...any) error }) (VectorIndex, error) {
	var vi VectorIndex
	var raw string
	var seq sql.NullInt64
	var built sql.NullTime
	if err := row.Scan(&vi.Version, &vi.Status, &raw, &seq, &built); err != nil {
		return vi, err
	}
	vi.Config = map[string]any{}
	_ = json.Unmarshal([]byte(raw), &vi.Config)
	vi.Signature, _ = vi.Config["signature"].(string)
	vi.Provider, _ = vi.Config["provider"].(string)
	vi.Model, _ = vi.Config["model"].(string)
	if d, ok := vi.Config["dims"].(float64); ok {
		vi.Dims = int(d)
	}
	vi.FeedSeq = seq.Int64
	vi.BuiltAt = timePtr(built)
	return vi, nil
}

const vectorIndexColumns = `version, status, config::text, feed_seq, built_at`

// VectorIndexes lista las versiones activas y en construcción.
func (s *Store) VectorIndexes(ctx context.Context) ([]VectorIndex, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+vectorIndexColumns+` FROM nexo_kb_index_versions
		WHERE index_kind = $1 AND status IN ('active', 'building') ORDER BY version`, IndexKindVector)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []VectorIndex
	for rows.Next() {
		vi, err := scanVectorIndex(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, vi)
	}
	return out, rows.Err()
}

// ActiveVectorIndex devuelve la versión activa (la única que responde
// consultas: nunca se mezclan modelos, RC-23).
func (s *Store) ActiveVectorIndex(ctx context.Context) (VectorIndex, bool, error) {
	vi, err := scanVectorIndex(s.DB.QueryRowContext(ctx, `SELECT `+vectorIndexColumns+` FROM nexo_kb_index_versions
		WHERE index_kind = $1 AND status = 'active' ORDER BY version DESC LIMIT 1`, IndexKindVector))
	if errors.Is(err, sql.ErrNoRows) {
		return VectorIndex{}, false, nil
	}
	return vi, err == nil, err
}

// EnsureVectorIndex devuelve la versión activa o en construcción con la
// firma de la configuración; si no hay, registra una nueva en construcción
// (force: siempre una nueva, para reconstruir con la misma configuración) y
// encola todas las secciones. Las otras versiones en construcción se
// retiran (su trabajo queda superado).
func (s *Store) EnsureVectorIndex(ctx context.Context, c embed.Config, force bool) (VectorIndex, bool, error) {
	sig := VectorSignature(c)
	if !force {
		vis, err := s.VectorIndexes(ctx)
		if err != nil {
			return VectorIndex{}, false, err
		}
		for _, vi := range vis {
			if vi.Signature == sig {
				return vi, false, nil
			}
		}
	}
	cfg := c.Describe()
	cfg["signature"], cfg["text"], cfg["metric"] = sig, VectorTextVersion, "cosine"
	raw, _ := json.Marshal(cfg)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return VectorIndex{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	// Serializa la creación entre réplicas.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('nexo_kb_vector_index'))`); err != nil {
		return VectorIndex{}, false, err
	}
	if !force {
		var exists int
		err := tx.QueryRowContext(ctx, `SELECT version FROM nexo_kb_index_versions WHERE index_kind = $1 AND status IN ('active','building') AND config->>'signature' = $2 LIMIT 1`, IndexKindVector, sig).Scan(&exists)
		if err == nil {
			_ = tx.Rollback()
			vi, _, err := s.findVectorIndex(ctx, exists)
			return vi, false, err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return VectorIndex{}, false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE nexo_kb_index_versions SET status = 'retired', updated_at = now() WHERE index_kind = $1 AND status = 'building'`, IndexKindVector); err != nil {
		return VectorIndex{}, false, err
	}
	var version int
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO nexo_kb_index_versions (index_kind, version, config, status, feed_seq)
		SELECT $1, COALESCE((SELECT max(version) FROM nexo_kb_index_versions WHERE index_kind = $1), 0) + 1, $2::jsonb, 'building',
			(SELECT COALESCE(max(seq), 0) FROM nexo_kb_changes)
		RETURNING version`, IndexKindVector, string(raw)).Scan(&version); err != nil {
		return VectorIndex{}, false, err
	}
	if _, err := tx.ExecContext(ctx, enqueueSQL+` ON CONFLICT (index_version, section_id) DO NOTHING`, version); err != nil {
		return VectorIndex{}, false, fmt.Errorf("kb vectors: encolar: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return VectorIndex{}, false, err
	}
	vi, _, err := s.findVectorIndex(ctx, version)
	return vi, true, err
}

func (s *Store) findVectorIndex(ctx context.Context, version int) (VectorIndex, bool, error) {
	vi, err := scanVectorIndex(s.DB.QueryRowContext(ctx, `SELECT `+vectorIndexColumns+` FROM nexo_kb_index_versions WHERE index_kind = $1 AND version = $2`, IndexKindVector, version))
	if errors.Is(err, sql.ErrNoRows) {
		return VectorIndex{}, false, nil
	}
	return vi, err == nil, err
}

// enqueueSQL encola las secciones sin vector en la versión $1: primero lo
// vigente (prioridad 0), después lo histórico (1). Se omiten las secciones
// que nunca estuvieron publicadas (recorded_until = recorded_from: retiradas
// en la misma carga, D-K2b-5). Los documentos rechazados por secretos nunca
// llegan a tener secciones. $2 (opcional) acota a unos documentos.
const enqueueSQL = `
	INSERT INTO nexo_kb_index_jobs (index_version, section_id, space_id, priority)
	SELECT $1, s.id, s.space_id, CASE WHEN s.is_current OR s.recorded_until IS NULL THEN 0 ELSE 1 END
	FROM nexo_kb_sections s
	WHERE (s.recorded_until IS NULL OR s.recorded_until > s.recorded_from)
	  AND NOT EXISTS (SELECT 1 FROM nexo_kb_section_vectors v WHERE v.index_version = $1 AND v.section_id = s.id)
	  AND NOT EXISTS (SELECT 1 FROM nexo_kb_index_dead x WHERE x.index_version = $1 AND x.section_id = s.id)
	ORDER BY 4, s.id`

const enqueueDocsSQL = `
	INSERT INTO nexo_kb_index_jobs (index_version, section_id, space_id, priority)
	SELECT $1, s.id, s.space_id, CASE WHEN s.is_current OR s.recorded_until IS NULL THEN 0 ELSE 1 END
	FROM nexo_kb_sections s
	WHERE s.document_id = ANY($2::uuid[])
	  AND (s.recorded_until IS NULL OR s.recorded_until > s.recorded_from)
	  AND NOT EXISTS (SELECT 1 FROM nexo_kb_section_vectors v WHERE v.index_version = $1 AND v.section_id = s.id)
	  AND NOT EXISTS (SELECT 1 FROM nexo_kb_index_dead x WHERE x.index_version = $1 AND x.section_id = s.id)
	ON CONFLICT (index_version, section_id) DO NOTHING`

// EnqueueChanges lee nexo_kb_changes desde feed_seq de la versión y encola
// las secciones sin vector de los documentos cambiados (idempotente). Relee
// además los cambios de los últimos 5 minutos: un cambio con seq menor
// confirmado después de leer el máximo (D-K2b-17) se encola en la pasada
// siguiente. Devuelve cuántos trabajos nuevos encoló.
func (s *Store) EnqueueChanges(ctx context.Context, version int) (int64, error) {
	var feed int64
	if err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(feed_seq, 0) FROM nexo_kb_index_versions WHERE index_kind = $1 AND version = $2`, IndexKindVector, version).Scan(&feed); err != nil {
		return 0, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT document_id::text, max(seq) AS m FROM nexo_kb_changes
		WHERE document_id IS NOT NULL AND (seq > $1 OR occurred_at > now() - interval '5 minutes')
		GROUP BY document_id ORDER BY m LIMIT 5000`, feed)
	if err != nil {
		return 0, err
	}
	var docs []string
	maxSeq := feed
	for rows.Next() {
		var d string
		var m int64
		if err := rows.Scan(&d, &m); err != nil {
			_ = rows.Close()
			return 0, err
		}
		docs = append(docs, d)
		maxSeq = max(maxSeq, m)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if len(docs) == 0 {
		return 0, nil
	}
	res, err := s.DB.ExecContext(ctx, enqueueDocsSQL, version, docs)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if maxSeq > feed {
		if _, err := s.DB.ExecContext(ctx, `UPDATE nexo_kb_index_versions SET feed_seq = GREATEST(COALESCE(feed_seq, 0), $3), updated_at = now() WHERE index_kind = $1 AND version = $2`, IndexKindVector, version, maxSeq); err != nil {
			return n, err
		}
	}
	return n, nil
}

// ─── Trabajos ────────────────────────────────────────────────────────────────

// VectorJob es una sección por vectorizar con su texto (RC-03).
type VectorJob struct {
	ID        int64
	SectionID string
	SpaceID   string
	FailCount int
	Text      string
	TextHash  string
}

// ClaimVectorJobs toma hasta n trabajos de la versión (FOR UPDATE SKIP
// LOCKED; un trabajo tomado hace más de stale se puede volver a tomar) con
// el texto a vectorizar.
func (s *Store) ClaimVectorJobs(ctx context.Context, version, n int, worker string, stale time.Duration) ([]VectorJob, error) {
	rows, err := s.DB.QueryContext(ctx, `
		WITH c AS (
			SELECT id FROM nexo_kb_index_jobs
			WHERE index_version = $1 AND (claimed_at IS NULL OR claimed_at < now() - make_interval(secs => $4))
			ORDER BY priority, id LIMIT $2 FOR UPDATE SKIP LOCKED
		)
		UPDATE nexo_kb_index_jobs j SET claimed_at = now(), claimed_by = $3 FROM c WHERE j.id = c.id
		RETURNING j.id, j.section_id::text, j.space_id::text, j.fail_count`, version, n, worker, stale.Seconds())
	if err != nil {
		return nil, err
	}
	var jobs []VectorJob
	ids := []string{}
	for rows.Next() {
		var j VectorJob
		if err := rows.Scan(&j.ID, &j.SectionID, &j.SpaceID, &j.FailCount); err != nil {
			_ = rows.Close()
			return nil, err
		}
		jobs = append(jobs, j)
		ids = append(ids, j.SectionID)
	}
	if err := rows.Close(); err != nil || len(jobs) == 0 {
		return jobs, err
	}
	texts, err := s.vectorTexts(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range jobs {
		jobs[i].Text = texts[jobs[i].SectionID]
		jobs[i].TextHash = ingest.SHA256Hex(jobs[i].Text)
	}
	return jobs, nil
}

func (s *Store) vectorTexts(ctx context.Context, sectionIDs []string) (map[string]string, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT s.id::text, array_to_json(s.heading_path)::text, s.body, v.title, COALESCE(v.origin->'path_titles', '[]'::jsonb)::text
		FROM nexo_kb_sections s JOIN nexo_kb_versions v ON v.id = s.version_id
		WHERE s.id = ANY($1::uuid[])`, sectionIDs)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var id, hp, body, title, pt string
		if err := rows.Scan(&id, &hp, &body, &title, &pt); err != nil {
			return nil, err
		}
		var heading, pathTitles []string
		_ = json.Unmarshal([]byte(hp), &heading)
		_ = json.Unmarshal([]byte(pt), &pathTitles)
		out[id] = VectorText(title, pathTitles, heading, body)
	}
	return out, rows.Err()
}

// VectorText es el texto que se vectoriza (RC-03): una línea de contexto con
// la ruta de títulos de la carpeta, el título del documento y la ruta de la
// sección (sin repetir títulos consecutivos), y el cuerpo.
func VectorText(title string, pathTitles, headingPath []string, body string) string {
	var parts []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p != "" && (len(parts) == 0 || !strings.EqualFold(parts[len(parts)-1], p)) {
			parts = append(parts, p)
		}
	}
	for _, p := range pathTitles {
		add(p)
	}
	add(title)
	for _, h := range headingPath {
		add(h)
	}
	body = strings.TrimSpace(body)
	if len(parts) == 0 {
		return body
	}
	return strings.Join(parts, " > ") + "\n\n" + body
}

// ReuseVectors copia, dentro de la versión, el vector de otra sección con el
// mismo texto (RC-05: una versión nueva de un documento solo vectoriza lo que
// cambió). Devuelve los trabajos resueltos (ya borrados de la cola).
func (s *Store) ReuseVectors(ctx context.Context, version int, jobs []VectorJob) (map[int64]bool, error) {
	if len(jobs) == 0 {
		return map[int64]bool{}, nil
	}
	ids, secs, spaces, hashes := make([]int64, len(jobs)), make([]string, len(jobs)), make([]string, len(jobs)), make([]string, len(jobs))
	for i, j := range jobs {
		ids[i], secs[i], spaces[i], hashes[i] = j.ID, j.SectionID, j.SpaceID, j.TextHash
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `
		WITH x AS (SELECT * FROM unnest($2::bigint[], $3::uuid[], $4::uuid[], $5::text[]) AS x(job, section_id, space_id, h)),
		ins AS (
			INSERT INTO nexo_kb_section_vectors (index_version, section_id, space_id, text_sha256, dims, embedding)
			SELECT $1, x.section_id, x.space_id, x.h, src.dims, src.embedding
			FROM x JOIN LATERAL (SELECT v.dims, v.embedding FROM nexo_kb_section_vectors v
				WHERE v.index_version = $1 AND v.text_sha256 = x.h LIMIT 1) src ON true
			ON CONFLICT (index_version, section_id) DO NOTHING
			RETURNING section_id
		)
		SELECT x.job FROM x WHERE x.section_id IN (SELECT section_id FROM ins)
			OR EXISTS (SELECT 1 FROM nexo_kb_section_vectors v WHERE v.index_version = $1 AND v.section_id = x.section_id)`,
		version, ids, secs, spaces, hashes)
	if err != nil {
		return nil, err
	}
	done := map[int64]bool{}
	var doneIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		done[id] = true
		doneIDs = append(doneIDs, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(doneIDs) > 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM nexo_kb_index_jobs WHERE id = ANY($1::bigint[])`, doneIDs); err != nil {
			return nil, err
		}
	}
	return done, tx.Commit()
}

// SaveVectors guarda los vectores y borra sus trabajos (una transacción).
func (s *Store) SaveVectors(ctx context.Context, version int, jobs []VectorJob, vecs [][]float32) error {
	if len(jobs) != len(vecs) {
		return fmt.Errorf("kb vectors: %d trabajos y %d vectores", len(jobs), len(vecs))
	}
	if len(jobs) == 0 {
		return nil
	}
	vs, err := s.VectorSupport(ctx)
	if err != nil || !vs.Available {
		return fmt.Errorf("kb vectors: pgvector no disponible")
	}
	ids, secs, spaces, hashes, lits := make([]int64, len(jobs)), make([]string, len(jobs)), make([]string, len(jobs)), make([]string, len(jobs)), make([]string, len(jobs))
	dims := make([]int64, len(jobs))
	for i, j := range jobs {
		ids[i], secs[i], spaces[i], hashes[i], lits[i], dims[i] = j.ID, j.SectionID, j.SpaceID, j.TextHash, VectorLiteral(vecs[i]), int64(len(vecs[i]))
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO nexo_kb_section_vectors (index_version, section_id, space_id, text_sha256, dims, embedding)
		SELECT $1, x.section_id, x.space_id, x.h, x.d, x.e::`+quoteIdent(vs.Schema)+`.vector
		FROM unnest($2::uuid[], $3::uuid[], $4::text[], $5::bigint[], $6::text[]) AS x(section_id, space_id, h, d, e)
		ON CONFLICT (index_version, section_id) DO NOTHING`, version, secs, spaces, hashes, dims, lits); err != nil {
		return fmt.Errorf("kb vectors: guardar: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM nexo_kb_index_jobs WHERE id = ANY($1::bigint[])`, ids); err != nil {
		return err
	}
	return tx.Commit()
}

// ReleaseVectorJobs devuelve los trabajos a la cola sin contar un fallo (el
// proveedor no respondió: no es culpa del trabajo).
func (s *Store) ReleaseVectorJobs(ctx context.Context, ids []int64) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE nexo_kb_index_jobs SET claimed_at = NULL, claimed_by = NULL WHERE id = ANY($1::bigint[])`, ids)
	return err
}

// FailVectorJobs suma un fallo a cada trabajo y lo deja tomado: se vuelve a
// intentar cuando vence el umbral de abandono (espera entre intentos). Los
// que llegan a maxFail pasan
// a nexo_kb_index_dead con la carga intacta y dejan la alerta
// kb_index_dead_letter en nexo_alerts (si la tabla existe). Devuelve cuántos
// pasaron a cartas muertas.
func (s *Store) FailVectorJobs(ctx context.Context, ids []int64, cause string, maxFail int) (int, error) {
	cause = truncateText(cause, 1000)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE nexo_kb_index_jobs SET fail_count = fail_count + 1, last_error = $2, claimed_at = now() WHERE id = ANY($1::bigint[])`, ids, cause); err != nil {
		return 0, err
	}
	rows, err := tx.QueryContext(ctx, `
		WITH d AS (DELETE FROM nexo_kb_index_jobs WHERE id = ANY($1::bigint[]) AND fail_count >= $2
			RETURNING index_version, section_id, space_id, fail_count, last_error, enqueued_at, priority)
		INSERT INTO nexo_kb_index_dead (index_version, section_id, space_id, payload, last_error, fail_count)
		SELECT index_version, section_id, space_id, jsonb_build_object('priority', priority, 'enqueued_at', enqueued_at), COALESCE(last_error, ''), fail_count FROM d
		RETURNING index_version, section_id::text`, ids, maxFail)
	if err != nil {
		return 0, err
	}
	type dead struct {
		v   int
		sec string
	}
	var deads []dead
	for rows.Next() {
		var d dead
		if err := rows.Scan(&d.v, &d.sec); err != nil {
			_ = rows.Close()
			return 0, err
		}
		deads = append(deads, d)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	for _, d := range deads {
		s.deadLetterAlert(ctx, d.v, d.sec, cause)
	}
	return len(deads), nil
}

// deadLetterAlert deja la alerta en nexo_alerts (S4) si la tabla existe; una
// por sección y versión (dedup_key). Mejor esfuerzo.
func (s *Store) deadLetterAlert(ctx context.Context, version int, section, cause string) {
	var exists bool
	if err := s.DB.QueryRowContext(ctx, `SELECT to_regclass('nexo_alerts') IS NOT NULL`).Scan(&exists); err != nil || !exists {
		return
	}
	details, _ := json.Marshal(map[string]any{"index_version": version, "section_id": section, "error": cause})
	_, err := s.DB.ExecContext(ctx, `INSERT INTO nexo_alerts (rule, severity, layer, subject, dedup_key, summary, details)
		VALUES ('kb_index_dead_letter', 'warning', 'conocimiento', $1, $2, $3, $4::jsonb) ON CONFLICT (dedup_key) DO NOTHING`,
		"vector:"+strconv.Itoa(version), "kb_index_dead_letter:"+strconv.Itoa(version)+":"+section,
		"Una sección no se pudo vectorizar tras varios intentos; se encuentra solo por texto (nexo_kb_index_dead)", string(details))
	if err != nil {
		log.Printf("[nexo] kb: alerta de carta muerta no registrada: %v", err)
	}
}

// RequeueDeadVectors vuelve a encolar las cartas muertas de una versión.
func (s *Store) RequeueDeadVectors(ctx context.Context, version int) (int64, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `
		WITH d AS (DELETE FROM nexo_kb_index_dead WHERE index_version = $1 RETURNING index_version, section_id, space_id, payload)
		INSERT INTO nexo_kb_index_jobs (index_version, section_id, space_id, priority)
		SELECT index_version, section_id, space_id, COALESCE((payload->>'priority')::smallint, 0) FROM d
		ON CONFLICT (index_version, section_id) DO NOTHING`, version)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, tx.Commit()
}

// TryActivateVectorIndex activa la versión en construcción si no le quedan
// trabajos pendientes (las cartas muertas no bloquean: esas secciones se
// encuentran solo por texto) y retira las demás versiones vectoriales.
func (s *Store) TryActivateVectorIndex(ctx context.Context, version int) (bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('nexo_kb_vector_index'))`); err != nil {
		return false, err
	}
	var pending bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM nexo_kb_index_jobs WHERE index_version = $1)`, version).Scan(&pending); err != nil || pending {
		return false, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE nexo_kb_index_versions SET status = 'active', built_at = now(), updated_at = now() WHERE index_kind = $1 AND version = $2 AND status = 'building'`, IndexKindVector, version)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE nexo_kb_index_versions SET status = 'retired', updated_at = now() WHERE index_kind = $1 AND version <> $2 AND status = 'active'`, IndexKindVector, version); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// PurgeRetiredVectors borra (por lotes) vectores y trabajos de versiones
// vectoriales retiradas: el trabajo superado se descarta.
func (s *Store) PurgeRetiredVectors(ctx context.Context, limit int) (int64, error) {
	var total int64
	res, err := s.DB.ExecContext(ctx, `DELETE FROM nexo_kb_index_jobs WHERE index_version IN (SELECT version FROM nexo_kb_index_versions WHERE index_kind = $1 AND status = 'retired')`, IndexKindVector)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	total += n
	vs, err := s.VectorSupport(ctx)
	if err != nil || !vs.Available {
		return total, err
	}
	res, err = s.DB.ExecContext(ctx, `DELETE FROM nexo_kb_section_vectors WHERE ctid IN (
		SELECT ctid FROM nexo_kb_section_vectors WHERE index_version IN (SELECT version FROM nexo_kb_index_versions WHERE index_kind = $1 AND status = 'retired') LIMIT $2)`, IndexKindVector, limit)
	if err != nil {
		return total, err
	}
	n, _ = res.RowsAffected()
	return total + n, nil
}

// VectorQueueStats es el estado de la cola de una versión (observación).
type VectorQueueStats struct {
	Version       int     `json:"version"`
	Status        string  `json:"status"`
	Provider      string  `json:"provider"`
	Model         string  `json:"model"`
	Dims          int     `json:"dims"`
	Vectors       int64   `json:"vectors"`
	Pending       int64   `json:"pending"`
	Claimed       int64   `json:"claimed"`
	Failing       int64   `json:"failing"`
	Dead          int64   `json:"dead"`
	OldestSeconds float64 `json:"oldest_pending_seconds"`
	FeedSeq       int64   `json:"feed_seq"`
	// FeedLag: cambios publicados que el indexador todavía no leyó (con
	// pending, la ventana entre publicar y tener vector).
	FeedLag int64 `json:"feed_lag"`
}

// VectorStats devuelve el estado de las versiones activas y en construcción.
func (s *Store) VectorStats(ctx context.Context) ([]VectorQueueStats, error) {
	vis, err := s.VectorIndexes(ctx)
	if err != nil {
		return nil, err
	}
	vs, err := s.VectorSupport(ctx)
	if err != nil {
		return nil, err
	}
	out := []VectorQueueStats{}
	var maxSeq int64
	if err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(max(seq), 0) FROM nexo_kb_changes`).Scan(&maxSeq); err != nil {
		return nil, err
	}
	for _, vi := range vis {
		st := VectorQueueStats{Version: vi.Version, Status: vi.Status, Provider: vi.Provider, Model: vi.Model, Dims: vi.Dims, FeedSeq: vi.FeedSeq}
		st.FeedLag = max(0, maxSeq-vi.FeedSeq)
		if err := s.DB.QueryRowContext(ctx, `SELECT count(*), count(*) FILTER (WHERE claimed_at IS NOT NULL), count(*) FILTER (WHERE fail_count > 0),
			COALESCE(EXTRACT(EPOCH FROM now() - min(enqueued_at)), 0) FROM nexo_kb_index_jobs WHERE index_version = $1`, vi.Version).
			Scan(&st.Pending, &st.Claimed, &st.Failing, &st.OldestSeconds); err != nil {
			return nil, err
		}
		if err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM nexo_kb_index_dead WHERE index_version = $1`, vi.Version).Scan(&st.Dead); err != nil {
			return nil, err
		}
		if vs.Available {
			if err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM nexo_kb_section_vectors WHERE index_version = $1`, vi.Version).Scan(&st.Vectors); err != nil {
				return nil, err
			}
		}
		out = append(out, st)
	}
	return out, nil
}

// PendingVectorJobs cuenta los trabajos pendientes de la versión en los
// espacios (index_lag de la respuesta híbrida).
func (s *Store) PendingVectorJobs(ctx context.Context, version int, spaceIDs []string) (int64, error) {
	var n int64
	err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT 1 FROM nexo_kb_index_jobs WHERE index_version = $1 AND space_id = ANY($2::uuid[]) LIMIT 10000) x`, version, spaceIDs).Scan(&n)
	return n, err
}

// VectorLiteral es el texto de un vector para pgvector ('[0.1,0.2,...]').
func VectorLiteral(v []float32) string {
	b := make([]byte, 0, len(v)*10+2)
	b = append(b, '[')
	for i, f := range v {
		if i > 0 {
			b = append(b, ',')
		}
		b = strconv.AppendFloat(b, float64(f), 'g', -1, 32)
	}
	return string(append(b, ']'))
}

func truncateText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
