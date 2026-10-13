package client

// Copia local de solo lectura (D-01, design §6.4, RF-40..RF-43, KP-07): un
// SQLite propio con FTS5 (BM25) que guarda, de los espacios visibles para el
// usuario, las secciones vigentes y las programadas con su vigencia. La
// vigencia se aplica AL CONSULTAR con el reloj local (o con at): sin
// conexión nunca se responde una regla vencida y una versión programada
// entra sola en su fecha.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	_ "modernc.org/sqlite" // driver "sqlite" (el mismo de Engram), con FTS5

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/ingest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/search"
)

// Variables de entorno de la copia local.
const (
	// EnvLocalDB fija la ruta del archivo (por defecto $NEXO_HOME/kb.db o
	// ~/.nexo/kb.db).
	EnvLocalDB = "NEXO_KB_LOCAL_DB"
	// EnvNexoHome es la carpeta de Nexo en la PC (la usa también el kit).
	EnvNexoHome = "NEXO_HOME"
	// EnvMinCoverage es el umbral de "no hay estándar" (el mismo del servidor).
	EnvMinCoverage = "NEXO_KB_MIN_COVERAGE"
	// EnvMaxPerDoc es el máximo de secciones de un documento por respuesta.
	EnvMaxPerDoc = "NEXO_KB_MAX_PER_DOC"
)

const localSchemaVersion = "1"

// DefaultLocalPath devuelve la ruta de la copia local.
func DefaultLocalPath() string {
	if p := strings.TrimSpace(os.Getenv(EnvLocalDB)); p != "" {
		return p
	}
	if h := strings.TrimSpace(os.Getenv(EnvNexoHome)); h != "" {
		return filepath.Join(h, "kb.db")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), "nexo-kb.db")
	}
	return filepath.Join(home, ".nexo", "kb.db")
}

// Local es la copia local.
type Local struct {
	db   *sql.DB
	path string
	now  func() time.Time
}

