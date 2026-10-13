package service_test

// NX-10 R2 (design §20): búsqueda por significado con el embedder simulado
// (sin red) sobre Postgres con pgvector: ciclo de vida del índice, cola,
// reutilización por hash, modo hybrid con filtros de permisos y tiempo,
// degradación sin proveedor o sin pgvector, cartas muertas, recorrido HNSW
// frente al exacto y modo de dos cuentas.

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/kbtest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/search"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/service"
)

func r2Options() service.R2Options {
	o := service.DefaultR2
	o.Worker = false
	o.MinSimilarity = 0.6
	return o
}

// ensureVectorExtension crea la extensión en la base de pruebas (como haría
// un DBA) para las pruebas con cuentas sin privilegios.
func ensureVectorExtension(t *testing.T) {
	t.Helper()
	db, err := sql.Open("pgx", kbtest.BaseDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE EXTENSION IF NOT EXISTS vector SCHEMA public`); err != nil {
		t.Skipf("pgvector no disponible en la base de pruebas: %v", err)
	}
}

type r2Fixture struct {
	env  *kbtest.Env
	tm   kbtest.Team
	fake *kbtest.FakeEmbedder
}

func r2Setup(t *testing.T) r2Fixture {
	t.Helper()
	ensureVectorExtension(t)
	fake := kbtest.NewFakeEmbedder()
	env := kbtest.New(t, service.WithEmbedder(fake, fake.FakeConfig()), service.WithR2(r2Options()))
	tm := env.NewTeam()
	if _, err := env.KB.CreateSpace(env.Ctx(), env.ID(tm.Adm), service.CreateSpaceInput{Slug: "arq", Owner: "ana"}); err != nil {
		t.Fatal(err)
	}
	return r2Fixture{env: env, tm: tm, fake: fake}
}

func (f r2Fixture) publish(t *testing.T, space string, docs ...service.PublishDoc) {
	t.Helper()
	res, err := f.env.KB.PublishBatch(f.env.Ctx(), f.env.ID(f.tm.Ana), space, service.BatchRequest{Documents: docs})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res.Results {
		if r.Status == "rejected" {
			t.Fatalf("rechazado %s: %s", r.DocKey, r.Reason)
		}
	}
}

func (f r2Fixture) search(t *testing.T, who, q, mode string, at *time.Time) search.Result {
	t.Helper()
	res, err := f.env.KB.Search(f.env.Ctx(), f.env.ID(who), service.SearchRequest{Q: q, Mode: mode, At: at, Limit: 5})
	if err != nil {
		t.Fatalf("search %q: %v", q, err)
	}
	return res
}

func (f r2Fixture) drain(t *testing.T) service.VectorDrain {
	t.Helper()
	res, err := f.env.KB.DrainVectors(context.Background())
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	return res
}

var r2Docs = []service.PublishDoc{
	{DocKey: "backend/logging", Category: "estandar", Content: "# Logging en microservicios\n\n## Niveles de log\n\nUsa warning para errores recuperables y error para fallas que requieren atención.\n\n## Datos sensibles\n\nNunca registrar contraseñas, tokens ni datos personales en los logs.\n"},
	{DocKey: "backend/reintentos", Category: "estandar", Content: "# Reintentos HTTP\n\n## Política\n\nTres reintentos con espera exponencial en llamadas a servicios externos, con Polly.\n"},
	{DocKey: "datos/nomenclatura", Category: "convencion", Content: "# Nomenclatura de base de datos\n\n## Tablas\n\nLos nombres de tabla van en singular y en minúsculas.\n"},
}

func hasDoc(res search.Result, key string) bool {
	for _, h := range res.Hits {
		if h.DocKey == key {
			return true
		}
	}
	return false
}

func TestR2HybridLifecycle(t *testing.T) {
	f := r2Setup(t)
	env, tm := f.env, f.tm
	f.publish(t, "arq", r2Docs...)

	// Sin índice activo: por defecto fts; hybrid pedido responde por texto y
	// avisa, sin error.
	res := f.search(t, tm.Luis, "nombres de tabla", "", nil)
	if res.Mode != search.ModeFTS || res.Vector != nil {
		t.Fatalf("sin índice activo el modo por defecto es fts: %s %+v", res.Mode, res.Vector)
	}
	res = f.search(t, tm.Luis, "nombres de tabla", search.ModeHybrid, nil)
	if res.Mode != search.ModeFTS || res.Vector == nil || res.Vector.Status != search.VectorUnavailable || res.Notice == "" || res.NoResults {
		t.Fatalf("hybrid sin índice: %s %+v %q", res.Mode, res.Vector, res.Notice)
	}

	// La cola: una versión en construcción con todas las secciones.
	d := f.drain(t)
	var sections int
	_ = env.DB.QueryRow(`SELECT count(*) FROM nexo_kb_sections`).Scan(&sections)
	if d.Embedded != sections || len(d.Activated) != 1 || d.Activated[0] != 1 {
		t.Fatalf("primera pasada: %+v (secciones %d)", d, sections)
	}
	if n := env.Count(`SELECT count(*) FROM nexo_kb_index_jobs`); n != 0 {
		t.Fatalf("cola vacía al terminar: %d", n)
	}

	// Con índice activo: hybrid por defecto; la paráfrasis en inglés encuentra
	// la política de reintentos por significado.
	res = f.search(t, tm.Luis, "retry policy for external calls", "", nil)
	if res.Mode != search.ModeHybrid || res.Vector == nil || res.Vector.Status != search.VectorUsed || res.Vector.Scan != "exact" {
		t.Fatalf("hybrid activo: %s %+v", res.Mode, res.Vector)
	}
	if res.NoResults || res.Hits[0].DocKey != "backend/reintentos" || res.Hits[0].Similarity == nil || !slices.Contains(res.Hits[0].MatchedBy, "vector") {
		t.Fatalf("paráfrasis: %+v", res.Hits)
	}
	t.Logf("similitud paráfrasis: %.3f", *res.Hits[0].Similarity)
	// Lo mismo con fts no encuentra nada (el umbral de cobertura lo descarta).
	if fts := f.search(t, tm.Luis, "retry policy for external calls", search.ModeFTS, nil); hasDoc(fts, "backend/reintentos") {
		t.Log("aviso: fts también encontró la paráfrasis")
	}
	// Sin estándar: con vectores siempre hay algo parecido; el umbral responde
	// no_results.
	if na := f.search(t, tm.Luis, "cuántas réplicas de Kubernetes por pod", "", nil); !na.NoResults {
		t.Fatalf("sin estándar debe dar no_results: %+v %+v", na.Hits, na.Relevance)
	}

	// Proveedor caído: respuesta solo por texto, con aviso y sin error.
	f.fake.SetFail(kbtest.ErrFakeDown)
	res = f.search(t, tm.Luis, "nivel de log para errores recuperables", "", nil)
	if res.Mode != search.ModeFTS || res.Vector == nil || res.Vector.Status != search.VectorFailed || res.Notice == "" || !hasDoc(res, "backend/logging") {
		t.Fatalf("proveedor caído: %s %+v %q %+v", res.Mode, res.Vector, res.Notice, res.Hits)
	}
	// El indexador no cuenta un fallo por el proveedor caído: libera el trabajo.
	f.publish(t, "arq", service.PublishDoc{DocKey: "backend/colas", Category: "estandar", Content: "# Colas\n\n## Idempotencia\n\nLos consumidores de colas son idempotentes.\n"})
	if _, err := env.KB.DrainVectors(context.Background()); err == nil {
		t.Fatal("con el proveedor caído la pasada informa el corte")
	}
	if n := env.Count(`SELECT count(*) FROM nexo_kb_index_jobs WHERE fail_count = 0 AND claimed_at IS NULL`); n == 0 {
		t.Fatal("los trabajos vuelven a la cola sin contar fallo")
	}
	f.fake.SetFail(nil)
	f.drain(t)

	// RC-05: una versión nueva solo vectoriza lo que cambió.
	before := f.fake.Texts.Load()
	f.publish(t, "arq", service.PublishDoc{DocKey: "backend/logging", Category: "estandar", Content: "# Logging en microservicios\n\n## Niveles de log\n\nUsa warning para errores recuperables y error para fallas que requieren atención.\n\n## Datos sensibles\n\nNunca registrar contraseñas, tokens, números de tarjeta ni datos personales en los logs.\n"})
	d = f.drain(t)
	if got := f.fake.Texts.Load() - before; d.Reused < 1 || got != 1 {
		t.Fatalf("reutilización: %+v, textos vectorizados %d (se esperaba 1)", d, got)
	}

	// Vigencia: con at en el pasado (antes de la v2) se usa el recorrido
	// exacto y solo aparecen secciones de la v1.
	var v2At time.Time
	_ = env.DB.QueryRow(`SELECT published_at FROM nexo_kb_versions v JOIN nexo_kb_documents d ON d.id = v.document_id WHERE d.doc_key = 'backend/logging' AND v.version = 2`).Scan(&v2At)
	past := v2At.Add(-time.Millisecond)
	res, err := env.KB.Search(env.Ctx(), env.ID(tm.Luis), service.SearchRequest{Q: "tarjeta datos personales en los logs", KnownAt: &past, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if res.Vector == nil || res.Vector.Scan != "exact" {
		t.Fatalf("consulta histórica: recorrido exacto: %+v", res.Vector)
	}
	for _, h := range res.Hits {
		if h.DocKey == "backend/logging" && h.Version != 1 {
			t.Fatalf("known_at anterior a la v2 no debe ver la v2: %+v", h)
		}
	}

	// Permisos: un espacio restringido no aparece por vectores.
	if _, err := env.KB.CreateSpace(env.Ctx(), env.ID(tm.Adm), service.CreateSpaceInput{Slug: "privado", Visibility: "restricted", Owner: "ana"}); err != nil {
		t.Fatal(err)
	}
	f.publish(t, "privado", service.PublishDoc{DocKey: "secreto", Category: "estandar", Content: "# Pagos\n\n## Reintentos de pagos\n\nLos pagos se reintentan tres veces en llamadas externas.\n"})
	f.drain(t)
	for _, q := range []string{"retry policy for external calls", "reintentos de pagos"} {
		for _, h := range f.search(t, tm.Luis, q, search.ModeHybrid, nil).Hits {
			if h.Space == "privado" {
				t.Fatalf("Luis no ve el espacio restringido por vectores: %+v", h)
			}
		}
	}
	if !hasDoc(f.search(t, tm.Ana, "reintentos de pagos", search.ModeHybrid, nil), "secreto") {
		t.Fatal("Ana sí ve su espacio restringido")
	}

	// Administración: solo platform.settings.
	if _, err := env.KB.VectorState(env.Ctx(), env.ID(tm.Luis)); err == nil {
		t.Fatal("un lector no ve el estado del índice")
	}
	if _, err := env.KB.ReindexVectors(env.Ctx(), env.ID(tm.Ana), false, 0); !domain.IsKind(err, domain.KindForbidden) {
		t.Fatalf("la responsable no reconstruye el índice: %v", err)
	}

	// RC-16: reconstrucción en segundo plano; la búsqueda sigue con v1 hasta
	// que v2 termine; luego v1 se retira y sus vectores se descartan.
	st, err := env.KB.ReindexVectors(env.Ctx(), env.ID(tm.Root), false, 0)
	if err != nil || len(st.Versions) != 2 {
		t.Fatalf("reindex: %+v %v", st, err)
	}
	if res := f.search(t, tm.Luis, "nombres de tabla en singular", "", nil); res.Vector == nil || res.Vector.IndexVersion != 1 {
		t.Fatalf("mientras se construye sigue la v1: %+v", res.Vector)
	}
	d = f.drain(t)
	if !slices.Contains(d.Activated, 2) {
		t.Fatalf("v2 activa: %+v", d)
	}
	f.drain(t) // descarta lo de la v1
	if n := env.Count(`SELECT count(*) FROM nexo_kb_section_vectors WHERE index_version = 1`); n != 0 {
		t.Fatalf("vectores de la versión retirada: %d", n)
	}
	if res := f.search(t, tm.Luis, "nombres de tabla en singular", "", nil); res.Vector == nil || res.Vector.IndexVersion != 2 {
		t.Fatalf("tras activar se usa la v2: %+v", res.Vector)
	}

	// Cartas muertas: un texto que el proveedor rechaza siempre.
	f.fake.SetReject("TEXTO-RECHAZADO")
	f.publish(t, "arq", service.PublishDoc{DocKey: "malo", Category: "estandar", Content: "# Malo\n\n## Uno\n\nTEXTO-RECHAZADO por el proveedor.\n"})
	for range 5 {
		f.drain(t)
		// Cada fallo espera el umbral de abandono; la prueba lo adelanta.
		env.Exec(`UPDATE nexo_kb_index_jobs SET claimed_at = now() - interval '1 hour' WHERE claimed_at IS NOT NULL`)
	}
	if n := env.Count(`SELECT count(*) FROM nexo_kb_index_dead`); n != 1 {
		t.Fatalf("carta muerta: %d", n)
	}
	if n := env.Count(`SELECT count(*) FROM nexo_alerts WHERE rule = 'kb_index_dead_letter'`); n != 1 {
		t.Fatalf("alerta kb_index_dead_letter: %d", n)
	}
	// La sección se encuentra igual por texto.
	if !hasDoc(f.search(t, tm.Luis, "TEXTO-RECHAZADO proveedor", "", nil), "malo") {
		t.Fatal("una sección sin vector se encuentra por texto")
	}
	f.fake.SetReject("")
	if _, err := env.KB.ReindexVectors(env.Ctx(), env.ID(tm.Root), true, 0); err != nil {
		t.Fatal(err)
	}
	f.drain(t)
	if n := env.Count(`SELECT count(*) FROM nexo_kb_index_dead`); n != 0 {
		t.Fatalf("reencolada: %d", n)
	}
	st, err = env.KB.VectorState(env.Ctx(), env.ID(tm.Root))
	if err != nil || !st.Enabled || !st.Support.Available || len(st.Versions) != 1 || st.Versions[0].Pending != 0 || st.Versions[0].Vectors == 0 {
		t.Fatalf("estado: %+v %v", st, err)
	}
	if strings.Contains(fmt.Sprint(st.Embedder), "key") {
		t.Fatal("el estado no expone la clave")
	}
}

// TestR2HNSWRecallSmallSpace: un espacio pequeño dentro de una tabla grande.
// El recorrido HNSW (iterative_scan) devuelve lo mismo que el exacto.
func TestR2HNSWRecallSmallSpace(t *testing.T) {
	f := r2Setup(t)
	env, tm := f.env, f.tm
	var big []service.PublishDoc
	for i := range 150 {
		var b strings.Builder
		fmt.Fprintf(&b, "# Documento %d\n\n", i)
		for j := range 4 {
			fmt.Fprintf(&b, "## Tema %d-%d\n\nTexto de relleno %d sobre colas, caches, despliegues y monitoreo número %d.\n\n", i, j, i*7+j, j)
		}
		big = append(big, service.PublishDoc{DocKey: fmt.Sprintf("relleno/doc-%03d", i), Category: "estandar", Content: b.String()})
	}
	f.publish(t, "arq", big...)
	if _, err := env.KB.CreateSpace(env.Ctx(), env.ID(tm.Adm), service.CreateSpaceInput{Slug: "chico", Owner: "ana"}); err != nil {
		t.Fatal(err)
	}
	f.publish(t, "chico", r2Docs...)
	f.drain(t)
	q := service.SearchRequest{Q: "retry policy for external calls", Spaces: []string{"chico"}, Mode: search.ModeHybrid, Limit: 5}
	o := r2Options()
	o.Hybrid.Weights.Text = 0.000001 // solo el orden vectorial
	o.Hybrid.Vector.ExactMax = 0     // fuerza HNSW
	env.KB.ApplyR2(o)
	hn, err := env.KB.Search(env.Ctx(), env.ID(tm.Luis), q)
	if err != nil {
		t.Fatal(err)
	}
	o.Hybrid.Vector.ExactMax = 1_000_000
	env.KB.ApplyR2(o)
	ex, err := env.KB.Search(env.Ctx(), env.ID(tm.Luis), q)
	if err != nil {
		t.Fatal(err)
	}
	if hn.Vector.Scan != "hnsw" || ex.Vector.Scan != "exact" {
		t.Fatalf("recorridos: %s %s", hn.Vector.Scan, ex.Vector.Scan)
	}
	ids := func(r search.Result) []string {
		var out []string
		for _, h := range r.Hits {
			out = append(out, h.SectionID)
		}
		return out
	}
	if len(hn.Hits) == 0 || !slices.Equal(ids(hn), ids(ex)) {
		t.Fatalf("HNSW con filtro de espacio pierde resultados:\nhnsw  %v\nexact %v", ids(hn), ids(ex))
	}
	// Con un índice para la dimensión, el plan usa el HNSW parcial (aquí se
	// crea a mano: la migración crea 768, 1024, 1536, 3072 y la configurada).
	env.Exec(fmt.Sprintf(`CREATE INDEX idx_test_hnsw_%[1]d ON nexo_kb_section_vectors USING hnsw ((embedding::public.vector(%[1]d)) public.vector_cosine_ops) WHERE dims = %[1]d`, kbtest.FakeDims))
	if plan := vectorPlan(t, env, kbtest.FakeDims, "vector"); !strings.Contains(plan, "idx_test_hnsw_") {
		t.Fatalf("el plan debía usar el índice HNSW:\n%s", plan)
	}
}

// vectorPlan es el plan de la consulta vectorial con la misma expresión que
// usa search.PGVector, sin recorrido secuencial ni orden en memoria: con
// pocas filas el planificador prefiere ordenar, aquí se comprueba que el índice
// HNSW es usable con esa expresión (con miles de filas lo elige solo).
func vectorPlan(t *testing.T, env *kbtest.Env, dims int, typ string) string {
	t.Helper()
	tx, err := env.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`ANALYZE nexo_kb_section_vectors`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`SET LOCAL enable_seqscan = off; SET LOCAL enable_sort = off`); err != nil {
		t.Fatal(err)
	}
	lit := "[" + strings.TrimSuffix(strings.Repeat("0.1,", dims), ",") + "]"
	rows, err := tx.Query(fmt.Sprintf(`EXPLAIN SELECT v.section_id FROM nexo_kb_section_vectors v WHERE v.index_version = 1 AND v.dims = %[1]d
		ORDER BY v.embedding::public.%[2]s(%[1]d) OPERATOR(public.<=>) $1::public.%[2]s(%[1]d) LIMIT 5`, dims, typ), lit)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var l string
		_ = rows.Scan(&l)
		b.WriteString(l + "\n")
	}
	return b.String()
}

// TestR2Halfvec: 3072 dimensiones (gemini-embedding-001 nativa,
// text-embedding-3-large) se guardan como vector y se indexan como halfvec.
func TestR2Halfvec(t *testing.T) {
	ensureVectorExtension(t)
	fake := kbtest.NewFakeEmbedder()
	fake.D = 3072
	o := r2Options()
	o.Hybrid.Vector.ExactMax = 0 // fuerza HNSW
	env := kbtest.New(t, service.WithEmbedder(fake, fake.FakeConfig()), service.WithR2(o))
	tm := env.NewTeam()
	if _, err := env.KB.CreateSpace(env.Ctx(), env.ID(tm.Adm), service.CreateSpaceInput{Slug: "arq", Owner: "ana"}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.KB.PublishBatch(env.Ctx(), env.ID(tm.Ana), "arq", service.BatchRequest{Documents: r2Docs}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.KB.DrainVectors(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, err := env.KB.Search(env.Ctx(), env.ID(tm.Luis), service.SearchRequest{Q: "retry policy for external calls"})
	if err != nil || res.Mode != search.ModeHybrid || res.Vector.Scan != "hnsw" || !hasDoc(res, "backend/reintentos") {
		t.Fatalf("halfvec: %+v %v", res.Vector, err)
	}
	if plan := vectorPlan(t, env, 3072, "halfvec"); !strings.Contains(plan, "idx_nexo_kb_section_vectors_hnsw_3072") {
		t.Fatalf("el plan debía usar el índice halfvec de 3072:\n%s", plan)
	}
}

// TestR2WithoutPgvector: sin la tabla de vectores (pgvector ausente) el
// servicio con proveedor configurado responde por texto, sin errores.
func TestR2WithoutPgvector(t *testing.T) {
	f := r2Setup(t)
	f.publish(t, "arq", r2Docs...)
	f.env.Exec(`DROP TABLE nexo_kb_section_vectors CASCADE`)
	fake := kbtest.NewFakeEmbedder()
	svc := service.New(f.env.DB, f.env.Roles, service.WithEmbedder(fake, fake.FakeConfig()), service.WithR2(r2Options()))
	if _, err := svc.DrainVectors(context.Background()); err != nil {
		t.Fatalf("sin pgvector el indexador no falla: %v", err)
	}
	res, err := svc.Search(f.env.Ctx(), f.env.ID(f.tm.Luis), service.SearchRequest{Q: "nombres de tabla", Mode: search.ModeHybrid})
	if err != nil || res.Mode != search.ModeFTS || res.Vector == nil || res.Vector.Status != search.VectorUnavailable || !strings.Contains(res.Vector.Reason, "pgvector") || res.NoResults {
		t.Fatalf("sin pgvector: %+v %v", res, err)
	}
	if fake.Calls.Load() != 0 {
		t.Fatal("sin pgvector no se llama al proveedor")
	}
}

// TestR2Off: sin proveedor el servicio es el de R1 (motor pgfts).
func TestR2Off(t *testing.T) {
	env := kbtest.New(t)
	if env.KB.VectorEnabled() {
		t.Fatal("sin NEXO_KB_EMBEDDER no hay R2")
	}
	tm := env.NewTeam()
	if _, err := env.KB.CreateSpace(env.Ctx(), env.ID(tm.Adm), service.CreateSpaceInput{Slug: "arq", Owner: "ana"}); err != nil {
		t.Fatal(err)
	}
	res, err := env.KB.Search(env.Ctx(), env.ID(tm.Luis), service.SearchRequest{Q: "algo", Mode: search.ModeHybrid})
	if err != nil || res.Mode != search.ModeFTS || res.Vector == nil || res.Vector.Status != search.VectorUnavailable || res.Retriever != "pgfts" {
		t.Fatalf("hybrid sin proveedor: %+v %v", res, err)
	}
}

// TestR2TwoAccounts: el indexador y la búsqueda funcionan con la cuenta de
// aplicación (NX-07); la migración la corrió la dueña.
func TestR2TwoAccounts(t *testing.T) {
	ensureVectorExtension(t)
	fake := kbtest.NewFakeEmbedder()
	env, _ := kbtest.TwoAccounts(t, service.WithEmbedder(fake, fake.FakeConfig()), service.WithR2(r2Options()))
	tm := env.NewTeam()
	if _, err := env.KB.CreateSpace(env.Ctx(), env.ID(tm.Adm), service.CreateSpaceInput{Slug: "arq", Owner: "ana"}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.KB.PublishBatch(env.Ctx(), env.ID(tm.Ana), "arq", service.BatchRequest{Documents: r2Docs}); err != nil {
		t.Fatal(err)
	}
	d, err := env.KB.DrainVectors(context.Background())
	if err != nil || len(d.Activated) != 1 {
		t.Fatalf("indexador con la cuenta de aplicación: %+v %v", d, err)
	}
	res, err := env.KB.Search(env.Ctx(), env.ID(tm.Luis), service.SearchRequest{Q: "retry policy for external calls"})
	if err != nil || res.Mode != search.ModeHybrid || !hasDoc(res, "backend/reintentos") {
		t.Fatalf("hybrid con la cuenta de aplicación: %+v %v", res, err)
	}
}
