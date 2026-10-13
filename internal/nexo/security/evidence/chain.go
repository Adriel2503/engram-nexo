// Package evidence implementa la fase S4 de NX-07 (evidencia y monitoreo,
// docs/nexo/seguridad/design.md §5 y §8.4): cadena de hashes en las tablas de
// auditoría, exportación firmada, alertas, revisión de accesos y retención
// por tabla con funciones SECURITY DEFINER.
//
// Como el resto de internal/nexo, no depende de cloudstore ni de cloudserver:
// recibe conexiones o transacciones ya abiertas. Las migraciones corren con la
// cuenta que migra (la dueña en el modo de dos cuentas) dentro de la
// transacción de migraciones Nexo; la aplicación sigue con SELECT e INSERT.
package evidence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ─── Tablas encadenadas ──────────────────────────────────────────────────────

// Layer es la capa de la que proviene un evento o una alerta. Solo hay dos
// abstracciones de producto (proyectos de Engram y espacios de conocimiento)
// más la plataforma (identidades, tokens, roles).
type Layer string

const (
	LayerPlatform  Layer = "plataforma"
	LayerProjects  Layer = "proyectos"
	LayerKnowledge Layer = "conocimiento"
)

// ChainSpec describe una tabla con cadena de hashes. Columns es la
// representación canónica: orden fijo, y una columna nueva de la tabla no
// cambia el hash de las filas existentes (solo entra si se agrega aquí, y eso
// sería una versión nueva del formato).
type ChainSpec struct {
	Table   string
	Layer   Layer
	Columns []string // sin id ni occurred_at (van siempre primero)
}

// ChainedTables son las tablas con cadena de hashes (RS-30). Las tres son de
// solo inserción para la cuenta de aplicación (security.EvidenceTables).
var ChainedTables = []ChainSpec{
	{Table: "cloud_auth_audit_log", Layer: LayerPlatform, Columns: []string{
		"actor_principal_id", "actor_source", "target_principal_id", "project", "action", "outcome", "reason_code", "metadata"}},
	{Table: "nexo_authz_events", Layer: LayerPlatform, Columns: []string{
		"action", "scope_type", "scope_key", "target_principal_id", "role_before", "role_after", "actor_principal_id",
		"actor_token_id", "actor_label", "actor_email", "actor_verified", "client_ip", "user_agent", "client_claim", "details"}},
	{Table: "nexo_kb_events", Layer: LayerKnowledge, Columns: []string{
		"space_id", "space_slug", "action", "target_kind", "target_id", "actor_principal_id", "actor_token_id",
		"actor_label", "actor_email", "actor_verified", "client_ip", "user_agent", "client_claim", "details"}},
}

// ChainSpecFor devuelve la especificación de una tabla encadenada.
func ChainSpecFor(table string) (ChainSpec, bool) {
	for _, s := range ChainedTables {
		if s.Table == table {
			return s, true
		}
	}
	return ChainSpec{}, false
}

// Tablas propias de la cadena.
const (
	// AnchorsTable: inicio de la cadena y primera fila conservada tras cada
	// depuración. Solo la escriben la migración y la función de depuración
	// (cuenta dueña); la aplicación solo la lee (security.OwnerManagedTables):
	// si pudiera escribirla, podría "justificar" un hueco con un ancla falsa.
	AnchorsTable = "nexo_audit_chain_anchors"
	// CheckpointsTable: puntos de control (posición y hash de la última fila)
	// que registra el monitor; detectan el borrado de las últimas filas.
	// Solo inserción para la aplicación (security.EvidenceTables).
	CheckpointsTable = "nexo_audit_chain_checkpoints"
)

// Tipos de ancla.
const (
	AnchorGenesis = "genesis" // inicio de la cadena: cubre las filas anteriores a la migración
	AnchorPurge   = "purge"   // depuración por retención: primera fila conservada
)

// ChainFormatVersion identifica la representación canónica.
const ChainFormatVersion = 1

func quoteIdent(name string) string { return pgx.Identifier{name}.Sanitize() }

func canonFunc(table string) string { return "nexo_chain_canon_" + table }
func linkFunc(table string) string  { return "nexo_chain_link_" + table }
func linkTrigger(table string) string {
	// Los triggers BEFORE se ejecutan por orden alfabético: "zz" deja la
	// cadena en último lugar, sobre la fila ya completa.
	return "zz_nexo_chain_" + table
}

