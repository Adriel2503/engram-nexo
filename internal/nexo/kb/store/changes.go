package store

// Nexo IA · NX-10 entrega 2b — "qué cambió desde X" (RC-27, HU-38) y feed
// de la copia local (C-09, KP-07).

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Relojes de la consulta de cambios.
const (
	ClockValid    = "valid"    // lo que regía en cada fecha, según lo publicado hoy
	ClockRecorded = "recorded" // lo que estaba publicado y regía en cada fecha
)

// SectionChange es una sección agregada, modificada o retirada entre dos
// fechas (misma ancla con otro hash de cuerpo = modificada).
type SectionChange struct {
	Space         string     `json:"space"`
	DocKey        string     `json:"doc_key"`
	Title         string     `json:"title"`
	Anchor        string     `json:"anchor"`
	HeadingPath   []string   `json:"heading_path"`
	Change        string     `json:"change"` // added | modified | removed
	VersionBefore int        `json:"version_before,omitempty"`
	VersionAfter  int        `json:"version_after,omitempty"`
	At            *time.Time `json:"at,omitempty"` // desde cuándo rige el cambio (o deja de regir)
	DocStatus     string     `json:"document_status"`
	SupersededBy  string     `json:"superseded_by,omitempty"`
}

func snapshotWhere(clock, at string) string {
	known := `s.recorded_from <= now() AND (s.recorded_until IS NULL OR s.recorded_until > now())`
	if clock == ClockRecorded {
		known = `s.recorded_from <= ` + at + ` AND (s.recorded_until IS NULL OR s.recorded_until > ` + at + `)`
	}
	return known + ` AND s.valid_from <= ` + at + ` AND (s.valid_until IS NULL OR s.valid_until > ` + at + `)`
}

