package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/ingest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
)

// Querier es *sql.DB o *sql.Tx.
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Store es el acceso a las tablas nexo_kb_*.
type Store struct {
	DB *sql.DB

	modeMu     sync.Mutex
	modeLoaded bool
	fold       bool
	index      IndexOptions   // NX-10 2b (R1)
	vec        *VectorSupport // NX-10 R2 (vectors.go)
	vecAt      time.Time
}

// New crea el almacén sobre la base de Engram Cloud.
func New(db *sql.DB) *Store { return &Store{DB: db} }

// FoldInApp informa si la variante sin tildes se calcula en la aplicación.
// Se resuelve al inicializar el servicio; los accesos directos pueden usar
// la misma transacción mediante foldInApp.
func (s *Store) FoldInApp(ctx context.Context) (bool, error) {
	return s.foldInApp(ctx, s.DB)
}

// Nunca se conserva modeMu mientras el ejecutor espera una conexión.
// Si la caché está fría dentro de una transacción, q debe ser esa transacción.
func (s *Store) foldInApp(ctx context.Context, q Querier) (bool, error) {
	s.modeMu.Lock()
	if s.modeLoaded {
		fold := s.fold
		s.modeMu.Unlock()
		return fold, nil
	}
	s.modeMu.Unlock()
	uses, err := configUsesUnaccent(ctx, q)
	if err != nil {
		return false, err // no se guarda: se reintenta en la próxima llamada
	}
	s.modeMu.Lock()
	defer s.modeMu.Unlock()
	if !s.modeLoaded {
		s.fold, s.modeLoaded = !uses, true
	}
	return s.fold, nil
}

// UnaccentVector devuelve la expresión SQL de la variante sin tildes de un
// texto: con unaccent, to_tsvector('nexo_es_unaccent', original); sin él,
// to_tsvector('nexo_es', plegado en Go).
func (s *Store) UnaccentVector(ctx context.Context, q Querier, original, folded string) (string, error) {
	fold, err := s.foldInApp(ctx, q)
	if err != nil {
		return "", err
	}
	if fold {
		return `to_tsvector('` + TSConfig + `', ` + folded + `)`, nil
	}
	return `to_tsvector('` + TSConfigUnaccent + `', ` + original + `)`, nil
}

// UnaccentQuery es lo mismo para la consulta (websearch_to_tsquery).
func (s *Store) UnaccentQuery(ctx context.Context, original, folded string) (string, error) {
	fold, err := s.FoldInApp(ctx)
	if err != nil {
		return "", err
	}
	if fold {
		return `websearch_to_tsquery('` + TSConfig + `', ` + folded + `)`, nil
	}
	return `websearch_to_tsquery('` + TSConfigUnaccent + `', ` + original + `)`, nil
}

// NewID devuelve un UUID v4 (generado en la aplicación, sin extensiones).
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// Now devuelve la hora de la transacción (misma marca para todo lo que se
// escribe en ella).
func Now(ctx context.Context, q Querier) (time.Time, error) {
	var t time.Time
	err := q.QueryRowContext(ctx, `SELECT now()`).Scan(&t)
	return t.UTC(), err
}

// ─── Espacios ────────────────────────────────────────────────────────────────

const spaceColumns = `s.id::text, s.slug, s.name, s.description, array_to_json(s.categories)::text, s.settings::text,
	COALESCE(sc.visibility, 'restricted'), COALESCE(sc.default_role, 'lector'), COALESCE(sc.status, 'archived'), s.created_at, s.updated_at`

func scanSpace(row interface{ Scan(...any) error }) (domain.Space, error) {
	var sp domain.Space
	var cats, settings string
	err := row.Scan(&sp.ID, &sp.Slug, &sp.Name, &sp.Description, &cats, &settings, &sp.Visibility, &sp.DefaultRole, &sp.Status, &sp.CreatedAt, &sp.UpdatedAt)
	if err != nil {
		return sp, err
	}
	_ = json.Unmarshal([]byte(cats), &sp.Categories)
	sp.Settings = map[string]any{}
	_ = json.Unmarshal([]byte(settings), &sp.Settings)
	sp.CreatedAt, sp.UpdatedAt = sp.CreatedAt.UTC(), sp.UpdatedAt.UTC()
	return sp, nil
}

