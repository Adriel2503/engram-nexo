package search

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"os"
	"regexp"
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
//
// Entrega 2b (R1, todo activable por opciones, ver PGFTSOptions):
//   - sinónimos y siglas (RC-06) en la segunda pasada (cualquier término);
//   - cobertura ponderada por rareza de cada resultado (Hit.Coverage) para el
//     umbral de "no hay estándar" del Searcher (RC-07);
//   - modo exact (RC-22): literal o expresión regular RE2;
//   - cita textual (IN-07): source_location.quote.
type PGFTS struct {
	st   *store.Store
	cap  int
	opts PGFTSOptions
}

// PGFTSOptions son las opciones de R1 del motor.
type PGFTSOptions struct {
	// Synonyms devuelve sinónimos y siglas por término (clave en minúsculas
	// sin tildes; valores: términos o frases equivalentes). nil = sin
	// expansión.
	Synonyms SynonymFunc `json:"-"`
	// Coverage calcula la cobertura ponderada de cada resultado (necesaria
	// para el umbral del Searcher).
	Coverage bool `json:"coverage"`
	// Quote agrega source_location.quote (máximo QuoteMax caracteres).
	Quote bool `json:"quote"`
}

// SynonymFunc devuelve el diccionario de sinónimos de los espacios.
type SynonymFunc func(ctx context.Context, spaceIDs []string) map[string][]string

// QuoteMax es el largo máximo de la cita textual (IN-07).
const QuoteMax = 300

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

// SetOptions fija las opciones de R1 (antes de atender consultas).
func (p *PGFTS) SetOptions(o PGFTSOptions) { p.opts = o }

// Config describe la configuración (se guarda en las corridas de evaluación).
func (p *PGFTS) Config() map[string]any {
	return map[string]any{"candidate_cap": p.cap, "synonyms": p.opts.Synonyms != nil, "coverage": p.opts.Coverage, "quote": p.opts.Quote}
}

// Name implementa Retriever.
func (p *PGFTS) Name() string { return store.IndexKindPGFTS }

// El atajo de vigentes usa el índice GIN parcial (is_current) y la hora de la
// base; la consulta general aplica ambos relojes sobre todas las versiones.
const pgftsCurrentWhere = `s.is_current
	AND s.valid_from <= now() AND (s.valid_until IS NULL OR s.valid_until > now())`

const pgftsTemporalWhere = `s.recorded_from <= $4 AND (s.recorded_until IS NULL OR s.recorded_until > $4)
	AND s.valid_from <= $3 AND (s.valid_until IS NULL OR s.valid_until > $3)`

// Parámetros: $1 texto, $2 espacios, $3 at, $4 known_at, $5 categoría, $6
// etiqueta, $7 límite, $8 texto sin tildes, $9 expansión de sinónimos (texto
// websearch, ” = ninguna), $10 lexemas de la consulta (cobertura).
const pgftsQuery = `
WITH strict AS (SELECT websearch_to_tsquery('` + store.TSConfig + `', $1) || %UNACCENT% AS query, $8::text AS folded),
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
	d.status, COALESCE(sup.doc_key, ''), h.rank, COALESCE(v.origin->>'source_url', ''),
	array_to_json(ARRAY(SELECT l FROM unnest($10::text[]) l WHERE s.tsv @@ to_tsquery('simple', quote_literal(l))))::text,
	left(s.body, 1500)
FROM hits h
JOIN nexo_kb_sections s ON s.id = h.id
JOIN nexo_kb_versions v ON v.id = s.version_id
JOIN nexo_kb_documents d ON d.id = s.document_id
JOIN nexo_kb_spaces sp ON sp.id = s.space_id
LEFT JOIN nexo_kb_documents sup ON sup.id = d.superseded_by
CROSS JOIN q
ORDER BY h.rank DESC, s.id`

// pgftsLooseQuery convierte los AND de la consulta estricta en OR y suma la
// expansión de sinónimos.
const pgftsLooseQuery = `(CASE WHEN position('!' IN strict.query::text) > 0 THEN ''::tsquery ELSE replace(strict.query::text, '&', '|')::tsquery END)
	|| (CASE WHEN $9 = '' THEN ''::tsquery ELSE websearch_to_tsquery('` + store.TSConfig + `', $9) END)`