// ListSectionChanges compara las secciones de los espacios entre since y
// until. Devuelve como máximo limit filas (truncated si hay más).
func ListSectionChanges(ctx context.Context, q Querier, spaceIDs []string, since, until time.Time, clock string, limit int) ([]SectionChange, bool, error) {
	if clock != ClockRecorded {
		clock = ClockValid
	}
	hash := `COALESCE(s.body_sha256, encode(sha256(convert_to(s.body, 'UTF8')), 'hex'))`
	from := func(alias, at string) string {
		return `SELECT s.document_id, s.anchor, ` + hash + ` AS h, v.version, s.heading_path, s.valid_from, s.valid_until, s.recorded_from, s.recorded_until
			FROM nexo_kb_sections s JOIN nexo_kb_versions v ON v.id = s.version_id
			WHERE s.space_id = ANY($1::uuid[]) AND ` + snapshotWhere(clock, at)
	}
	dateAfter, dateBefore := `b.valid_from`, `a.valid_until`
	if clock == ClockRecorded {
		dateAfter, dateBefore = `b.recorded_from`, `COALESCE(a.recorded_until, a.valid_until)`
	}
	rows, err := q.QueryContext(ctx, `
		WITH a AS (`+from("a", "$2")+`), b AS (`+from("b", "$3")+`),
		diff AS (
			SELECT COALESCE(a.document_id, b.document_id) AS document_id, COALESCE(a.anchor, b.anchor) AS anchor,
				COALESCE(b.heading_path, a.heading_path) AS heading_path,
				CASE WHEN a.document_id IS NULL THEN 'added' WHEN b.document_id IS NULL THEN 'removed' ELSE 'modified' END AS change,
				a.version AS vb, b.version AS va,
				CASE WHEN b.document_id IS NOT NULL THEN `+dateAfter+` ELSE `+dateBefore+` END AS at
			FROM a FULL OUTER JOIN b ON a.document_id = b.document_id AND a.anchor = b.anchor
			WHERE a.document_id IS NULL OR b.document_id IS NULL OR a.h <> b.h)
		SELECT sp.slug, d.doc_key, d.title, x.anchor, array_to_json(x.heading_path)::text, x.change, COALESCE(x.vb, 0), COALESCE(x.va, 0), x.at,
			d.status, COALESCE(sup.doc_key, '')
		FROM diff x JOIN nexo_kb_documents d ON d.id = x.document_id JOIN nexo_kb_spaces sp ON sp.id = d.space_id
		LEFT JOIN nexo_kb_documents sup ON sup.id = d.superseded_by
		ORDER BY sp.slug, d.doc_key, x.change, x.anchor
		LIMIT $4`, spaceIDs, since, until, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := []SectionChange{}
	for rows.Next() {
		var c SectionChange
		var hp string
		var at *time.Time
		if err := rows.Scan(&c.Space, &c.DocKey, &c.Title, &c.Anchor, &hp, &c.Change, &c.VersionBefore, &c.VersionAfter, &at, &c.DocStatus, &c.SupersededBy); err != nil {
			return nil, false, err
		}
		_ = json.Unmarshal([]byte(hp), &c.HeadingPath)
		if at != nil {
			u := at.UTC()
			c.At = &u
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	truncated := len(out) > limit
	if truncated {
		out = out[:limit]
	}
	return out, truncated, nil
}

// ─── Feed de la copia local (C-09) ───────────────────────────────────────────

// FeedChange es una fila de nexo_kb_changes.
type FeedChange struct {
	Seq        int64     `json:"seq"`
	SpaceID    string    `json:"space_id"`
	DocumentID string    `json:"document_id,omitempty"`
	Kind       string    `json:"kind"`
	OccurredAt time.Time `json:"occurred_at"`
}

// ListFeedChanges devuelve los cambios con seq > since en los espacios dados.
func ListFeedChanges(ctx context.Context, q Querier, spaceIDs []string, since int64, limit int) ([]FeedChange, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT seq, space_id::text, COALESCE(document_id::text, ''), kind, occurred_at
		FROM nexo_kb_changes WHERE seq > $1 AND space_id = ANY($2::uuid[]) ORDER BY seq LIMIT $3`, since, spaceIDs, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FeedChange
	for rows.Next() {
		var c FeedChange
		if err := rows.Scan(&c.Seq, &c.SpaceID, &c.DocumentID, &c.Kind, &c.OccurredAt); err != nil {
			return nil, err
		}
		c.OccurredAt = c.OccurredAt.UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

// MaxSeq es el último seq del feed (o 0).
func MaxSeq(ctx context.Context, q Querier) (int64, error) {
	var n int64
	err := q.QueryRowContext(ctx, `SELECT COALESCE(max(seq), 0) FROM nexo_kb_changes`).Scan(&n)
	return n, err
}

// FeedSection es una sección vigente o programada para la copia local.
type FeedSection struct {
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
}

// OpenSections devuelve las secciones del documento que rigen hoy o regirán
// (registro abierto y vigencia no vencida): la vigente y las programadas,
// cada una con su vigencia (KP-07: la copia local filtra al consultar).
func OpenSections(ctx context.Context, q Querier, documentID string) ([]FeedSection, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT v.version, s.anchor, s.ordinal, array_to_json(s.heading_path)::text, s.body,
			COALESCE(s.body_sha256, encode(sha256(convert_to(s.body, 'UTF8')), 'hex')), s.source_location::text, s.extraction, s.valid_from, s.valid_until
		FROM nexo_kb_sections s JOIN nexo_kb_versions v ON v.id = s.version_id
		WHERE s.document_id = $1::uuid AND s.recorded_until IS NULL AND (s.valid_until IS NULL OR s.valid_until > now())
		ORDER BY v.version, s.ordinal`, documentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FeedSection{}
	for rows.Next() {
		var f FeedSection
		var hp, loc string
		var vu *time.Time
		if err := rows.Scan(&f.Version, &f.Anchor, &f.Ordinal, &hp, &f.Body, &f.BodySHA256, &loc, &f.Extraction, &f.ValidFrom, &vu); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(hp), &f.HeadingPath)
		_ = json.Unmarshal([]byte(loc), &f.SourceLocation)
		f.ValidFrom = f.ValidFrom.UTC()
		if vu != nil {
			u := vu.UTC()
			f.ValidUntil = &u
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// GetDocumentByID devuelve un documento por id.
func GetDocumentByID(ctx context.Context, q Querier, id string) (DocRow, bool, error) {
	d, err := scanDoc(q.QueryRowContext(ctx, `SELECT `+docColumns+docFrom+` WHERE d.id = $1::uuid`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return DocRow{}, false, nil
	}
	return d, err == nil, err
}

// Neighbor es una sección vecina (RC-10).
type Neighbor struct {
	Anchor      string
	HeadingPath []string
	Body        string
	Position    string
}

// NeighborSections devuelve la sección anterior y la siguiente de la misma
// versión (contexto opcional de un resultado).
func NeighborSections(ctx context.Context, q Querier, sectionID string) ([]Neighbor, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT n.anchor, array_to_json(n.heading_path)::text, left(n.body, 4000), CASE WHEN n.ordinal < s.ordinal THEN 'previous' ELSE 'next' END
		FROM nexo_kb_sections s JOIN nexo_kb_sections n ON n.version_id = s.version_id AND n.ordinal IN (s.ordinal - 1, s.ordinal + 1)
		WHERE s.id = $1::uuid ORDER BY n.ordinal`, sectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Neighbor
	for rows.Next() {
		var n Neighbor
		var hp string
		if err := rows.Scan(&n.Anchor, &hp, &n.Body, &n.Position); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(hp), &n.HeadingPath)
		out = append(out, n)
	}
	return out, rows.Err()
}