// ChainLockKey es la expresión del advisory lock por tabla que serializa las
// inserciones encadenadas (y la depuración, que toca el inicio de la cadena).
func chainLockSQL(table string) string {
	return `pg_advisory_xact_lock(hashtext('nexo_chain:` + table + `'))`
}

func currentSchema(ctx context.Context, tx *sql.Tx) (string, error) {
	var schema sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		return "", fmt.Errorf("evidence: esquema actual: %w", err)
	}
	if !schema.Valid || schema.String == "" {
		return "", fmt.Errorf("evidence: no hay esquema actual (search_path vacío)")
	}
	return schema.String, nil
}

// migrateChain crea las tablas de la cadena, las columnas, las funciones
// canónicas, los triggers y el ancla inicial de cada tabla. Idempotente.
func migrateChain(ctx context.Context, tx *sql.Tx, schema string) error {
	s := quoteIdent(schema)
	base := []string{
		`CREATE TABLE IF NOT EXISTS ` + AnchorsTable + ` (
			id            BIGSERIAL PRIMARY KEY,
			occurred_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			table_name    TEXT NOT NULL,
			kind          TEXT NOT NULL CHECK (kind IN ('genesis', 'purge')),
			seq           BIGINT NOT NULL,
			prev_hash     BYTEA NOT NULL,
			legacy_rows   BIGINT NOT NULL DEFAULT 0,
			legacy_max_id BIGINT,
			purged_rows   BIGINT NOT NULL DEFAULT 0,
			details       JSONB
		)`,
		`CREATE INDEX IF NOT EXISTS idx_nexo_audit_chain_anchors_table ON ` + AnchorsTable + ` (table_name, seq)`,
		`CREATE TABLE IF NOT EXISTS ` + CheckpointsTable + ` (
			id          BIGSERIAL PRIMARY KEY,
			occurred_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			table_name  TEXT NOT NULL,
			seq         BIGINT NOT NULL,
			hash        BYTEA NOT NULL,
			source      TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_nexo_audit_chain_checkpoints_table ON ` + CheckpointsTable + ` (table_name, seq DESC)`,
		// Marca de tiempo canónica: UTC con microsegundos, sin depender de
		// TimeZone ni DateStyle de la sesión.
		`CREATE OR REPLACE FUNCTION nexo_chain_ts(t timestamptz) RETURNS text
		LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
			SELECT to_char(t AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')
		$$`,
	}
	for _, q := range base {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("evidence: cadena: %w", err)
		}
	}
	for _, spec := range ChainedTables {
		if err := migrateChainTable(ctx, tx, s, spec); err != nil {
			return err
		}
	}
	return nil
}