// OpenLocal abre (o crea) la copia local.
func OpenLocal(path string) (*Local, error) {
	if path == "" {
		path = DefaultLocalPath()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("copia local: %w", err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(off)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // un escritor; las lecturas son rápidas
	l := &Local{db: db, path: path, now: time.Now}
	if err := l.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("copia local %s: %w", path, err)
	}
	return l, nil
}

// Path es la ruta del archivo.
func (l *Local) Path() string { return l.path }

// Close cierra la copia.
func (l *Local) Close() error { return l.db.Close() }

func (l *Local) migrate(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS kb_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS kb_spaces (id TEXT PRIMARY KEY, slug TEXT NOT NULL UNIQUE, name TEXT NOT NULL, role TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS kb_documents (
			id TEXT PRIMARY KEY, space_id TEXT NOT NULL, space TEXT NOT NULL, doc_key TEXT NOT NULL, title TEXT NOT NULL,
			category TEXT NOT NULL DEFAULT '', tags TEXT NOT NULL DEFAULT '[]', owner TEXT NOT NULL DEFAULT '', status TEXT NOT NULL,
			current_version INTEGER NOT NULL, valid_from INTEGER NOT NULL, valid_until INTEGER, superseded_by TEXT NOT NULL DEFAULT '')`,
		`CREATE INDEX IF NOT EXISTS kb_documents_space ON kb_documents(space_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS kb_documents_key ON kb_documents(space, doc_key)`,
		`CREATE TABLE IF NOT EXISTS kb_sections (
			id INTEGER PRIMARY KEY, document_id TEXT NOT NULL, space_id TEXT NOT NULL, version INTEGER NOT NULL, anchor TEXT NOT NULL,
			ordinal INTEGER NOT NULL, heading_path TEXT NOT NULL DEFAULT '[]', body TEXT NOT NULL, body_sha256 TEXT NOT NULL DEFAULT '',
			source_location TEXT NOT NULL DEFAULT '{}', extraction TEXT NOT NULL DEFAULT '', valid_from INTEGER NOT NULL, valid_until INTEGER,
			source_url TEXT NOT NULL DEFAULT '')`,
		`CREATE INDEX IF NOT EXISTS kb_sections_doc ON kb_sections(document_id)`,
		`CREATE INDEX IF NOT EXISTS kb_sections_space ON kb_sections(space_id)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS kb_sections_fts USING fts5(title, heading, body, tokenize = 'unicode61 remove_diacritics 2')`,
	}
	for _, q := range stmts {
		if _, err := l.db.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	_, err := l.db.ExecContext(ctx, `INSERT INTO kb_meta (key, value) VALUES ('schema_version', ?) ON CONFLICT(key) DO NOTHING`, localSchemaVersion)
	return err
}

// ─── Metadatos ───────────────────────────────────────────────────────────────

func (l *Local) meta(ctx context.Context, key string) string {
	var v string
	_ = l.db.QueryRowContext(ctx, `SELECT value FROM kb_meta WHERE key = ?`, key).Scan(&v)
	return v
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func setMeta(ctx context.Context, x execer, key, value string) error {
	_, err := x.ExecContext(ctx, `INSERT INTO kb_meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// Status es el estado de la copia local.
type Status struct {
	Path        string     `json:"path"`
	SyncedAt    *time.Time `json:"synced_at,omitempty"`
	Age         string     `json:"age,omitempty"`
	Seq         int64      `json:"seq"`
	Spaces      int        `json:"spaces"`
	Documents   int        `json:"documents"`
	Sections    int        `json:"sections"`
	Server      string     `json:"server,omitempty"`
	LastError   string     `json:"last_error,omitempty"`
	LastErrorAt *time.Time `json:"last_error_at,omitempty"`
	Empty       bool       `json:"empty"`
}

// Status devuelve el estado de la copia.
func (l *Local) Status(ctx context.Context) Status {
	st := Status{Path: l.path}
	if t := parseTime(l.meta(ctx, "synced_at")); t != nil {
		st.SyncedAt = t
		st.Age = humanAge(l.now().Sub(*t))
	}
	st.Seq, _ = strconv.ParseInt(l.meta(ctx, "seq"), 10, 64)
	st.Server = l.meta(ctx, "server")
	st.LastError = l.meta(ctx, "last_error")
	st.LastErrorAt = parseTime(l.meta(ctx, "last_error_at"))
	_ = l.db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM kb_spaces), (SELECT count(*) FROM kb_documents), (SELECT count(*) FROM kb_sections)`).
		Scan(&st.Spaces, &st.Documents, &st.Sections)
	st.Empty = st.SyncedAt == nil
	return st
}

func parseTime(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return nil
	}
	return &t
}

// humanAge describe una antigüedad en español ("hace 3 h").
func humanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "hace menos de 1 min"
	case d < time.Hour:
		return fmt.Sprintf("hace %d min", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("hace %d h", int(d.Hours()))
	default:
		return fmt.Sprintf("hace %d días", int(d.Hours()/24))
	}
}

// ─── Aplicar el feed ─────────────────────────────────────────────────────────

// SyncSpace, SyncSection, SyncDocument, SyncChange y SyncPage son la forma
// JSON de GET /nexo/kb/v1/sync (service.SyncResult), sin depender del
// paquete del servidor.
type SyncSpace struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
	Role string `json:"role"`
}

// SyncSection es una sección vigente o programada.
type SyncSection struct {
	Version        int            `json:"version"`
	Anchor         string         `json:"anchor"`
	Ordinal        int            `json:"ordinal"`
	HeadingPath    []string       `json:"heading_path"`
	Body           string         `json:"body"`
	BodySHA256     string         `json:"body_sha256"`
	SourceLocation map[string]any `json:"source_location"`
	Extraction     string         `json:"extraction"`
	ValidFrom      time.Time      `json:"valid_from"`
	ValidUntil     *time.Time     `json:"valid_until,omitempty"`
	SourceURL      string         `json:"source_url,omitempty"`
}

// SyncDocument es el estado final de un documento.
type SyncDocument struct {
	ID             string        `json:"id"`
	Space          string        `json:"space"`
	DocKey         string        `json:"doc_key"`
	Title          string        `json:"title"`
	Category       string        `json:"category"`
	Tags           []string      `json:"tags"`
	Owner          string        `json:"owner"`
	Status         string        `json:"status"`
	CurrentVersion int           `json:"current_version"`
	ValidFrom      time.Time     `json:"valid_from"`
	ValidUntil     *time.Time    `json:"valid_until,omitempty"`
	SupersededBy   string        `json:"superseded_by,omitempty"`
	Sections       []SyncSection `json:"sections"`
}

// SyncChange es un cambio del feed.
type SyncChange struct {
	Seq      int64         `json:"seq"`
	Kind     string        `json:"kind"`
	Space    string        `json:"space"`
	Document *SyncDocument `json:"document,omitempty"`
	DocID    string        `json:"document_id,omitempty"`
	DocKey   string        `json:"doc_key,omitempty"`
}

// SyncPage es una página del feed.
type SyncPage struct {
	Seq            int64        `json:"seq"`
	HasMore        bool         `json:"has_more"`
	ResyncRequired bool         `json:"resync_required"`
	ServerTime     time.Time    `json:"server_time"`
	VisibleSpaces  []SyncSpace  `json:"visible_spaces"`
	Changes        []SyncChange `json:"changes"`
	PublicURL      string       `json:"public_url,omitempty"`
}

// ApplyStats cuenta lo aplicado.
type ApplyStats struct {
	Upserts       int      `json:"upserts"`
	Removes       int      `json:"removes"`
	SpacesRemoved []string `json:"spaces_removed,omitempty"`
}

// spaceSlugs devuelve los slugs guardados.
func (l *Local) spaceSlugs(ctx context.Context) (map[string]bool, error) {
	rows, err := l.db.QueryContext(ctx, `SELECT slug FROM kb_spaces`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out[s] = true
	}
	return out, rows.Err()
}

// Apply aplica una página del feed en una transacción: borra los espacios que
// ya no son visibles (RF-41), actualiza los visibles, reemplaza cada
// documento cambiado por su estado final (o lo borra) y guarda el cursor.
func (l *Local) Apply(ctx context.Context, p SyncPage) (ApplyStats, error) {
	var st ApplyStats
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return st, err
	}
	defer func() { _ = tx.Rollback() }()
	visible := map[string]SyncSpace{}
	for _, s := range p.VisibleSpaces {
		visible[s.ID] = s
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, slug FROM kb_spaces`)
	if err != nil {
		return st, err
	}
	var gone [][2]string
	for rows.Next() {
		var id, slug string
		if err := rows.Scan(&id, &slug); err != nil {
			rows.Close()
			return st, err
		}
		if v, ok := visible[id]; !ok || v.Slug != slug {
			gone = append(gone, [2]string{id, slug})
		}
	}
	rows.Close()
	for _, g := range gone {
		if err := deleteSpace(ctx, tx, g[0]); err != nil {
			return st, err
		}
		if _, ok := visible[g[0]]; !ok {
			st.SpacesRemoved = append(st.SpacesRemoved, g[1])
		}
	}
	for _, s := range p.VisibleSpaces {
		if _, err := tx.ExecContext(ctx, `INSERT INTO kb_spaces (id, slug, name, role) VALUES (?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET slug = excluded.slug, name = excluded.name, role = excluded.role`, s.ID, s.Slug, s.Name, s.Role); err != nil {
			return st, err
		}
	}
	for _, c := range p.Changes {
		switch {
		case c.Document != nil:
			sp, ok := spaceBySlug(p.VisibleSpaces, c.Document.Space)
			if !ok {
				continue // el espacio dejó de ser visible en esta misma página
			}
			if _, err := deleteDocument(ctx, tx, c.Document.ID); err != nil {
				return st, err
			}
			// Un documento con la misma clave e id distinto (borrado y
			// recreado) se reemplaza.
			if _, err := tx.ExecContext(ctx, `DELETE FROM kb_sections_fts WHERE rowid IN (SELECT s.id FROM kb_sections s JOIN kb_documents d ON d.id = s.document_id WHERE d.space = ? AND d.doc_key = ?)`,
				sp.Slug, c.Document.DocKey); err != nil {
				return st, err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM kb_sections WHERE document_id IN (SELECT id FROM kb_documents WHERE space = ? AND doc_key = ?)`, sp.Slug, c.Document.DocKey); err != nil {
				return st, err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM kb_documents WHERE space = ? AND doc_key = ?`, sp.Slug, c.Document.DocKey); err != nil {
				return st, err
			}
			if err := insertDocument(ctx, tx, sp, *c.Document); err != nil {
				return st, err
			}
			st.Upserts++
		case c.DocID != "":
			gone, err := deleteDocument(ctx, tx, c.DocID)
			if err != nil {
				return st, err
			}
			if gone {
				st.Removes++ // solo los que la copia tenía
			}
		}
	}
	if err := setMeta(ctx, tx, "seq", strconv.FormatInt(p.Seq, 10)); err != nil {
		return st, err
	}
	if p.PublicURL != "" {
		if err := setMeta(ctx, tx, "public_url", p.PublicURL); err != nil {
			return st, err
		}
	}
	return st, tx.Commit()
}

func spaceBySlug(spaces []SyncSpace, slug string) (SyncSpace, bool) {
	for _, s := range spaces {
		if s.Slug == slug {
			return s, true
		}
	}
	return SyncSpace{}, false
}

func deleteSpace(ctx context.Context, tx *sql.Tx, spaceID string) error {
	for _, q := range []string{
		`DELETE FROM kb_sections_fts WHERE rowid IN (SELECT id FROM kb_sections WHERE space_id = ?)`,
		`DELETE FROM kb_sections WHERE space_id = ?`,
		`DELETE FROM kb_documents WHERE space_id = ?`,
		`DELETE FROM kb_spaces WHERE id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, spaceID); err != nil {
			return err
		}
	}
	return nil
}

func deleteDocument(ctx context.Context, tx *sql.Tx, docID string) (bool, error) {
	var n int64
	for _, q := range []string{
		`DELETE FROM kb_sections_fts WHERE rowid IN (SELECT id FROM kb_sections WHERE document_id = ?)`,
		`DELETE FROM kb_sections WHERE document_id = ?`,
		`DELETE FROM kb_documents WHERE id = ?`,
	} {
		res, err := tx.ExecContext(ctx, q, docID)
		if err != nil {
			return false, err
		}
		n, _ = res.RowsAffected()
	}
	return n > 0, nil
}

func unixOrNil(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Unix()
}

func insertDocument(ctx context.Context, tx *sql.Tx, sp SyncSpace, d SyncDocument) error {
	tags, _ := json.Marshal(nonNil(d.Tags))
	if _, err := tx.ExecContext(ctx, `INSERT INTO kb_documents (id, space_id, space, doc_key, title, category, tags, owner, status, current_version, valid_from, valid_until, superseded_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, d.ID, sp.ID, sp.Slug, d.DocKey, d.Title, d.Category, string(tags), d.Owner, d.Status,
		d.CurrentVersion, d.ValidFrom.UTC().Unix(), unixOrNil(d.ValidUntil), d.SupersededBy); err != nil {
		return err
	}
	for _, s := range d.Sections {
		hp, _ := json.Marshal(nonNil(s.HeadingPath))
		loc, _ := json.Marshal(s.SourceLocation)
		res, err := tx.ExecContext(ctx, `INSERT INTO kb_sections (document_id, space_id, version, anchor, ordinal, heading_path, body, body_sha256, source_location, extraction, valid_from, valid_until, source_url)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, d.ID, sp.ID, s.Version, s.Anchor, s.Ordinal, string(hp), s.Body, s.BodySHA256, string(loc),
			s.Extraction, s.ValidFrom.UTC().Unix(), unixOrNil(s.ValidUntil), s.SourceURL)
		if err != nil {
			return err
		}
		rowID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO kb_sections_fts (rowid, title, heading, body) VALUES (?, ?, ?, ?)`,
			rowID, d.Title, strings.Join(s.HeadingPath, " "), s.Body); err != nil {
			return err
		}
	}
	return nil
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// Wipe borra el contenido (no los metadatos de identidad): para un
// resync_required o un cambio de usuario o de servidor.
func (l *Local) Wipe(ctx context.Context, keepMeta ...string) error {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, q := range []string{`DELETE FROM kb_sections_fts`, `DELETE FROM kb_sections`, `DELETE FROM kb_documents`, `DELETE FROM kb_spaces`} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	keep := map[string]bool{"schema_version": true}
	for _, k := range keepMeta {
		keep[k] = true
	}
	rows, err := tx.QueryContext(ctx, `SELECT key FROM kb_meta`)
	if err != nil {
		return err
	}
	var del []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return err
		}
		if !keep[k] {
			del = append(del, k)
		}
	}
	rows.Close()
	for _, k := range del {
		if _, err := tx.ExecContext(ctx, `DELETE FROM kb_meta WHERE key = ?`, k); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ─── Consulta ────────────────────────────────────────────────────────────────

// LocalQuery es una búsqueda en la copia.
type LocalQuery struct {
	Text     string
	Spaces   []string
	Category string
	Tag      string
	At       *time.Time
	Limit    int
	Mode     string // fts (por defecto) o exact
}

// LocalHit es un resultado de la copia (mismos datos de cita que el
// servidor).
type LocalHit struct {
	Space          string
	DocKey         string
	Title          string
	Category       string
	Owner          string
	Status         string
	SupersededBy   string
	Version        int
	Anchor         string
	HeadingPath    []string
	Body           string
	SourceLocation domain.SourceLocation
	ValidFrom      time.Time
	ValidUntil     *time.Time
	SourceURL      string
	Rank           float64
	Coverage       float64
	Match          string
}

// LocalResult es la respuesta de la copia.
type LocalResult struct {
	Hits      []LocalHit
	NoResults bool
	Relevance *search.Relevance
}

// fts por defecto; cobertura y diversidad iguales al servidor (R1).
func envFloat(name string, def float64) float64 {
	if v, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv(name)), 64); err == nil && v >= 0 {
		return v
	}
	return def
}

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name))); err == nil && v >= 0 {
		return v
	}
	return def
}