// GetSpace devuelve el espacio por slug (con visibilidad y estado de nexo_scopes).
func GetSpace(ctx context.Context, q Querier, slug string, forUpdate bool) (domain.Space, bool, error) {
	lock := ""
	if forUpdate {
		lock = " FOR UPDATE OF s"
	}
	sp, err := scanSpace(q.QueryRowContext(ctx, `SELECT `+spaceColumns+`
		FROM nexo_kb_spaces s LEFT JOIN nexo_scopes sc ON sc.scope_type = 'space' AND sc.scope_key = s.slug
		WHERE s.slug = $1`+lock, slug))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Space{}, false, nil
	}
	return sp, err == nil, err
}

// EnsureSpace crea la fila de nexo_kb_spaces de un ámbito de espacio que ya
// existe en nexo_scopes (creado por la API de roles o por un CreateSpace que
// no llegó a completar su segunda transacción). Idempotente.
func EnsureSpace(ctx context.Context, q Querier, slug, name, description string, categories []string, createdBy string) (bool, error) {
	if len(categories) == 0 {
		categories = domain.DefaultCategories
	}
	res, err := q.ExecContext(ctx, `
		INSERT INTO nexo_kb_spaces (id, slug, name, description, categories, created_by_principal_id)
		SELECT $1::uuid, $2, $3, $4, $5::text[], $6::bigint
		WHERE EXISTS (SELECT 1 FROM nexo_scopes WHERE scope_type = 'space' AND scope_key = $2)
		ON CONFLICT (slug) DO NOTHING`, NewID(), slug, name, description, categories, nullBigint(createdBy))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// UpdateSpace cambia nombre, descripción, categorías y configuración.
func UpdateSpace(ctx context.Context, q Querier, id, name, description string, categories []string, settings map[string]any) error {
	raw, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `UPDATE nexo_kb_spaces SET name = $2, description = $3, categories = $4::text[], settings = $5::jsonb, updated_at = NOW() WHERE id = $1::uuid`,
		id, name, description, categories, string(raw))
	return err
}

// SpacesBySlugs devuelve id y datos de los espacios pedidos.
func SpacesBySlugs(ctx context.Context, q Querier, slugs []string) (map[string]domain.Space, error) {
	out := map[string]domain.Space{}
	if len(slugs) == 0 {
		return out, nil
	}
	rows, err := q.QueryContext(ctx, `SELECT `+spaceColumns+`
		FROM nexo_kb_spaces s LEFT JOIN nexo_scopes sc ON sc.scope_type = 'space' AND sc.scope_key = s.slug
		WHERE s.slug = ANY($1::text[])`, slugs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		sp, err := scanSpace(rows)
		if err != nil {
			return nil, err
		}
		out[sp.Slug] = sp
	}
	return out, rows.Err()
}

// ─── Documentos ──────────────────────────────────────────────────────────────

// DocRow es un documento con lo necesario para publicar.
type DocRow struct {
	domain.Document
	ContentSHA256 string
	MetaSHA256    string
}

const docColumns = `d.id::text, d.space_id::text, sp.slug, d.doc_key, d.title, d.category, array_to_json(d.tags)::text,
	d.owner_label, COALESCE(d.owner_principal_id::text, ''), d.status, d.valid_from, d.valid_until, d.current_version,
	COALESCE(sup.doc_key, ''), COALESCE(d.origin_kind, ''), COALESCE(d.origin_ref, ''), d.created_at, d.updated_at, d.retired_at,
	COALESCE(d.retirement_note, ''), v.content_sha256, v.meta_sha256`

const docFrom = ` FROM nexo_kb_documents d
	JOIN nexo_kb_spaces sp ON sp.id = d.space_id
	JOIN nexo_kb_versions v ON v.document_id = d.id AND v.version = d.current_version
	LEFT JOIN nexo_kb_documents sup ON sup.id = d.superseded_by`