func migrateChainTable(ctx context.Context, tx *sql.Tx, schema string, spec ChainSpec) error {
	t := spec.Table
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, t).Scan(&exists); err != nil {
		return fmt.Errorf("evidence: cadena %s: %w", t, err)
	}
	if !exists {
		return nil // la tabla de su capa todavía no existe (no ocurre en el orden de migración actual)
	}
	cols := make([]string, 0, len(spec.Columns)+4)
	cols = append(cols, quoteLiteral(t), "r.nexo_chain_seq", "r.id", "nexo_chain_ts(r.occurred_at)")
	for _, c := range spec.Columns {
		cols = append(cols, "r."+quoteIdent(c))
	}
	stmts := []string{
		`ALTER TABLE ` + t + ` ADD COLUMN IF NOT EXISTS nexo_chain_seq BIGINT`,
		`ALTER TABLE ` + t + ` ADD COLUMN IF NOT EXISTS nexo_prev_hash BYTEA`,
		`ALTER TABLE ` + t + ` ADD COLUMN IF NOT EXISTS nexo_hash BYTEA`,
		// Única y parcial: la última fila encadenada se encuentra con un
		// recorrido de índice hacia atrás (las filas previas a la migración
		// quedan fuera).
		`CREATE UNIQUE INDEX IF NOT EXISTS ` + quoteIdent("uq_"+t+"_nexo_chain_seq") + ` ON ` + t + ` (nexo_chain_seq) WHERE nexo_chain_seq IS NOT NULL`,
		// Representación canónica: arreglo JSON con la tabla, la posición en
		// la cadena, el id, la hora UTC y las columnas en orden fijo (JSONB
		// normaliza el contenido de metadata y details).
		`CREATE OR REPLACE FUNCTION ` + canonFunc(t) + `(r ` + t + `) RETURNS text
		LANGUAGE sql STABLE PARALLEL SAFE AS $$
			SELECT jsonb_build_array(` + strings.Join(cols, ", ") + `)::text
		$$`,
		// Cadena: advisory lock por tabla; la fila anterior es la última
		// encadenada (o, con la tabla vacía, la última ancla); calcula
		// hash = sha256(prev_hash || sha256(canónica)). No escribe ninguna
		// otra tabla: la aplicación solo necesita INSERT y SELECT.
		`CREATE OR REPLACE FUNCTION ` + linkFunc(t) + `() RETURNS trigger
		LANGUAGE plpgsql SET search_path = ` + schema + `, pg_temp AS $$
		DECLARE
			v_seq  bigint;
			v_prev bytea;
		BEGIN
			PERFORM ` + chainLockSQL(t) + `;
			SELECT x.nexo_chain_seq + 1, x.nexo_hash INTO v_seq, v_prev
			FROM ` + t + ` x WHERE x.nexo_chain_seq IS NOT NULL ORDER BY x.nexo_chain_seq DESC LIMIT 1;
			IF NOT FOUND THEN
				SELECT a.seq, a.prev_hash INTO v_seq, v_prev
				FROM ` + AnchorsTable + ` a WHERE a.table_name = ` + quoteLiteral(t) + ` ORDER BY a.seq DESC, a.id DESC LIMIT 1;
				IF NOT FOUND THEN
					RAISE EXCEPTION 'nexo_chain: falta el ancla de la cadena de %', ` + quoteLiteral(t) + `;
				END IF;
			END IF;
			NEW.nexo_chain_seq := v_seq;
			NEW.nexo_prev_hash := v_prev;
			NEW.nexo_hash := sha256(v_prev || sha256(convert_to(` + canonFunc(t) + `(NEW), 'UTF8')));
			RETURN NEW;
		END
		$$`,
		`DO $$ BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = ` + quoteLiteral(linkTrigger(t)) + ` AND tgrelid = ` + quoteLiteral(t) + `::regclass) THEN
				CREATE TRIGGER ` + linkTrigger(t) + ` BEFORE INSERT ON ` + t + `
					FOR EACH ROW EXECUTE FUNCTION ` + linkFunc(t) + `();
			END IF;
		END $$`,
	}
	for _, q := range stmts {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("evidence: cadena %s: %w", t, err)
		}
	}
	// Ancla inicial (una vez): las filas anteriores a la migración no se
	// recalculan; el hash inicial resume la última de ellas.
	var hasAnchor bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM `+AnchorsTable+` WHERE table_name = $1)`, t).Scan(&hasAnchor); err != nil {
		return fmt.Errorf("evidence: cadena %s: %w", t, err)
	}
	if hasAnchor {
		return nil
	}
	var legacyRows int64
	var legacyMax sql.NullInt64
	var lastCanon sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT count(*), max(id) FROM `+t+` WHERE nexo_chain_seq IS NULL`).Scan(&legacyRows, &legacyMax); err != nil {
		return fmt.Errorf("evidence: cadena %s: %w", t, err)
	}
	if legacyMax.Valid {
		if err := tx.QueryRowContext(ctx, `SELECT `+canonFunc(t)+`(x) FROM `+t+` x WHERE x.id = $1`, legacyMax.Int64).Scan(&lastCanon); err != nil {
			return fmt.Errorf("evidence: cadena %s: %w", t, err)
		}
	}
	genesis := GenesisHash(t, lastCanon.String, legacyRows)
	if _, err := tx.ExecContext(ctx, `INSERT INTO `+AnchorsTable+` (table_name, kind, seq, prev_hash, legacy_rows, legacy_max_id, details)
		VALUES ($1, 'genesis', 1, $2, $3, $4, jsonb_build_object('format', $5::int))`, t, genesis, legacyRows, legacyMax, ChainFormatVersion); err != nil {
		return fmt.Errorf("evidence: cadena %s: %w", t, err)
	}
	return nil
}

