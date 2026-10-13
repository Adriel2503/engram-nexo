package service_test

// RNF-K1: búsqueda en el servidor con datos sintéticos. Se activa con
// NEXO_KB_PERF_SECTIONS=<n> (por ejemplo 200000 o 1000000); sin la variable,
// se salta. Carga 500 espacios abiertos, n/20 documentos y n secciones
// vigentes (más un 10 % de secciones históricas), con vocabulario de
// distribución sesgada (pocas palabras muy frecuentes, muchas raras), y mide
// p50/p95/p99 de la búsqueda completa del servicio (autorización con
// VisibleScopes + filtros comunes + pgfts + ts_headline).

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/kbtest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/service"
)

// RNF-K3: un lote de 200 documentos Markdown se publica en menos de 60 s.
func TestPublishBatchOf200Documents(t *testing.T) {
	f := setup(t)
	base := readDoc(t, "frontend/formularios.md")
	docs := make([]service.PublishDoc, 200)
	for i := range docs {
		docs[i] = service.PublishDoc{Path: fmt.Sprintf("lote/doc-%03d.md", i),
			Content: fmt.Sprintf("---\nid: lote/doc-%03d\ncategory: lineamiento\n---\n", i) + base[strings.Index(base, "# Formularios"):] + fmt.Sprintf("\n## Anexo %d\n\nTexto propio del documento %d.\n", i, i),
			Origin:  gitOrigin(fmt.Sprintf("lote/doc-%03d.md", i), "c1")}
	}
	t0 := time.Now()
	res := f.publish(t, f.tm.CI, docs)
	took := time.Since(t0)
	if res.Summary["created"] != 200 {
		t.Fatalf("lote de 200: %v (%s)", res.Summary, statuses(res)[:200])
	}
	t.Logf("RNF-K3: 200 documentos (%d secciones) publicados en %s", f.env.Count(`SELECT COUNT(*) FROM nexo_kb_sections`), took.Round(time.Millisecond))
	if took > 60*time.Second {
		t.Fatalf("RNF-K3: %s > 60 s", took)
	}
	t0 = time.Now()
	res = f.publish(t, f.tm.CI, docs)
	t.Logf("RNF-K3: reintento idempotente del mismo lote en %s", time.Since(t0).Round(time.Millisecond))
	if res.Summary["unchanged"] != 200 {
		t.Fatalf("reintento: %v", res.Summary)
	}
}