func scanDoc(row interface{ Scan(...any) error }) (DocRow, error) {
	var d DocRow
	var tags string
	var until, retired sql.NullTime
	err := row.Scan(&d.ID, &d.SpaceID, &d.Space, &d.DocKey, &d.Title, &d.Category, &tags, &d.OwnerLabel, &d.OwnerPrincipal,
		&d.Status, &d.ValidFrom, &until, &d.CurrentVersion, &d.SupersededBy, &d.OriginKind, &d.OriginRef, &d.CreatedAt, &d.UpdatedAt,
		&retired, &d.RetirementNote, &d.ContentSHA256, &d.MetaSHA256)
	if err != nil {
		return d, err
	}
	_ = json.Unmarshal([]byte(tags), &d.Tags)
	if d.Tags == nil {
		d.Tags = []string{}
	}
	d.ValidUntil = timePtr(until)
	d.RetiredAt = timePtr(retired)
	d.ValidFrom, d.CreatedAt, d.UpdatedAt = d.ValidFrom.UTC(), d.CreatedAt.UTC(), d.UpdatedAt.UTC()
	return d, nil
}

// GetDocument devuelve el documento por clave. forUpdate bloquea su fila.
func GetDocument(ctx context.Context, q Querier, spaceID, docKey string, forUpdate bool) (DocRow, bool, error) {
	lock := ""
	if forUpdate {
		lock = " FOR UPDATE OF d"
	}
	d, err := scanDoc(q.QueryRowContext(ctx, `SELECT `+docColumns+docFrom+` WHERE d.space_id = $1::uuid AND d.doc_key = $2`+lock, spaceID, docKey))
	if errors.Is(err, sql.ErrNoRows) {
		return DocRow{}, false, nil
	}
	return d, err == nil, err
}

// DocQuery filtra el listado de documentos (cursor = último doc_key).
type DocQuery struct {
	Status   string
	Category string
	After    string
	Limit    int
}

// ListDocuments lista documentos de un espacio por doc_key.
func ListDocuments(ctx context.Context, q Querier, spaceID string, f DocQuery) ([]domain.Document, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+docColumns+docFrom+`
		WHERE d.space_id = $1::uuid AND ($2 = '' OR d.status = $2) AND ($3 = '' OR d.category = $3) AND d.doc_key > $4
		ORDER BY d.doc_key LIMIT $5`, spaceID, f.Status, f.Category, f.After, f.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Document{}
	for rows.Next() {
		d, err := scanDoc(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d.Document)
	}
	return out, rows.Err()
}

