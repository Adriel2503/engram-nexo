package search

// Nexo IA · NX-10 entrega 2b (RC-22, HU-05 c4) — modo exact: literal o
// expresión regular dentro de los espacios visibles, con los mismos filtros
// comunes. La expresión se evalúa con RE2 de Go (tiempo lineal, sin
// retroceso); Postgres solo preselecciona por un literal con ILIKE (nunca
// ejecuta la expresión del usuario).

import (
	"context"
	"database/sql"
	"encoding/json"
	"regexp"
	"regexp/syntax"
	"sort"
	"strconv"
	"strings"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
)

// Modos de búsqueda.
const (
	ModeFTS   = "fts"
	ModeExact = "exact"
)

// MatchExact marca los resultados del modo exact.
const MatchExact = "exact"

// Límites del modo exact.
const (
	maxPatternLen   = 200
	exactCandidates = 2000
)

// ExactPattern es un patrón del modo exact.
type ExactPattern struct {
	Literal string         // preselección (ILIKE)
	Re      *regexp.Regexp // nil = literal
}

// ParseExact interpreta el texto: /expresión/ es una expresión regular (sin
// distinguir mayúsculas); cualquier otro texto es un literal. Una expresión
// necesita un literal de 3 caracteres o más para preseleccionar.
func ParseExact(text string) (ExactPattern, error) {
	t := strings.TrimSpace(text)
	if len(t) > maxPatternLen {
		return ExactPattern{}, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "exact pattern is longer than %d characters", maxPatternLen)
	}
	if len(t) >= 3 && strings.HasPrefix(t, "/") && strings.HasSuffix(t, "/") {
		expr := t[1 : len(t)-1]
		re, err := regexp.Compile("(?i)" + expr)
		if err != nil {
			return ExactPattern{}, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "invalid regular expression: %v", err)
		}
		lit := longestLiteral(expr)
		if len([]rune(lit)) < 3 {
			return ExactPattern{}, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "the regular expression needs a literal of 3 or more characters (for example /Polly.*Retry/)")
		}
		return ExactPattern{Literal: lit, Re: re}, nil
	}
	if len([]rune(t)) < 2 {
		return ExactPattern{}, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "exact text needs 2 or more characters")
	}
	return ExactPattern{Literal: t}, nil
}

// longestLiteral devuelve el literal más largo que toda coincidencia contiene.
func longestLiteral(expr string) string {
	re, err := syntax.Parse(expr, syntax.Perl)
	if err != nil {
		return ""
	}
	best := ""
	var walk func(r *syntax.Regexp, required bool)
	walk = func(r *syntax.Regexp, required bool) {
		switch r.Op {
		case syntax.OpLiteral:
			if required {
				if s := string(r.Rune); len([]rune(s)) > len([]rune(best)) {
					best = s
				}
			}
		case syntax.OpConcat, syntax.OpCapture:
			for _, s := range r.Sub {
				walk(s, required)
			}
		case syntax.OpPlus:
			for _, s := range r.Sub {
				walk(s, required)
			}
		default:
			// Alternativas, opcionales y repeticiones de cero: no obligatorias.
		}
	}
	walk(re.Simplify(), true)
	return best
}

func likeEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// exactSearch busca el literal o la expresión. Orden: más apariciones
// primero, luego documento y posición.
func (p *PGFTS) exactSearch(ctx context.Context, q Query, f Filter) ([]Hit, error) {
	pat, err := ParseExact(q.Text)
	if err != nil {
		return nil, err
	}
	where := pgftsTemporalWhere
	if f.Current() {
		where = pgftsCurrentWhere + ` AND $3::timestamptz IS NOT NULL AND $4::timestamptz IS NOT NULL`
	}
	rows, err := p.st.DB.QueryContext(ctx, `
		SELECT s.id::text, s.space_id::text, sp.slug, d.id::text, d.doc_key, v.title, s.category, array_to_json(s.tags)::text, v.owner_label,
			v.version, s.anchor, array_to_json(s.heading_path)::text, s.source_location::text, s.extraction, s.valid_from, s.valid_until,
			s.recorded_from, s.recorded_until, d.status, COALESCE(sup.doc_key, ''), COALESCE(v.origin->>'source_url', ''), s.body, s.ordinal
		FROM nexo_kb_sections s
		JOIN nexo_kb_versions v ON v.id = s.version_id
		JOIN nexo_kb_documents d ON d.id = s.document_id
		JOIN nexo_kb_spaces sp ON sp.id = s.space_id
		LEFT JOIN nexo_kb_documents sup ON sup.id = d.superseded_by
		WHERE s.space_id = ANY($2::uuid[]) AND `+where+`
		  AND ($5 = '' OR s.category = $5) AND ($6 = '' OR s.tags @> ARRAY[$6]::text[])
		  AND s.body ILIKE '%' || $1 || '%' ESCAPE '\'
		LIMIT `+strconv.Itoa(exactCandidates), likeEscape(pat.Literal), f.SpaceIDs(), f.At(), f.KnownAt(), strings.TrimSpace(q.Category), strings.TrimSpace(q.Tag))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type cand struct {
		h       Hit
		ordinal int
	}
	var cands []cand
	lower := strings.ToLower(pat.Literal)
	for rows.Next() {
		var h Hit
		var tags, hp, loc, body string
		var vu, ru sql.NullTime
		var ord int
		if err := rows.Scan(&h.SectionID, &h.SpaceID, &h.Space, &h.DocumentID, &h.DocKey, &h.Title, &h.Category, &tags, &h.Owner,
			&h.Version, &h.Anchor, &hp, &loc, &h.Extraction, &h.ValidFrom, &vu, &h.RecordedFrom, &ru, &h.DocStatus, &h.SupersededBy,
			&h.SourceURL, &body, &ord); err != nil {
			return nil, err
		}
		var idx [][]int
		if pat.Re != nil {
			idx = pat.Re.FindAllStringIndex(body, 100)
		} else {
			lb := strings.ToLower(body)
			for off := 0; len(idx) < 100; {
				i := strings.Index(lb[off:], lower)
				if i < 0 {
					break
				}
				idx = append(idx, []int{off + i, off + i + len(lower)})
				off += i + len(lower)
			}
		}
		if len(idx) == 0 {
			continue
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
		h.Match, h.Coverage = MatchExact, 1
		h.Rank = float64(len(idx))
		// En el título de la sección pesa más.
		if strings.Contains(strings.ToLower(strings.Join(h.HeadingPath, " ")), lower) {
			h.Rank += 5
		}
		h.Excerpt = snippet(body, idx[0][0], idx[0][1])
		if p.opts.Quote {
			h.SourceLocation["quote"] = Quote(body)
		}
		cands = append(cands, cand{h: h, ordinal: ord})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if a.h.Rank != b.h.Rank {
			return a.h.Rank > b.h.Rank
		}
		if a.h.DocKey != b.h.DocKey {
			return a.h.DocKey < b.h.DocKey
		}
		return a.ordinal < b.ordinal
	})
	out := make([]Hit, 0, q.Limit)
	for _, c := range cands {
		if len(out) == q.Limit {
			break
		}
		out = append(out, c.h)
	}
	return out, nil
}

// snippet arma un extracto alrededor de la coincidencia, marcada con **.
func snippet(body string, start, end int) string {
	from, to := start-120, end+120
	if from < 0 {
		from = 0
	}
	if to > len(body) {
		to = len(body)
	}
	for from > 0 && !isRuneStart(body[from]) {
		from--
	}
	for to < len(body) && !isRuneStart(body[to]) {
		to++
	}
	s := body[from:start] + "**" + body[start:end] + "**" + body[end:to]
	s = strings.Join(strings.Fields(s), " ")
	if from > 0 {
		s = "... " + s
	}
	if to < len(body) {
		s += " ..."
	}
	return s
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