const hitColumns = `d.space, d.doc_key, d.title, d.category, d.owner, d.status, d.superseded_by, s.version, s.anchor, s.heading_path, s.body,
	s.source_location, s.valid_from, s.valid_until, s.source_url`

func scanHit(rows *sql.Rows, extra ...any) (LocalHit, error) {
	var h LocalHit
	var hp, loc string
	var vf int64
	var vu sql.NullInt64
	dest := append([]any{&h.Space, &h.DocKey, &h.Title, &h.Category, &h.Owner, &h.Status, &h.SupersededBy, &h.Version, &h.Anchor, &hp, &h.Body,
		&loc, &vf, &vu, &h.SourceURL}, extra...)
	if err := rows.Scan(dest...); err != nil {
		return h, err
	}
	_ = json.Unmarshal([]byte(hp), &h.HeadingPath)
	_ = json.Unmarshal([]byte(loc), &h.SourceLocation)
	h.ValidFrom = time.Unix(vf, 0).UTC()
	if vu.Valid {
		u := time.Unix(vu.Int64, 0).UTC()
		h.ValidUntil = &u
	}
	return h, nil
}

// filters arma la parte común: vigencia en at, espacios, categoría y
// etiqueta (filtros obligatorios también en la copia, §14.3 regla 4).
func filters(q LocalQuery, at int64) (string, []any) {
	where := ` AND s.valid_from <= ? AND (s.valid_until IS NULL OR s.valid_until > ?)`
	args := []any{at, at}
	if len(q.Spaces) > 0 {
		where += ` AND d.space IN (` + strings.TrimSuffix(strings.Repeat("?,", len(q.Spaces)), ",") + `)`
		for _, s := range q.Spaces {
			args = append(args, s)
		}
	}
	if c := strings.ToLower(strings.TrimSpace(q.Category)); c != "" {
		where += ` AND d.category = ?`
		args = append(args, c)
	}
	if t := strings.ToLower(strings.TrimSpace(q.Tag)); t != "" {
		where += ` AND EXISTS (SELECT 1 FROM json_each(d.tags) j WHERE lower(j.value) = ?)`
		args = append(args, t)
	}
	return where, args
}