// GenesisHash es el hash inicial de la cadena de una tabla: resume la última
// fila anterior a la migración (su representación canónica) y la cantidad de
// filas previas.
func GenesisHash(table, lastLegacyCanonical string, legacyRows int64) []byte {
	sum := sha256.Sum256([]byte(fmt.Sprintf("nexo-chain-genesis|v%d|%s|%d|%s", ChainFormatVersion, table, legacyRows, lastLegacyCanonical)))
	return sum[:]
}

// LinkHash es el hash de una fila: sha256(prev_hash || sha256(canónica)).
// digest es sha256(canónica).
func LinkHash(prev, digest []byte) []byte {
	h := sha256.New()
	h.Write(prev)
	h.Write(digest)
	return h.Sum(nil)
}

func quoteLiteral(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// ─── Puntos de control ───────────────────────────────────────────────────────

// Checkpoint registra la última posición y el hash de cada tabla encadenada
// si cambió desde el punto de control anterior. Lo llama el monitor (y la
// CLI al exportar). Devuelve cuántos registró.
func Checkpoint(ctx context.Context, db *sql.DB, source string) (int, error) {
	n := 0
	for _, spec := range ChainedTables {
		res, err := db.ExecContext(ctx, `
			INSERT INTO `+CheckpointsTable+` (table_name, seq, hash, source)
			SELECT $1, x.nexo_chain_seq, x.nexo_hash, $2
			FROM (SELECT nexo_chain_seq, nexo_hash FROM `+spec.Table+` WHERE nexo_chain_seq IS NOT NULL ORDER BY nexo_chain_seq DESC LIMIT 1) x
			WHERE x.nexo_chain_seq > COALESCE((SELECT max(c.seq) FROM `+CheckpointsTable+` c WHERE c.table_name = $1), 0)`, spec.Table, source)
		if err != nil {
			return n, fmt.Errorf("evidence: punto de control %s: %w", spec.Table, err)
		}
		if k, _ := res.RowsAffected(); k > 0 {
			n++
		}
	}
	return n, nil
}

// ─── Verificación ────────────────────────────────────────────────────────────

// BrokenRow es la primera fila que no coincide.
type BrokenRow struct {
	ID     int64  `json:"id"`
	Seq    int64  `json:"seq"`
	Kind   string `json:"kind"` // content (la fila cambió) | link (no enlaza con la anterior) | checkpoint (cambió desde un punto de control)
	Detail string `json:"detail"`
}

// Gap es un tramo de posiciones de la cadena sin filas.
type Gap struct {
	FromSeq int64  `json:"from_seq"`
	ToSeq   int64  `json:"to_seq"`
	Missing int64  `json:"missing"`
	Where   string `json:"where"` // start | middle | end
}

// TableReport es el resultado de verificar una tabla.
type TableReport struct {
	Table          string     `json:"table"`
	Layer          Layer      `json:"layer"`
	OK             bool       `json:"ok"`
	Checked        int64      `json:"checked_rows"`
	FirstSeq       int64      `json:"first_seq,omitempty"`
	LastSeq        int64      `json:"last_seq,omitempty"`
	LastHash       string     `json:"last_hash,omitempty"`
	CheckpointSeq  int64      `json:"checkpoint_seq"`
	Checkpoints    int        `json:"checkpoints_checked"`
	LegacyRows     int64      `json:"legacy_rows"`
	Unchained      int64      `json:"unchained_rows"`
	PurgedAnchors  int        `json:"purge_anchors"`
	Altered        int64      `json:"altered_rows"`
	FirstBroken    *BrokenRow `json:"first_broken,omitempty"`
	Gaps           []Gap      `json:"gaps,omitempty"`
	MissingRows    int64      `json:"missing_rows"`
	Problems       []string   `json:"problems,omitempty"`
	DurationMillis int64      `json:"duration_ms"`
}

// Report agrupa la verificación de varias tablas.
type Report struct {
	OK         bool          `json:"ok"`
	VerifiedAt time.Time     `json:"verified_at"`
	Tables     []TableReport `json:"tables"`
}

// maxReportedGaps acota los huecos que se listan (el total queda en
// MissingRows).
const maxReportedGaps = 100

type anchorRow struct {
	seq  int64
	prev []byte
	kind string
}

// VerifyChain verifica la cadena de una tabla. Solo lee (sirve con la cuenta
// de aplicación). Recorre las filas en orden de la cadena y comprueba: el
// enlace con la anterior, el hash del contenido, los huecos de posiciones
// (inicio, medio y final contra el último punto de control), que las filas de
// los puntos de control no hayan cambiado y que no haya filas insertadas sin
// cadena después de la migración (trigger desactivado).
func VerifyChain(ctx context.Context, db *sql.DB, table string) (TableReport, error) {
	start := time.Now()
	spec, ok := ChainSpecFor(table)
	if !ok {
		return TableReport{}, fmt.Errorf("evidence: %s no tiene cadena de hashes", table)
	}
	rep := TableReport{Table: table, Layer: spec.Layer}
	defer func() { rep.DurationMillis = time.Since(start).Milliseconds() }()

	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL AND to_regclass($2) IS NOT NULL`, table, AnchorsTable).Scan(&exists); err != nil {
		return rep, fmt.Errorf("evidence: verificar %s: %w", table, err)
	}
	if !exists {
		rep.Problems = append(rep.Problems, "la tabla o la cadena no existen (migración pendiente)")
		return rep, nil
	}

	anchors := map[int64]anchorRow{}
	var genesis *anchorRow
	var legacyMaxID sql.NullInt64
	var maxPurge int64
	rows, err := db.QueryContext(ctx, `SELECT seq, prev_hash, kind, legacy_max_id FROM `+AnchorsTable+` WHERE table_name = $1 ORDER BY seq, id`, table)
	if err != nil {
		return rep, fmt.Errorf("evidence: verificar %s: anclas: %w", table, err)
	}
	for rows.Next() {
		var a anchorRow
		var lm sql.NullInt64
		if err := rows.Scan(&a.seq, &a.prev, &a.kind, &lm); err != nil {
			_ = rows.Close()
			return rep, fmt.Errorf("evidence: verificar %s: anclas: %w", table, err)
		}
		anchors[a.seq] = a
		if a.kind == AnchorGenesis && genesis == nil {
			g := a
			genesis = &g
			legacyMaxID = lm
		}
		if a.kind == AnchorPurge {
			rep.PurgedAnchors++
			if a.seq > maxPurge {
				maxPurge = a.seq
			}
		}
	}
	if err := rows.Close(); err != nil {
		return rep, err
	}
	if genesis == nil {
		rep.Problems = append(rep.Problems, "falta el ancla inicial de la cadena")
	}

	// Puntos de control y exportaciones firmadas (los anteriores a la última
	// depuración ya no tienen fila).
	checkpoints := map[int64][]byte{}
	rows, err = db.QueryContext(ctx, `
		SELECT seq, hash FROM `+CheckpointsTable+` WHERE table_name = $1 AND seq >= $2
		UNION ALL
		SELECT last_seq, decode(last_hash, 'hex') FROM nexo_audit_exports
		WHERE table_name = $1 AND last_hash IS NOT NULL AND last_seq >= $2`, table, maxPurge)
	if err != nil {
		return rep, fmt.Errorf("evidence: verificar %s: puntos de control: %w", table, err)
	}
	for rows.Next() {
		var seq int64
		var h []byte
		if err := rows.Scan(&seq, &h); err != nil {
			_ = rows.Close()
			return rep, err
		}
		checkpoints[seq] = h
		if seq > rep.CheckpointSeq {
			rep.CheckpointSeq = seq
		}
	}
	if err := rows.Close(); err != nil {
		return rep, err
	}

	// Filas sin cadena: anteriores a la migración (normal) o posteriores
	// (insertadas con el trigger desactivado: alteración).
	q := `SELECT count(*), count(*) FILTER (WHERE $1::bigint IS NULL OR id > $1::bigint) FROM ` + table + ` WHERE nexo_chain_seq IS NULL`
	var nullRows, after int64
	if err := db.QueryRowContext(ctx, q, legacyMaxID).Scan(&nullRows, &after); err != nil {
		return rep, fmt.Errorf("evidence: verificar %s: %w", table, err)
	}
	rep.LegacyRows, rep.Unchained = nullRows-after, after
	if after > 0 {
		rep.Problems = append(rep.Problems, fmt.Sprintf("%d filas posteriores a la migración sin cadena (¿trigger desactivado?)", after))
	}

	addGap := func(from, to int64, where string) {
		if to < from {
			return
		}
		rep.MissingRows += to - from + 1
		if len(rep.Gaps) < maxReportedGaps {
			rep.Gaps = append(rep.Gaps, Gap{FromSeq: from, ToSeq: to, Missing: to - from + 1, Where: where})
		}
	}
	broken := func(id, seq int64, kind, detail string) {
		rep.Altered++
		if rep.FirstBroken == nil {
			rep.FirstBroken = &BrokenRow{ID: id, Seq: seq, Kind: kind, Detail: detail}
		}
	}

	// sha256 del contenido en la base; el enlace y el hash de la fila se
	// recalculan aquí.
	rows, err = db.QueryContext(ctx, `SELECT x.nexo_chain_seq, x.id, x.nexo_prev_hash, x.nexo_hash,
			sha256(convert_to(`+canonFunc(table)+`(x), 'UTF8'))
		FROM `+table+` x WHERE x.nexo_chain_seq IS NOT NULL ORDER BY x.nexo_chain_seq`)
	if err != nil {
		return rep, fmt.Errorf("evidence: verificar %s: %w", table, err)
	}
	defer rows.Close()
	var (
		prevHash []byte
		lastSeq  int64
		first    = true
		seq, id  int64
		prev, h  []byte
		digest   []byte
	)
	for rows.Next() {
		if err := rows.Scan(&seq, &id, &prev, &h, &digest); err != nil {
			return rep, fmt.Errorf("evidence: verificar %s: %w", table, err)
		}
		rep.Checked++
		if first {
			first = false
			rep.FirstSeq = seq
			if a, ok := anchors[seq]; ok {
				if !bytes.Equal(a.prev, prev) {
					broken(id, seq, "link", "no enlaza con el ancla de la cadena ("+a.kind+")")
				}
			} else {
				// Faltan filas al inicio sin depuración registrada.
				from := int64(1)
				for s := range anchors {
					if s < seq && s > from {
						from = s
					}
				}
				addGap(from, seq-1, "start")
			}
		} else if seq != lastSeq+1 {
			addGap(lastSeq+1, seq-1, "middle")
		} else if !bytes.Equal(prev, prevHash) {
			broken(id, seq, "link", "nexo_prev_hash no coincide con el hash de la fila anterior")
		}
		if !bytes.Equal(LinkHash(prev, digest), h) {
			broken(id, seq, "content", "el contenido de la fila no coincide con su hash")
		}
		if ch, ok := checkpoints[seq]; ok {
			rep.Checkpoints++
			if !bytes.Equal(ch, h) {
				broken(id, seq, "checkpoint", "la fila cambió desde el punto de control registrado")
			}
		}
		prevHash, lastSeq = h, seq
	}
	if err := rows.Err(); err != nil {
		return rep, fmt.Errorf("evidence: verificar %s: %w", table, err)
	}
	rep.LastSeq = lastSeq
	if len(prevHash) > 0 {
		rep.LastHash = hex.EncodeToString(prevHash)
	}
	// Final de la cadena contra el último punto de control: si faltan las
	// últimas filas (o todas), hay un hueco al final.
	switch {
	case first && rep.CheckpointSeq >= maxPurge && rep.CheckpointSeq > 0:
		addGap(max(maxPurge, 1), rep.CheckpointSeq, "end")
	case !first && lastSeq < rep.CheckpointSeq:
		addGap(lastSeq+1, rep.CheckpointSeq, "end")
	}
	rep.OK = rep.FirstBroken == nil && rep.MissingRows == 0 && len(rep.Problems) == 0
	return rep, nil
}

// VerifyAll verifica las tablas indicadas (todas las encadenadas si tables
// está vacío).
func VerifyAll(ctx context.Context, db *sql.DB, tables []string) (Report, error) {
	if len(tables) == 0 {
		for _, s := range ChainedTables {
			tables = append(tables, s.Table)
		}
	}
	rep := Report{OK: true, VerifiedAt: time.Now().UTC()}
	for _, t := range tables {
		tr, err := VerifyChain(ctx, db, t)
		if err != nil {
			return rep, err
		}
		rep.Tables = append(rep.Tables, tr)
		if !tr.OK {
			rep.OK = false
		}
	}
	return rep, nil
}