// ActiveDocsByOrigin: documentos activos de un origen (para la poda).
func ActiveDocsByOrigin(ctx context.Context, q Querier, spaceID, kind, ref string) ([]DocRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+docColumns+docFrom+`
		WHERE d.space_id = $1::uuid AND d.status = 'active' AND d.origin_kind = $2 AND d.origin_ref = $3 ORDER BY d.doc_key`, spaceID, kind, ref)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DocRow
	for rows.Next() {
		d, err := scanDoc(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DocWrite son los datos de documento que escribe una publicación.
type DocWrite struct {
	ID, SpaceID, DocKey, Title, Category string
	Tags                                 []string
	OwnerLabel, OwnerPrincipalID         string
	ValidFrom                            time.Time
	ValidUntil                           *time.Time
	Version                              int
	OriginKind, OriginRef                string
}

// InsertDocument crea un documento nuevo.
func InsertDocument(ctx context.Context, q Querier, w DocWrite, now time.Time) error {
	_, err := q.ExecContext(ctx, `
		INSERT INTO nexo_kb_documents (id, space_id, doc_key, title, category, tags, owner_label, owner_principal_id, status,
			valid_from, valid_until, current_version, origin_kind, origin_ref, created_at, updated_at)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6::text[], $7, $8::bigint, 'active', $9, $10, $11, NULLIF($12, ''), NULLIF($13, ''), $14, $14)`,
		w.ID, w.SpaceID, w.DocKey, w.Title, w.Category, nonNil(w.Tags), w.OwnerLabel, nullBigint(w.OwnerPrincipalID),
		w.ValidFrom, nullTime(w.ValidUntil), w.Version, w.OriginKind, w.OriginRef, now)
	return err
}

// UpdateDocumentVersion apunta el documento a su nueva versión y lo reactiva.
func UpdateDocumentVersion(ctx context.Context, q Querier, w DocWrite, now time.Time) error {
	_, err := q.ExecContext(ctx, `
		UPDATE nexo_kb_documents SET title = $2, category = $3, tags = $4::text[], owner_label = $5, owner_principal_id = $6::bigint,
			status = 'active', valid_from = $7, valid_until = $8, current_version = $9, superseded_by = NULL,
			origin_kind = NULLIF($10, ''), origin_ref = NULLIF($11, ''), updated_at = $12, retired_at = NULL, retirement_note = NULL
		WHERE id = $1::uuid`,
		w.ID, w.Title, w.Category, nonNil(w.Tags), w.OwnerLabel, nullBigint(w.OwnerPrincipalID), w.ValidFrom, nullTime(w.ValidUntil),
		w.Version, w.OriginKind, w.OriginRef, now)
	return err
}

// RetireDocument marca el documento como retirado (y su reemplazo, si hay).
func RetireDocument(ctx context.Context, q Querier, docID, supersededBy, note string, now time.Time) error {
	_, err := q.ExecContext(ctx, `UPDATE nexo_kb_documents SET status = 'retired', superseded_by = NULLIF($2, '')::uuid,
		retired_at = $3, retirement_note = NULLIF($4, ''), updated_at = $3 WHERE id = $1::uuid`, docID, supersededBy, now, note)
	return err
}

// ─── Versiones ───────────────────────────────────────────────────────────────

// VersionWrite son los datos de una versión nueva.
type VersionWrite struct {
	domain.Version
	SpaceID    string
	MetaSHA256 string
	Actor      Actor
}

// InsertVersion inserta una versión inmutable.
func InsertVersion(ctx context.Context, q Querier, v VersionWrite) error {
	origin, err := json.Marshal(v.Origin)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `
		INSERT INTO nexo_kb_versions (id, document_id, space_id, version, title, category, tags, owner_label, content_md, content_sha256,
			meta_sha256, source_format, source_mime, source_sha256, source_size_bytes, source_blob_ref, origin, change_note,
			valid_from, valid_until, section_count, published_by_principal_id, published_by_token_id, published_by_label,
			published_by_email, published_by_verified, published_at)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7::text[], $8, $9, $10, $11, $12, NULLIF($13, ''), NULLIF($14, ''), $15,
			NULLIF($16, ''), $17::jsonb, NULLIF($18, ''), $19, $20, $21, $22::bigint, $23::bigint, $24,
			(SELECT u.email FROM cloud_human_users u WHERE u.principal_id = $22::bigint), $25, $26)`,
		v.ID, v.DocumentID, v.SpaceID, v.Version.Version, v.Title, v.Category, nonNil(v.Tags), v.OwnerLabel, v.ContentMD, v.ContentSHA256,
		v.MetaSHA256, v.SourceFormat, v.SourceMIME, v.SourceSHA256, v.SourceSizeBytes, v.SourceBlobRef, string(origin), v.ChangeNote,
		v.ValidFrom, nullTime(v.ValidUntil), v.SectionCount, nullBigint(v.Actor.PrincipalID), nullBigint(v.Actor.TokenID), v.Actor.Label,
		v.Actor.Verified, v.PublishedAt)
	return err
}

const versionColumns = `v.id::text, v.document_id::text, v.version, v.title, v.category, array_to_json(v.tags)::text, v.owner_label,
	v.content_sha256, v.source_format, COALESCE(v.source_mime, ''), COALESCE(v.source_sha256, ''), v.source_size_bytes,
	COALESCE(v.source_blob_ref, ''), v.origin::text, COALESCE(v.change_note, ''), v.valid_from, v.valid_until, v.published_by_label,
	COALESCE(v.published_by_principal_id::text, ''), COALESCE(v.published_by_token_id::text, ''), COALESCE(v.published_by_email, ''),
	v.published_at, v.section_count`

func scanVersion(row interface{ Scan(...any) error }, withContent bool, content *string) (domain.Version, error) {
	var v domain.Version
	var tags, origin string
	var until sql.NullTime
	dest := []any{&v.ID, &v.DocumentID, &v.Version, &v.Title, &v.Category, &tags, &v.OwnerLabel, &v.ContentSHA256, &v.SourceFormat,
		&v.SourceMIME, &v.SourceSHA256, &v.SourceSizeBytes, &v.SourceBlobRef, &origin, &v.ChangeNote, &v.ValidFrom, &until,
		&v.PublishedBy, &v.PublishedByID, &v.PublishedByTok, &v.PublishedEmail, &v.PublishedAt, &v.SectionCount}
	if withContent {
		dest = append(dest, content)
	}
	if err := row.Scan(dest...); err != nil {
		return v, err
	}
	_ = json.Unmarshal([]byte(tags), &v.Tags)
	_ = json.Unmarshal([]byte(origin), &v.Origin)
	v.ValidUntil = timePtr(until)
	v.ValidFrom, v.PublishedAt = v.ValidFrom.UTC(), v.PublishedAt.UTC()
	return v, nil
}

// GetVersion devuelve una versión con su contenido (version 0 = la actual).
func GetVersion(ctx context.Context, q Querier, docID string, version int) (domain.Version, bool, error) {
	var content string
	v, err := scanVersion(q.QueryRowContext(ctx, `SELECT `+versionColumns+`, v.content_md FROM nexo_kb_versions v
		JOIN nexo_kb_documents d ON d.id = v.document_id
		WHERE v.document_id = $1::uuid AND v.version = CASE WHEN $2 = 0 THEN d.current_version ELSE $2 END`, docID, version), true, &content)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Version{}, false, nil
	}
	v.ContentMD = content
	return v, err == nil, err
}