// Search implementa Retriever.
func (p *PGFTS) Search(ctx context.Context, q Query, f Filter) ([]Hit, error) {
	if strings.EqualFold(strings.TrimSpace(q.Mode), ModeExact) {
		return p.exactSearch(ctx, q, f)
	}
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
	expansion := ""
	var syn map[string][]string
	if p.opts.Synonyms != nil {
		syn = p.opts.Synonyms(ctx, f.SpaceIDs())
		expansion = ExpandSynonyms(q.Text, syn)
	}
	var lexemes []string
	var weights *coverageModel
	if p.opts.Coverage {
		weights, err = p.coverage(ctx, q.Text, f.SpaceIDs(), syn)
		if err != nil {
			return nil, err
		}
		lexemes = weights.all
	}
	args := []any{q.Text, f.SpaceIDs(), f.At(), f.KnownAt(), strings.TrimSpace(q.Category), strings.TrimSpace(q.Tag), q.Limit, ingest.Fold(q.Text),
		expansion, nonNilStrings(lexemes)}
	// 1. Todos los términos (AND de websearch_to_tsquery). La expansión no se
	// usa aquí ($9 se referencia solo en la segunda pasada).
	strictSQL := strings.Replace(base, "%MODE%", "strict.query", 1)
	strictSQL = strings.Replace(strictSQL, "$8::text AS folded", "$8::text AS folded, $9::text AS expansion", 1)
	out, err := p.run(ctx, strictSQL, args, MatchAllTerms, weights)
	if err != nil || len(out) >= q.Limit {
		return out, err
	}
	// 2. Preguntas en lenguaje natural: si faltan resultados, cualquiera de
	// los términos (OR) más sus sinónimos, ordenado por ts_rank_cd (más
	// términos y más cerca, mejor). No aplica si la consulta tiene
	// exclusiones (-palabra).
	args[6] = q.Limit + len(out)
	loose, err := p.run(ctx, strings.Replace(base, "%MODE%", pgftsLooseQuery, 1), args, MatchSomeTerms, weights)
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

// coverageModel es la cobertura por concepto (RC-07): cada palabra de la
// consulta es un concepto, con sus lexemas y los de sus sinónimos; pesa por
// su rareza en las secciones vigentes de los espacios, ln(1 + N/(df+1)), con
// df = el mayor entre la palabra y sus sinónimos (un término que no existe en
// los espacios, ni por sinónimo, pesa lo máximo: señal de "no hay estándar").
type coverageModel struct {
	weights map[int]float64  // concepto -> peso
	alias   map[string][]int // lexema -> conceptos
	all     []string         // lexemas a comprobar en cada resultado
}

var wordRe = regexp.MustCompile(`[\p{L}\p{N}][\p{L}\p{N}#+._-]*`)

func (p *PGFTS) coverage(ctx context.Context, text string, spaceIDs []string, syn map[string][]string) (*coverageModel, error) {
	var terms []string
	var owner []int64
	words := wordRe.FindAllString(text, 40)
	for i, w := range words {
		terms, owner = append(terms, w), append(owner, int64(i))
		for _, alt := range syn[normTerm(w)] {
			terms, owner = append(terms, alt), append(owner, int64(i))
		}
	}
	m := &coverageModel{weights: map[int]float64{}, alias: map[string][]int{}}
	if len(terms) == 0 {
		return m, nil
	}
	rows, err := p.st.DB.QueryContext(ctx, `
		WITH t AS (SELECT w, o FROM unnest($1::text[], $3::bigint[]) AS t(w, o)),
		lx AS (SELECT DISTINCT t.o, x.lexeme AS l FROM t, unnest(to_tsvector('`+store.TSConfig+`', t.w)) x),
		n AS (SELECT count(*) AS n FROM nexo_kb_sections s WHERE s.space_id = ANY($2::uuid[]) AND s.is_current)
		SELECT lx.o, lx.l, (SELECT count(*) FROM (SELECT 1 FROM nexo_kb_sections s WHERE s.space_id = ANY($2::uuid[]) AND s.is_current
			AND s.tsv @@ to_tsquery('simple', quote_literal(lx.l)) LIMIT 5000) x), n.n
		FROM lx, n`, terms, spaceIDs, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	df := map[int]int64{}
	var n int64 = 1
	seen := map[string]bool{}
	for rows.Next() {
		var o, d, total int64
		var l string
		if err := rows.Scan(&o, &l, &d, &total); err != nil {
			return nil, err
		}
		if total > 0 {
			n = total
		}
		c := int(o)
		if cur, ok := df[c]; !ok || d > cur {
			df[c] = d
		}
		m.alias[l] = append(m.alias[l], c)
		if !seen[l] {
			seen[l] = true
			m.all = append(m.all, l)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for c, d := range df {
		m.weights[c] = math.Log(1 + float64(n)/float64(d+1))
	}
	return m, nil
}

// score es la cobertura ponderada de un resultado según sus lexemas.
func (m *coverageModel) score(matched []string) float64 {
	var total, got float64
	for _, w := range m.weights {
		total += w
	}
	if total == 0 {
		return 1
	}
	hit := map[int]bool{}
	for _, l := range matched {
		for _, c := range m.alias[l] {
			hit[c] = true
		}
	}
	for c := range hit {
		got += m.weights[c]
	}
	return got / total
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (p *PGFTS) run(ctx context.Context, query string, args []any, match string, weights *coverageModel) ([]Hit, error) {
	rows, err := p.st.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var h Hit
		var tags, hp, loc, matched, body string
		var vu, ru sql.NullTime
		if err := rows.Scan(&h.SectionID, &h.SpaceID, &h.Space, &h.DocumentID, &h.DocKey, &h.Title, &h.Category, &tags, &h.Owner,
			&h.Version, &h.Anchor, &hp, &h.Excerpt, &loc, &h.Extraction, &h.ValidFrom, &vu, &h.RecordedFrom, &ru,
			&h.DocStatus, &h.SupersededBy, &h.Rank, &h.SourceURL, &matched, &body); err != nil {
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
		h.Coverage = -1
		if weights != nil {
			var m []string
			_ = json.Unmarshal([]byte(matched), &m)
			h.Coverage = weights.score(m)
			if match == MatchAllTerms {
				h.Coverage = 1
			}
		}
		if p.opts.Quote {
			h.SourceLocation["quote"] = Quote(body)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// Quote es la cita textual de una sección (IN-07): el texto sin la línea de
// título, en una sola línea, de hasta QuoteMax caracteres cortado en una
// palabra.
func Quote(body string) string {
	lines := strings.Split(body, "\n")
	if len(lines) > 1 && strings.HasPrefix(strings.TrimSpace(lines[0]), "#") {
		lines = lines[1:]
	}
	s := strings.Join(strings.Fields(strings.Join(lines, " ")), " ")
	r := []rune(s)
	if len(r) <= QuoteMax {
		return s
	}
	cut := QuoteMax - 3
	for cut > QuoteMax/2 && r[cut] != ' ' {
		cut--
	}
	return strings.TrimSpace(string(r[:cut])) + "..."
}