// Search busca en la copia con BM25 (FTS5), aplicando la vigencia en at
// (por defecto, ahora con el reloj local).
func (l *Local) Search(ctx context.Context, q LocalQuery) (LocalResult, error) {
	text := strings.TrimSpace(q.Text)
	if text == "" {
		return LocalResult{}, errors.New("falta la consulta")
	}
	if q.Limit <= 0 {
		q.Limit = search.DefaultLimit
	}
	if q.Limit > search.MaxLimit {
		q.Limit = search.MaxLimit
	}
	at := l.now().UTC()
	if q.At != nil {
		at = q.At.UTC()
	}
	if strings.EqualFold(strings.TrimSpace(q.Mode), search.ModeExact) {
		return l.searchExact(ctx, q, text, at.Unix())
	}
	terms := queryTerms(text)
	if len(terms) == 0 {
		return LocalResult{NoResults: true}, nil
	}
	where, args := filters(q, at.Unix())
	minCov := envFloat(EnvMinCoverage, 0.3)
	maxPerDoc := envInt(EnvMaxPerDoc, 2)
	want := q.Limit * 3
	run := func(match string) ([]LocalHit, error) {
		rows, err := l.db.QueryContext(ctx, `SELECT `+hitColumns+`, bm25(kb_sections_fts, 5.0, 3.0, 1.0)
			FROM kb_sections_fts f JOIN kb_sections s ON s.id = f.rowid JOIN kb_documents d ON d.id = s.document_id
			WHERE kb_sections_fts MATCH ?`+where+` ORDER BY 16 LIMIT ?`, append(append([]any{match}, args...), want)...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []LocalHit
		for rows.Next() {
			var rank float64
			h, err := scanHit(rows, &rank)
			if err != nil {
				return nil, err
			}
			h.Rank = -rank
			out = append(out, h)
		}
		return out, rows.Err()
	}
	hits, err := run(ftsQuery(terms, " AND "))
	if err != nil {
		return LocalResult{}, err
	}
	for i := range hits {
		hits[i].Match = search.MatchAllTerms
	}
	if len(hits) < q.Limit && len(terms) > 1 {
		more, err := run(ftsQuery(terms, " OR "))
		if err != nil {
			return LocalResult{}, err
		}
		seen := map[string]bool{}
		for _, h := range hits {
			seen[h.Space+"\x00"+h.DocKey+"\x00"+h.Anchor+"\x00"+strconv.Itoa(h.Version)] = true
		}
		for _, h := range more {
			k := h.Space + "\x00" + h.DocKey + "\x00" + h.Anchor + "\x00" + strconv.Itoa(h.Version)
			if !seen[k] {
				h.Match = search.MatchSomeTerms
				hits = append(hits, h)
			}
		}
	}
	// Umbral de "no hay estándar" (RC-07): cobertura ponderada por rareza
	// de los términos en las secciones vigentes de la copia.
	weights, err := l.termWeights(ctx, terms, where, args)
	if err != nil {
		return LocalResult{}, err
	}
	res := LocalResult{}
	var rel *search.Relevance
	if minCov > 0 {
		rel = &search.Relevance{MinCoverage: minCov}
	}
	perDoc := map[string]int{}
	for _, h := range hits {
		h.Coverage = coverage(terms, weights, h)
		if rel != nil {
			rel.BestCoverage = math.Max(rel.BestCoverage, h.Coverage)
			if h.Coverage < minCov {
				rel.Below++
				continue
			}
			rel.Above++
		}
		if len(res.Hits) == q.Limit {
			continue
		}
		key := h.Space + "\x00" + h.DocKey
		if maxPerDoc > 0 && perDoc[key] >= maxPerDoc {
			continue
		}
		perDoc[key]++
		res.Hits = append(res.Hits, h)
	}
	res.Relevance = rel
	res.NoResults = len(res.Hits) == 0
	return res, nil
}

func (l *Local) searchExact(ctx context.Context, q LocalQuery, text string, at int64) (LocalResult, error) {
	pat, err := search.ParseExact(text)
	if err != nil {
		return LocalResult{}, err
	}
	where, args := filters(q, at)
	like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.ToLower(pat.Literal)) + "%"
	rows, err := l.db.QueryContext(ctx, `SELECT `+hitColumns+` FROM kb_sections s JOIN kb_documents d ON d.id = s.document_id
		WHERE (lower(s.body) LIKE ? ESCAPE '\' OR lower(d.title) LIKE ? ESCAPE '\')`+where+` LIMIT 2000`, append([]any{like, like}, args...)...)
	if err != nil {
		return LocalResult{}, err
	}
	defer rows.Close()
	var hits []LocalHit
	for rows.Next() {
		h, err := scanHit(rows)
		if err != nil {
			return LocalResult{}, err
		}
		n := 0
		if pat.Re != nil {
			n = len(pat.Re.FindAllStringIndex(h.Title+"\n"+h.Body, -1))
		} else {
			n = strings.Count(strings.ToLower(h.Title+"\n"+h.Body), strings.ToLower(pat.Literal))
		}
		if n == 0 {
			continue
		}
		h.Rank, h.Match, h.Coverage = float64(n), search.MatchExact, -1
		hits = append(hits, h)
	}
	if err := rows.Err(); err != nil {
		return LocalResult{}, err
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Rank > hits[j].Rank })
	if len(hits) > q.Limit {
		hits = hits[:q.Limit]
	}
	return LocalResult{Hits: hits, NoResults: len(hits) == 0}, nil
}

