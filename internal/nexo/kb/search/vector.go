package search

// Nexo IA · NX-10 R2 (RC-19, RC-23) — búsqueda por vectores en pgvector con
// los filtros comunes (espacios visibles, vigencia y registro, categoría,
// etiqueta) dentro de la misma consulta.
//
// Dos recorridos:
//
//   - HNSW (lo vigente en espacios grandes): subconsulta con LIMIT ampliado
//     (100 a 200 candidatos), ORDER BY sobre la misma expresión que el índice
//     parcial (embedding::vector(D)), SET LOCAL hnsw.ef_search y, con
//     pgvector >= 0.8, hnsw.iterative_scan = relaxed_order (el filtro por
//     espacio y vigencia no deja huecos: HNSW sigue buscando hasta llenar el
//     LIMIT). Patrón de WeKnora retriever/postgres/repository.go:291-469.
//   - Exacto (conjunto filtrado pequeño o consulta histórica con at /
//     known_at): primero las secciones que pasan los filtros (CTE
//     materializada) y luego la distancia de cada una. Recall perfecto.

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/embed"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/store"
)

// VectorBackend es el índice vectorial activo y el Embedder de su modelo.
type VectorBackend struct {
	Index    store.VectorIndex
	Embedder embed.Embedder
	Support  store.VectorSupport
}

// VectorOptions ajusta la consulta vectorial.
type VectorOptions struct {
	// Candidates es el LIMIT de la subconsulta HNSW (y ef_search).
	Candidates int `json:"candidates"`
	// ExactMax: con a lo sumo esta cantidad de secciones vigentes en los
	// espacios visibles se usa el recorrido exacto.
	ExactMax int `json:"exact_max"`
	// Iterative: relaxed_order, strict_order u off (pgvector >= 0.8).
	Iterative string `json:"iterative_scan"`
}

// DefaultVectorOptions son los valores por defecto.
var DefaultVectorOptions = VectorOptions{Candidates: 200, ExactMax: 20000, Iterative: "relaxed_order"}

// PGVector consulta nexo_kb_section_vectors.
type PGVector struct {
	st   *store.Store
	opts VectorOptions
}

// vecHit es un resultado vectorial: sección y similitud de coseno.
type vecHit struct {
	id  string
	sim float64
}

// exprs son la expresión indexada, el operador de coseno y el vector de la
// consulta: vector(D) hasta 2000 dimensiones, halfvec(D) por encima (la
// misma expresión que el índice parcial de la migración).
func (p *PGVector) exprs(b VectorBackend) (expr, op, qv string) {
	s := quoteIdentifier(b.Support.Schema)
	d := strconv.Itoa(b.Index.Dims)
	typ := embed.StorageType(b.Index.Dims)
	return "v.embedding::" + s + "." + typ + "(" + d + ")", "OPERATOR(" + s + ".<=>)", "$1::" + s + "." + typ + "(" + d + ")"
}

const vectorCurrentWhere = `s.is_current AND s.valid_from <= now() AND (s.valid_until IS NULL OR s.valid_until > now())
	AND $6::timestamptz IS NOT NULL AND $7::timestamptz IS NOT NULL`

const vectorTemporalWhere = `s.recorded_from <= $7 AND (s.recorded_until IS NULL OR s.recorded_until > $7)
	AND s.valid_from <= $6 AND (s.valid_until IS NULL OR s.valid_until > $6)`