// ListVersions devuelve el historial (sin contenido), de la más nueva a la
// más antigua, desde antes de la versión beforeVersion (0 = desde la última).
func ListVersions(ctx context.Context, q Querier, docID string, beforeVersion, limit int) ([]domain.Version, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+versionColumns+` FROM nexo_kb_versions v
		WHERE v.document_id = $1::uuid AND ($2 = 0 OR v.version < $2) ORDER BY v.version DESC LIMIT $3`, docID, beforeVersion, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Version{}
	for rows.Next() {
		v, err := scanVersion(rows, false, nil)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ─── Secciones ───────────────────────────────────────────────────────────────

// SectionWrite son las secciones de una versión nueva.
type SectionWrite struct {
	SpaceID, DocumentID, VersionID string
	Category                       string
	Tags                           []string
	ValidFrom                      time.Time
	ValidUntil                     *time.Time
	RecordedFrom                   time.Time
	IsCurrent                      bool
	Sections                       []ingest.SectionDraft
	// NX-10 2b (RC-03): título del documento y ruta de títulos de la carpeta;
	// con IndexOptions.DocContext se indexan con peso A junto a los títulos
	// de la sección.
	Title      string
	PathTitles []string
}

// IndexOptions son las opciones del índice de texto (R1). Cambiarlas es una
// versión nueva del índice pgfts (nexo_kb_index_versions) y exige reindexar
// (Store.Reindex) para que las secciones anteriores las reflejen.
type IndexOptions struct {
	// DocContext (RC-03): título del documento y ruta de títulos de la
	// carpeta antepuestos al texto indexado de cada sección (peso A).
	DocContext bool `json:"doc_context"`
}

// SetIndexOptions fija las opciones del índice (antes de escribir).
func (s *Store) SetIndexOptions(o IndexOptions) {
	s.modeMu.Lock()
	s.index = o
	s.modeMu.Unlock()
}

// IndexOpts devuelve las opciones del índice en uso.
func (s *Store) IndexOpts() IndexOptions {
	s.modeMu.Lock()
	defer s.modeMu.Unlock()
	return s.index
}

// MaxIndexedBodyBytes acota el texto de una sección que entra al tsvector
// (Postgres rechaza un tsvector de más de 1 MB: una sección gigante, como un
// bloque de código o JSON enorme, hacía fallar la publicación del documento).
// El cuerpo se guarda completo; solo el índice usa el prefijo.
const MaxIndexedBodyBytes = 256 << 10

func capIndexed(s string) string {
	if len(s) <= MaxIndexedBodyBytes {
		return s
	}
	cut := MaxIndexedBodyBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// IndexedTitles es el texto de peso A de una sección.
func IndexedTitles(o IndexOptions, title string, pathTitles, headingPath []string) string {
	parts := []string{}
	if o.DocContext {
		parts = append(parts, pathTitles...)
		if t := strings.TrimSpace(title); t != "" {
			parts = append(parts, t)
		}
	}
	parts = append(parts, headingPath...)
	return strings.Join(parts, " ")
}

const pathSep = "\x1f"

// InsertSections inserta las secciones en una sola sentencia y calcula tsv
// (títulos con peso A, cuerpo con peso B).
func (s *Store) InsertSections(ctx context.Context, q Querier, w SectionWrite) error {
	if len(w.Sections) == 0 {
		return nil
	}
	n := len(w.Sections)
	ids, anchors, paths, bodies, locs, extr := make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n)
	idxTitles, idxBodies, foldTitles, foldBodies := make([]string, n), make([]string, n), make([]string, n), make([]string, n)
	ords, toks := make([]int64, n), make([]int64, n)
	hashes := make([]string, n)
	opts := s.IndexOpts()
	for i, sec := range w.Sections {
		hashes[i] = ingest.SHA256Hex(sec.Body)
		ids[i] = NewID()
		anchors[i] = sec.Anchor
		paths[i] = strings.Join(sec.HeadingPath, pathSep)
		bodies[i] = sec.Body
		raw, err := json.Marshal(sec.Location)
		if err != nil {
			return err
		}
		locs[i] = string(raw)
		extr[i] = sec.Extraction
		ords[i], toks[i] = int64(sec.Ordinal), int64(sec.TokenCount)
		idxTitles[i], idxBodies[i] = IndexedTitles(opts, w.Title, w.PathTitles, sec.HeadingPath), capIndexed(sec.Body)
		foldTitles[i], foldBodies[i] = ingest.Fold(idxTitles[i]), ingest.Fold(sec.Body)
	}
	ut, err := s.UnaccentVector(ctx, q, "x.it", "x.ft")
	if err != nil {
		return err
	}
	ub, err := s.UnaccentVector(ctx, q, "x.ib", "x.fb")
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `
		INSERT INTO nexo_kb_sections (id, space_id, document_id, version_id, ordinal, anchor, heading_path, body, token_count,
			source_location, extraction, category, tags, valid_from, valid_until, recorded_from, recorded_until, is_current, tsv, body_sha256)
		SELECT x.id::uuid, $1::uuid, $2::uuid, $3::uuid, x.ord, x.anchor,
			CASE WHEN x.path = '' THEN '{}'::text[] ELSE string_to_array(x.path, E'\x1f') END,
			x.body, x.tok, x.loc::jsonb, x.extr, $4, $5::text[], $6, $7, $8, NULL, $9,
			setweight(to_tsvector('`+TSConfig+`', x.it) || `+ut+`, 'A') || setweight(to_tsvector('`+TSConfig+`', x.ib) || `+ub+`, 'B'),
			x.bh
		FROM unnest($10::text[], $11::bigint[], $12::text[], $13::text[], $14::text[], $15::bigint[], $16::text[], $17::text[], $18::text[], $19::text[], $20::text[], $21::text[], $22::text[])
			AS x(id, ord, anchor, path, body, tok, loc, extr, it, ib, ft, fb, bh)`,
		w.SpaceID, w.DocumentID, w.VersionID, w.Category, nonNil(w.Tags), w.ValidFrom, nullTime(w.ValidUntil), w.RecordedFrom, w.IsCurrent,
		ids, ords, anchors, paths, bodies, toks, locs, extr, idxTitles, idxBodies, foldTitles, foldBodies, hashes)
	return err
}

// CloseCurrentSections cierra las secciones vigentes de un documento cuando
// una versión nueva entra en vigor en from (o el documento se retira en
// from), según la regla bitemporal (design "Desviaciones", D-I3):
//
//   - una sección que empezaba a regir en from o después queda reemplazada en
//     el registro: recorded_until = now, is_current = false;
//   - una sección que regía antes de from deja de regir en from:
//     valid_until = LEAST(valid_until, from); sigue siendo "actual" solo si
//     todavía rige (valid_until > now), p. ej. cuando la versión nueva es de
//     vigencia futura.
func CloseCurrentSections(ctx context.Context, q Querier, docID string, from, now time.Time) error {
	_, err := q.ExecContext(ctx, `
		UPDATE nexo_kb_sections SET
			recorded_until = CASE WHEN valid_from >= $2 THEN $3 ELSE recorded_until END,
			valid_until    = CASE WHEN valid_from >= $2 THEN valid_until ELSE LEAST(COALESCE(valid_until, 'infinity'::timestamptz), $2) END,
			is_current     = CASE WHEN valid_from >= $2 THEN FALSE ELSE LEAST(COALESCE(valid_until, 'infinity'::timestamptz), $2) > $3 END
		WHERE document_id = $1::uuid AND is_current`, docID, from, now)
	return err
}

const sectionColumns = `s.id::text, s.ordinal, s.anchor, array_to_json(s.heading_path)::text, s.body, s.token_count, s.source_location::text,
	s.extraction, s.valid_from, s.valid_until, s.recorded_from, s.recorded_until, s.is_current`

func scanSection(row interface{ Scan(...any) error }) (domain.Section, error) {
	var sec domain.Section
	var hp, loc string
	var vu, ru sql.NullTime
	if err := row.Scan(&sec.ID, &sec.Ordinal, &sec.Anchor, &hp, &sec.Body, &sec.TokenCount, &loc, &sec.Extraction, &sec.ValidFrom, &vu,
		&sec.RecordedFrom, &ru, &sec.IsCurrent); err != nil {
		return sec, err
	}
	_ = json.Unmarshal([]byte(hp), &sec.HeadingPath)
	if sec.HeadingPath == nil {
		sec.HeadingPath = []string{}
	}
	sec.SourceLocation = domain.SourceLocation{}
	_ = json.Unmarshal([]byte(loc), &sec.SourceLocation)
	sec.ValidUntil, sec.RecordedUntil = timePtr(vu), timePtr(ru)
	sec.ValidFrom, sec.RecordedFrom = sec.ValidFrom.UTC(), sec.RecordedFrom.UTC()
	return sec, nil
}

// ListSections devuelve las secciones de una versión en orden.
func ListSections(ctx context.Context, q Querier, versionID string) ([]domain.Section, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+sectionColumns+` FROM nexo_kb_sections s WHERE s.version_id = $1::uuid ORDER BY s.ordinal`, versionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Section{}
	for rows.Next() {
		sec, err := scanSection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sec)
	}
	return out, rows.Err()
}