// termWeights calcula ln(1 + N/(df+1)) por término en las secciones vigentes
// que cumplen los filtros (el mismo modelo de cobertura del servidor).
func (l *Local) termWeights(ctx context.Context, terms []string, where string, args []any) (map[string]float64, error) {
	var n float64
	if err := l.db.QueryRowContext(ctx, `SELECT count(*) FROM kb_sections s JOIN kb_documents d ON d.id = s.document_id WHERE 1 = 1`+where, args...).Scan(&n); err != nil {
		return nil, err
	}
	out := map[string]float64{}
	for _, t := range terms {
		var df float64
		if err := l.db.QueryRowContext(ctx, `SELECT count(*) FROM kb_sections_fts f JOIN kb_sections s ON s.id = f.rowid JOIN kb_documents d ON d.id = s.document_id
			WHERE kb_sections_fts MATCH ?`+where, append([]any{ftsTerm(t)}, args...)...).Scan(&df); err != nil {
			return nil, err
		}
		out[t] = math.Log(1 + n/(df+1))
	}
	return out, nil
}

func coverage(terms []string, weights map[string]float64, h LocalHit) float64 {
	words := map[string]bool{}
	for _, w := range tokenize(h.Title + " " + strings.Join(h.HeadingPath, " ") + " " + h.Body) {
		words[w] = true
	}
	var total, got float64
	for _, t := range terms {
		w := weights[t]
		total += w
		stem := termStem(t)
		for word := range words {
			if strings.HasPrefix(word, stem) {
				got += w
				break
			}
		}
	}
	if total == 0 {
		return 0
	}
	return got / total
}