// search devuelve las secciones más parecidas que cumplen el filtro y si se
// usó el recorrido exacto.
func (p *PGVector) search(ctx context.Context, b VectorBackend, qvec []float32, q Query, f Filter, limit int) ([]vecHit, bool, error) {
	exact := !f.Current()
	if !exact && p.opts.ExactMax > 0 {
		var n int
		if err := p.st.DB.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT 1 FROM nexo_kb_sections WHERE space_id = ANY($1::uuid[]) AND is_current LIMIT $2) x`,
			f.SpaceIDs(), p.opts.ExactMax+1).Scan(&n); err != nil {
			return nil, false, err
		}
		exact = n <= p.opts.ExactMax
	}
	where := vectorCurrentWhere
	if !f.Current() {
		where = vectorTemporalWhere
	}
	expr, op, qv := p.exprs(b)
	args := []any{store.VectorLiteral(qvec), b.Index.Version, f.SpaceIDs(), q.Category, q.Tag, f.At(), f.KnownAt()}
	filter := `s.space_id = ANY($3::uuid[]) AND ` + where + ` AND ($4 = '' OR s.category = $4) AND ($5 = '' OR s.tags @> ARRAY[$5]::text[])`
	if exact {
		sqlText := `WITH f AS MATERIALIZED (SELECT s.id FROM nexo_kb_sections s WHERE ` + filter + `)
			SELECT v.section_id::text, 1 - (` + expr + ` ` + op + ` ` + qv + `) AS sim
			FROM f JOIN nexo_kb_section_vectors v ON v.index_version = $2 AND v.section_id = f.id
			ORDER BY 2 DESC, 1 LIMIT $8`
		hits, err := scanVecHits(p.st.DB.QueryContext(ctx, sqlText, append(args, limit)...))
		return hits, true, err
	}
	cand := max(p.opts.Candidates, limit)
	sqlText := fmt.Sprintf(`SELECT c.id::text, 1 - c.d FROM (
			SELECT v.section_id AS id, %[1]s %[2]s %[3]s AS d
			FROM nexo_kb_section_vectors v JOIN nexo_kb_sections s ON s.id = v.section_id
			WHERE v.index_version = $2 AND v.dims = %[4]d AND v.space_id = ANY($3::uuid[]) AND %[5]s
			ORDER BY %[1]s %[2]s %[3]s
			LIMIT $8
		) c ORDER BY c.d, c.id LIMIT $9`, expr, op, qv, b.Index.Dims, filter)
	tx, err := p.st.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	// SET LOCAL solo vale dentro de la transacción. Un parámetro que la
	// versión no conoce aborta la transacción: se reintenta sin él.
	if _, err := tx.ExecContext(ctx, `SET LOCAL hnsw.ef_search = `+strconv.Itoa(max(cand, 40))); err != nil {
		return p.searchPlain(ctx, sqlText, append(args, cand, limit))
	}
	if b.Support.Iterative && (p.opts.Iterative == "relaxed_order" || p.opts.Iterative == "strict_order") {
		if _, err := tx.ExecContext(ctx, `SET LOCAL hnsw.iterative_scan = `+p.opts.Iterative); err != nil {
			return p.searchPlain(ctx, sqlText, append(args, cand, limit))
		}
	}
	hits, err := scanVecHits(tx.QueryContext(ctx, sqlText, append(args, cand, limit)...))
	return hits, false, err
}

func (p *PGVector) searchPlain(ctx context.Context, sqlText string, args []any) ([]vecHit, bool, error) {
	hits, err := scanVecHits(p.st.DB.QueryContext(ctx, sqlText, args...))
	return hits, false, err
}

// similarities calcula la similitud de unas secciones con la pregunta (para
// los resultados que solo encontró el texto: el umbral necesita ambas
// señales).
func (p *PGVector) similarities(ctx context.Context, b VectorBackend, qvec []float32, ids []string) (map[string]float64, error) {
	out := map[string]float64{}
	if len(ids) == 0 {
		return out, nil
	}
	expr, op, qv := p.exprs(b)
	hits, err := scanVecHits(p.st.DB.QueryContext(ctx, `SELECT v.section_id::text, 1 - (`+expr+` `+op+` `+qv+`)
		FROM nexo_kb_section_vectors v WHERE v.index_version = $2 AND v.section_id = ANY($3::uuid[])`, store.VectorLiteral(qvec), b.Index.Version, ids))
	for _, h := range hits {
		out[h.id] = h.sim
	}
	return out, err
}

func scanVecHits(rows *sql.Rows, err error) ([]vecHit, error) {
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []vecHit
	for rows.Next() {
		var h vecHit
		if err := rows.Scan(&h.id, &h.sim); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func quoteIdentifier(s string) string {
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