// GetSection devuelve una sección de una versión por ancla.
func GetSection(ctx context.Context, q Querier, versionID, anchor string) (domain.Section, bool, error) {
	sec, err := scanSection(q.QueryRowContext(ctx, `SELECT `+sectionColumns+` FROM nexo_kb_sections s WHERE s.version_id = $1::uuid AND s.anchor = $2`, versionID, anchor))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Section{}, false, nil
	}
	return sec, err == nil, err
}

// ─── Eventos y cambios ───────────────────────────────────────────────────────

// Actor es el autor de una escritura, tomado del token (NX-01).
type Actor struct {
	PrincipalID, TokenID, Label string
	Verified                    bool
	ClientIP, UserAgent, Claim  string
}

// EventWrite es un evento a registrar.
type EventWrite struct {
	SpaceID, SpaceSlug, Action, TargetKind, TargetID string
	Actor                                            Actor
	Details                                          map[string]any
	At                                               *time.Time
}

// InsertEvent registra un evento de solo inserción.
func InsertEvent(ctx context.Context, q Querier, e EventWrite) error {
	var details any
	if len(e.Details) > 0 {
		raw, err := json.Marshal(security.NexoRedactAuditMetadata(e.Details)) // NX-07: sin formatos de credencial
		if err != nil {
			return err
		}
		details = string(raw)
	}
	e.TargetID = security.NexoRedactExternal(e.TargetID, 0)
	e.Actor.UserAgent = security.NexoRedactExternal(e.Actor.UserAgent, 0)
	e.Actor.Claim = security.NexoRedactExternal(e.Actor.Claim, 0)
	label := security.NexoRedactExternal(e.Actor.Label, 0)
	if label == "" {
		label = "desconocido"
	}
	_, err := q.ExecContext(ctx, `
		INSERT INTO nexo_kb_events (occurred_at, space_id, space_slug, action, target_kind, target_id, actor_principal_id, actor_token_id,
			actor_label, actor_email, actor_verified, client_ip, user_agent, client_claim, details)
		VALUES (COALESCE($1, NOW()), NULLIF($2, '')::uuid, $3, $4, NULLIF($5, ''), NULLIF($6, ''), $7::bigint, $8::bigint, $9,
			(SELECT u.email FROM cloud_human_users u WHERE u.principal_id = $7::bigint), $10, NULLIF($11, ''), NULLIF($12, ''), NULLIF($13, ''), $14::jsonb)`,
		nullTime(e.At), e.SpaceID, e.SpaceSlug, e.Action, e.TargetKind, e.TargetID, nullBigint(e.Actor.PrincipalID), nullBigint(e.Actor.TokenID),
		label, e.Actor.Verified, e.Actor.ClientIP, e.Actor.UserAgent, e.Actor.Claim, details)
	return err
}