// ─── Términos de la consulta ─────────────────────────────────────────────────

// stopwords en español e inglés (preguntas en lenguaje natural).
var stopwords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`a al algo algun alguna algunas alguno algunos ante antes aqui asi aun cada como con contra cual cuales cuando
		de debe deben deberia del desde donde dos e el ella ellas ellos en entre era es esa esas ese eso esos esta estan estas este esto estos
		existe existen fue ha hace hacer hacemos hay la las le les lo los mas me mi mis mucho muy no nos nuestra nuestro o os otra otro para pero
		poco por porque puede pueden que quien se segun ser si sin sobre son su sus tambien tiene tienen todo todos tu un una unas uno unos usa
		usamos usar uso y ya
		the a an and are as at be by for from how in is it of on or that the this to what when where which who why with do does should use`) {
		m[w] = true
	}
	return m
}()

// tokenize pliega (minúsculas, sin tildes) y separa en palabras.
func tokenize(s string) []string {
	s = strings.ToLower(ingest.Fold(s))
	return strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// queryTerms devuelve los términos útiles de la consulta (sin repetidos ni
// palabras vacías), como máximo 12.
func queryTerms(q string) []string {
	var out []string
	seen := map[string]bool{}
	for _, w := range tokenize(q) {
		if len(w) < 2 || stopwords[w] || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
		if len(out) == 12 {
			break
		}
	}
	return out
}

// termStem recorta sufijos de plural y flexión frecuentes para buscar por
// prefijo ("validaciones" -> "validacion", "tablas" -> "tabla"): la copia
// no tiene el lematizador en español del servidor.
func termStem(t string) string {
	r := []rune(t)
	switch {
	case len(r) >= 7:
		return string(r[:len(r)-2])
	case len(r) >= 5:
		return string(r[:len(r)-1])
	}
	return t
}

func ftsTerm(t string) string { return `"` + strings.ReplaceAll(termStem(t), `"`, `""`) + `"*` }

func ftsQuery(terms []string, op string) string {
	parts := make([]string, len(terms))
	for i, t := range terms {
		parts[i] = ftsTerm(t)
	}
	return strings.Join(parts, op)
}

// ─── Lectura ─────────────────────────────────────────────────────────────────

// LocalDoc es un documento leído de la copia (solo lo vigente hoy).
type LocalDoc struct {
	Space        string
	DocKey       string
	Title        string
	Category     string
	Owner        string
	Status       string
	SupersededBy string
	Version      int
	Sections     []LocalHit
}

// Get lee un documento (o una sección si anchor no está vacío) con lo que
// rige en at (por defecto, ahora).
func (l *Local) Get(ctx context.Context, space, docKey, anchor string, at *time.Time) (LocalDoc, bool, error) {
	t := l.now().UTC()
	if at != nil {
		t = at.UTC()
	}
	q := `SELECT ` + hitColumns + ` FROM kb_sections s JOIN kb_documents d ON d.id = s.document_id
		WHERE d.space = ? AND d.doc_key = ? AND s.valid_from <= ? AND (s.valid_until IS NULL OR s.valid_until > ?)`
	args := []any{space, docKey, t.Unix(), t.Unix()}
	if anchor != "" {
		q += ` AND s.anchor = ?`
		args = append(args, anchor)
	}
	rows, err := l.db.QueryContext(ctx, q+` ORDER BY s.version DESC, s.ordinal`, args...)
	if err != nil {
		return LocalDoc{}, false, err
	}
	defer rows.Close()
	var doc LocalDoc
	for rows.Next() {
		h, err := scanHit(rows)
		if err != nil {
			return LocalDoc{}, false, err
		}
		if doc.Version != 0 && h.Version != doc.Version {
			continue // dos versiones no se solapan en vigencia; por las dudas, la más nueva
		}
		doc.Space, doc.DocKey, doc.Title, doc.Category, doc.Owner, doc.Status, doc.SupersededBy, doc.Version =
			h.Space, h.DocKey, h.Title, h.Category, h.Owner, h.Status, h.SupersededBy, h.Version
		doc.Sections = append(doc.Sections, h)
	}
	if err := rows.Err(); err != nil {
		return LocalDoc{}, false, err
	}
	return doc, len(doc.Sections) > 0, nil
}

// Spaces lista los espacios de la copia.
func (l *Local) Spaces(ctx context.Context) ([]SyncSpace, error) {
	rows, err := l.db.QueryContext(ctx, `SELECT id, slug, name, role FROM kb_spaces ORDER BY slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SyncSpace{}
	for rows.Next() {
		var s SyncSpace
		if err := rows.Scan(&s.ID, &s.Slug, &s.Name, &s.Role); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// PublicURL es la URL pública que informó el servidor (o vacía).
func (l *Local) PublicURL(ctx context.Context) string { return l.meta(ctx, "public_url") }
