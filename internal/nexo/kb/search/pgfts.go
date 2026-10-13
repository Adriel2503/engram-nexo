package search

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strconv"
	"strings"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/ingest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/store"
)

// PGFTS es el Retriever de texto de Postgres: websearch_to_tsquery sobre
// nexo_kb_sections.tsv y extracto con ts_headline. Sin JOIN para el ranking:
// el filtro de espacio, vigencia, categoría y etiqueta se resuelve en la tabla
// de secciones (columnas desnormalizadas); los datos del documento se agregan
// solo a los resultados finales.
//
// Ranking en dos etapas (RNF-K1, design "Desviaciones", D-I5): ts_rank
// (frecuencia con pesos: títulos A, cuerpo B) sobre los candidatos, y
// ts_rank_cd (densidad de cobertura: cercanía de los términos) solo sobre los
// 100 mejores. ts_rank_cd sobre todas las coincidencias cuesta unas 25 veces
// más. Los candidatos se cortan en CandidateCap: un término presente en
// decenas de miles de secciones casi no discrimina, y el corte acota el peor
// caso.
type PGFTS struct {
	st  *store.Store
	cap int
}

// EnvCandidateCap fija el tope de candidatos por consulta.
const EnvCandidateCap = "NEXO_KB_RANK_CANDIDATES"

// DefaultCandidateCap es el tope de candidatos por defecto.
const DefaultCandidateCap = 20000

// NewPGFTS crea el motor sobre el almacén.
func NewPGFTS(st *store.Store) *PGFTS {
	c := DefaultCandidateCap
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(EnvCandidateCap))); err == nil && n >= 100 {
		c = n
	}
	return &PGFTS{st: st, cap: c}
}

// Name implementa Retriever.
func (p *PGFTS) Name() string { return store.IndexKindPGFTS }

// El atajo de vigentes usa el índice GIN parcial (is_current) y la hora de la
// base; la consulta general aplica ambos relojes sobre todas las versiones.
const pgftsCurrentWhere = `s.is_current
	AND s.valid_from <= now() AND (s.valid_until IS NULL OR s.valid_until > now())`

const pgftsTemporalWhere = `s.recorded_from <= $4 AND (s.recorded_until IS NULL OR s.recorded_until > $4)
	AND s.valid_from <= $3 AND (s.valid_until IS NULL OR s.valid_until > $3)`

const pgftsQuery = `
WITH strict AS (SELECT websearch_to_tsquery('` + store.TSConfig + `', $1) || %UNACCENT% AS query),
q AS (SELECT %MODE% AS query FROM strict),
cand AS (
	SELECT s.id, s.tsv
	FROM nexo_kb_sections s, q
	WHERE s.tsv @@ q.query
	  AND s.space_id = ANY($2::uuid[])
	  AND %s
	  AND ($5 = '' OR s.category = $5)
	  AND ($6 = '' OR s.tags @> ARRAY[$6]::text[])
	LIMIT %CAP%
),
top AS (
	SELECT c.id, c.tsv, ts_rank(c.tsv, q.query, 1) AS r1
	FROM cand c, q
	ORDER BY r1 DESC, c.id
	LIMIT 100
),
hits AS (
	SELECT t.id, ts_rank_cd(t.tsv, q.query, 1) + t.r1 AS rank
	FROM top t, q
	ORDER BY rank DESC, t.id
	LIMIT $7
)
SELECT s.id::text, s.space_id::text, sp.slug, d.id::text, d.doc_key, v.title, s.category, array_to_json(s.tags)::text, v.owner_label,
	v.version, s.anchor, array_to_json(s.heading_path)::text,
	ts_headline('` + store.TSConfig + `', s.body, q.query, 'MaxFragments=2, MaxWords=35, MinWords=12, StartSel=**, StopSel=**, FragmentDelimiter=" ... "'),
	s.source_location::text, s.extraction, s.valid_from, s.valid_until, s.recorded_from, s.recorded_until,
	d.status, COALESCE(sup.doc_key, ''), h.rank
FROM hits h
JOIN nexo_kb_sections s ON s.id = h.id
JOIN nexo_kb_versions v ON v.id = s.version_id
JOIN nexo_kb_documents d ON d.id = s.document_id
JOIN nexo_kb_spaces sp ON sp.id = s.space_id
LEFT JOIN nexo_kb_documents sup ON sup.id = d.superseded_by
CROSS JOIN q
ORDER BY h.rank DESC, s.id`