// InsertChange agrega una fila al feed de cambios (misma transacción).
func InsertChange(ctx context.Context, q Querier, spaceID, documentID, kind string, now time.Time) error {
	_, err := q.ExecContext(ctx, `INSERT INTO nexo_kb_changes (space_id, document_id, kind, occurred_at) VALUES ($1::uuid, NULLIF($2, '')::uuid, $3, $4)`,
		spaceID, documentID, kind, now)
	return err
}

// ListEvents lista eventos de un espacio (cursor = id anterior).
func ListEvents(ctx context.Context, q Querier, slug string, beforeID int64, action string, limit int) ([]domain.Event, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT id, occurred_at, space_slug, action, COALESCE(target_kind, ''), COALESCE(target_id, ''), COALESCE(actor_principal_id::text, ''),
			COALESCE(actor_token_id::text, ''), actor_label, COALESCE(actor_email, ''), actor_verified, COALESCE(client_ip, ''),
			COALESCE(user_agent, ''), COALESCE(client_claim, ''), COALESCE(details::text, '')
		FROM nexo_kb_events WHERE space_slug = $1 AND ($2 = 0 OR id < $2) AND ($3 = '' OR action = $3)
		ORDER BY id DESC LIMIT $4`, slug, beforeID, action, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Event{}
	for rows.Next() {
		var e domain.Event
		var details string
		if err := rows.Scan(&e.ID, &e.OccurredAt, &e.Space, &e.Action, &e.TargetKind, &e.TargetID, &e.ActorID, &e.ActorToken, &e.ActorLabel,
			&e.ActorEmail, &e.Verified, &e.ClientIP, &e.UserAgent, &e.ClientClaim, &details); err != nil {
			return nil, err
		}
		if details != "" {
			_ = json.Unmarshal([]byte(details), &e.Details)
		}
		// NX-07: filas históricas escritas antes de la redacción en escritura.
		e.TargetID, e.ActorLabel = security.NexoRedactAuditText(e.TargetID), security.NexoRedactAuditText(e.ActorLabel)
		e.UserAgent, e.ClientClaim = security.NexoRedactAuditText(e.UserAgent), security.NexoRedactAuditText(e.ClientClaim)
		e.Details = security.NexoRedactAuditMetadata(e.Details)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ─── Utilidades ──────────────────────────────────────────────────────────────

func timePtr(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	u := t.Time.UTC()
	return &u
}

func nullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return *t
}

func nullBigint(s string) any {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n <= 0 {
		return nil
	}
	return n
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