func TestPerfSearchSyntheticSections(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("NEXO_KB_PERF_SECTIONS"))
	if n <= 0 {
		t.Skip("NEXO_KB_PERF_SECTIONS no definido (prueba de rendimiento RNF-K1)")
	}
	env := kbtest.New(t)
	tm := env.NewTeam()
	ctx := context.Background()
	const spaces = 500
	docs := n / 20
	start := time.Now()
	load := []string{
		`DROP INDEX IF EXISTS idx_nexo_kb_sections_tsv_current`,
		`DROP INDEX IF EXISTS idx_nexo_kb_sections_tsv_all`,
		`CREATE TEMP TABLE vocab AS SELECT i, (ARRAY['validacion','formulario','componente','boton','tabla','accesibilidad','rendimiento','seguridad','pago','cliente','tienda','inventario','precio','descuento','reclamo','pedido','envio','factura','usuario','sesion'])[1 + i % 20] || CASE WHEN i < 20 THEN '' ELSE i::text END AS w FROM generate_series(0, 19999) i`,
		`INSERT INTO nexo_scopes (scope_type, scope_key, visibility, status) SELECT 'space', 'perf-' || i, 'open', 'active' FROM generate_series(1, ` + strconv.Itoa(spaces) + `) i`,
		`INSERT INTO nexo_kb_spaces (id, slug, name) SELECT md5('s' || i)::uuid, 'perf-' || i, 'Perf ' || i FROM generate_series(1, ` + strconv.Itoa(spaces) + `) i`,
		`INSERT INTO nexo_kb_documents (id, space_id, doc_key, title, category, status, valid_from, current_version, origin_kind, origin_ref)
		 SELECT md5('d' || i)::uuid, md5('s' || (1 + i % ` + strconv.Itoa(spaces) + `))::uuid, 'doc/' || i, 'Documento ' || i, 'lineamiento', 'active', now() - interval '30 days', 2, 'git', 'perf/repo'
		 FROM generate_series(1, ` + strconv.Itoa(docs) + `) i`,
		`INSERT INTO nexo_kb_versions (id, document_id, space_id, version, title, category, content_md, content_sha256, meta_sha256, source_format, valid_from, published_by_label, published_at)
		 SELECT md5('v' || i || '-' || v)::uuid, md5('d' || i)::uuid, md5('s' || (1 + i % ` + strconv.Itoa(spaces) + `))::uuid, v, 'Documento ' || i, 'lineamiento', 'x', 'h', 'm', 'md', now() - interval '30 days', 'perf', now() - interval '30 days'
		 FROM generate_series(1, ` + strconv.Itoa(docs) + `) i, generate_series(1, 2) v`,
		// Secciones: 20 vigentes por documento (versión 2) y 2 históricas (versión 1).
		`INSERT INTO nexo_kb_sections (id, space_id, document_id, version_id, ordinal, anchor, heading_path, body, token_count, category, valid_from, recorded_from, recorded_until, is_current, tsv)
		 SELECT md5('x' || d || '-' || o || '-' || v)::uuid, md5('s' || (1 + d % ` + strconv.Itoa(spaces) + `))::uuid, md5('d' || d)::uuid, md5('v' || d || '-' || v)::uuid, o, 'sec-' || o,
		        ARRAY['Documento ' || d, t.title], t.body, 60, 'lineamiento', now() - interval '30 days', now() - interval '30 days',
		        CASE WHEN v = 1 THEN now() - interval '10 days' END, v = 2,
		        setweight(to_tsvector('nexo_es', t.title), 'A') || setweight(to_tsvector('nexo_es', t.body), 'B')
		 FROM generate_series(1, ` + strconv.Itoa(docs) + `) d
		 CROSS JOIN LATERAL (SELECT 2 AS v, o FROM generate_series(1, 20) o UNION ALL SELECT 1, o FROM generate_series(1, 2) o) vo(v, o)
		 CROSS JOIN (SELECT array_agg(w ORDER BY i) AS a FROM vocab) va
		 CROSS JOIN LATERAL (
		   SELECT (SELECT string_agg(va.a[1 + floor(20000 * random() ^ 3)::int + 0 * g], ' ') FROM generate_series(1, 3) g WHERE d > 0 AND vo.o > 0) AS title,
		          (SELECT string_agg(va.a[1 + floor(20000 * random() ^ 3)::int + 0 * g], ' ') FROM generate_series(1, 60) g WHERE d > 0 AND vo.o > 0) AS body
		 ) t`,
		`CREATE INDEX idx_nexo_kb_sections_tsv_current ON nexo_kb_sections USING GIN (tsv) WHERE is_current`,
		`CREATE INDEX idx_nexo_kb_sections_tsv_all ON nexo_kb_sections USING GIN (tsv)`,
		`ANALYZE nexo_kb_sections`, `ANALYZE nexo_kb_documents`, `ANALYZE nexo_kb_versions`,
	}
	for _, q := range load {
		if _, err := env.DB.ExecContext(ctx, q); err != nil {
			t.Fatalf("carga: %v\n%s", err, q)
		}
	}
	current := env.Count(`SELECT COUNT(*) FROM nexo_kb_sections WHERE is_current`)
	total := env.Count(`SELECT COUNT(*) FROM nexo_kb_sections`)
	t.Logf("carga: %d secciones vigentes (%d en total), %d documentos, %d espacios en %s", current, total, docs, spaces, time.Since(start).Round(time.Second))

	queries := []string{
		"validacion", "formulario", "validacion formulario", "boton accesibilidad", "rendimiento seguridad pago",
		"cliente tienda", "inventario precio descuento", "reclamo pedido", "envio factura", "usuario sesion",
		"validacion123", "formulario 541", "precio4402", "\"validacion formulario\"", "pago -descuento",
		"tabla componente boton", "accesibilidad123 rendimiento", "factura usuario sesion tienda", "descuento", "componente",
	}
	measure := func(label string, mod func(*service.SearchRequest)) {
		// Calentar.
		for _, q := range queries[:5] {
			_, _ = env.KB.Search(env.Ctx(), env.ID(tm.Luis), service.SearchRequest{Q: q, Limit: 10})
		}
		var lat []time.Duration
		for rep := 0; rep < 5; rep++ {
			for _, q := range queries {
				req := service.SearchRequest{Q: q, Limit: 10}
				if mod != nil {
					mod(&req)
				}
				t0 := time.Now()
				res, err := env.KB.Search(env.Ctx(), env.ID(tm.Luis), req)
				if err != nil {
					t.Fatalf("%s %q: %v", label, q, err)
				}
				lat = append(lat, time.Since(t0))
				_ = res
			}
		}
		sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
		p := func(x float64) time.Duration { return lat[int(x*float64(len(lat)-1))] }
		t.Logf("%-38s n=%d p50=%s p95=%s p99=%s max=%s", label, len(lat), p(0.50).Round(time.Millisecond), p(0.95).Round(time.Millisecond), p(0.99).Round(time.Millisecond), lat[len(lat)-1].Round(time.Millisecond))
		if p(0.95) > 3*time.Second {
			t.Errorf("%s: p95 %s supera el máximo aceptable de 3 s (RNF-02 del MVP)", label, p(0.95))
		}
	}
	if os.Getenv("NEXO_KB_PERF_EXPLAIN") == "1" {
		for _, q := range []string{"validacion", "precio4402", "validacion formulario"} {
			rows, err := env.DB.QueryContext(ctx, `EXPLAIN (ANALYZE, BUFFERS)
				SELECT s.id, ts_rank_cd(s.tsv, query, 1) AS rank FROM nexo_kb_sections s, websearch_to_tsquery('nexo_es', $1) query
				WHERE s.tsv @@ query AND s.space_id = ANY(ARRAY(SELECT id FROM nexo_kb_spaces)) AND s.is_current
				  AND s.valid_from <= now() AND (s.valid_until IS NULL OR s.valid_until > now())
				ORDER BY rank DESC, s.id LIMIT 10`, q)
			if err != nil {
				t.Fatal(err)
			}
			var plan string
			for rows.Next() {
				var line string
				_ = rows.Scan(&line)
				plan += line + "\n"
			}
			rows.Close()
			t.Logf("EXPLAIN %q:\n%s", q, plan)
		}
	}
	measure("vigente hoy, 500 espacios visibles", nil)
	measure("vigente hoy, 5 espacios pedidos", func(r *service.SearchRequest) {
		r.Spaces = []string{"perf-1", "perf-2", "perf-3", "perf-4", "perf-5"}
	})
	past := time.Now().Add(-15 * 24 * time.Hour)
	measure("known_at hace 15 días (histórico)", func(r *service.SearchRequest) { r.KnownAt = &past })
	fmt.Fprintln(os.Stderr)
}