// Search implementa Retriever.
func (p *PGFTS) Search(ctx context.Context, q Query, f Filter) ([]Hit, error) {
	unacc, err := p.st.UnaccentQuery(ctx, "$1", "$8")
	if err != nil {
		return nil, err
	}
	where := pgftsTemporalWhere
	if f.Current() {
		where = pgftsCurrentWhere + ` AND $3::timestamptz IS NOT NULL AND $4::timestamptz IS NOT NULL`
	}
	base := strings.Replace(strings.Replace(pgftsQuery, "%s", where, 1), "%UNACCENT%", unacc, 1)
	base = strings.Replace(base, "%CAP%", strconv.Itoa(p.cap), 1)
	args := []any{q.Text, f.SpaceIDs(), f.At(), f.KnownAt(), strings.TrimSpace(q.Category), strings.TrimSpace(q.Tag), q.Limit}
	if strings.Contains(unacc, "$8") {
		args = append(args, ingest.Fold(q.Text))
	}
	// 1. Todos los términos (AND de websearch_to_tsquery).
	out, err := p.run(ctx, strings.Replace(base, "%MODE%", "strict.query", 1), args, MatchAllTerms)
	if err != nil || len(out) >= q.Limit {
		return out, err
	}
	// 2. Preguntas en lenguaje natural: si faltan resultados, cualquiera de
	// los términos (OR), ordenado por ts_rank_cd (más términos y más cerca,
	// mejor). No aplica si la consulta tiene exclusiones (-palabra).
	args[6] = q.Limit + len(out)
	loose, err := p.run(ctx, strings.Replace(base, "%MODE%", pgftsLooseQuery, 1), args, MatchSomeTerms)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, h := range out {
		seen[h.SectionID] = true
	}
	for _, h := range loose {
		if len(out) >= q.Limit {
			break
		}
		if !seen[h.SectionID] {
			out = append(out, h)
		}
	}
	return out, nil
}

// pgftsLooseQuery convierte los AND de la consulta estricta en OR.
const pgftsLooseQuery = `CASE WHEN position('!' IN strict.query::text) > 0 THEN ''::tsquery ELSE replace(strict.query::text, '&', '|')::tsquery END`

func (p *PGFTS) run(ctx context.Context, query string, args []any, match string) ([]Hit, error) {
	rows, err := p.st.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var h Hit
		var tags, hp, loc string
		var vu, ru sql.NullTime
		if err := rows.Scan(&h.SectionID, &h.SpaceID, &h.Space, &h.DocumentID, &h.DocKey, &h.Title, &h.Category, &tags, &h.Owner,
			&h.Version, &h.Anchor, &hp, &h.Excerpt, &loc, &h.Extraction, &h.ValidFrom, &vu, &h.RecordedFrom, &ru,
			&h.DocStatus, &h.SupersededBy, &h.Rank); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(tags), &h.Tags)
		_ = json.Unmarshal([]byte(hp), &h.HeadingPath)
		if h.Tags == nil {
			h.Tags = []string{}
		}
		if h.HeadingPath == nil {
			h.HeadingPath = []string{}
		}
		h.SourceLocation = domain.SourceLocation{}
		_ = json.Unmarshal([]byte(loc), &h.SourceLocation)
		if vu.Valid {
			t := vu.Time.UTC()
			h.ValidUntil = &t
		}
		if ru.Valid {
			t := ru.Time.UTC()
			h.RecordedUntil = &t
		}
		h.ValidFrom, h.RecordedFrom = h.ValidFrom.UTC(), h.RecordedFrom.UTC()
		h.Match = match
		out = append(out, h)
	}
	return out, rows.Err()
}
