package service_test

// NX-10 R3 (design §21): reordenamiento con el reordenador simulado (sin red)
// sobre Postgres con pgvector: orden, informe, caída segura, umbral que el
// reordenador no puede bajar, permisos (lo que ve el proveedor), parámetros
// de la consulta, estado administrativo y rastro en las corridas de
// evaluación.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/eval"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/kbtest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/rerank"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/search"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/service"
)

// r3Setup arma un entorno con embedder y reordenador simulados. El umbral de
// similitud es mínimo para que la búsqueda devuelva varios candidatos.
func r3Setup(t *testing.T, ro search.RerankOptions) (r2Fixture, *kbtest.FakeReranker) {
	t.Helper()
	ensureVectorExtension(t)
	fake := kbtest.NewFakeEmbedder()
	fr := kbtest.NewFakeReranker()
	r2 := r2Options()
	r2.MinSimilarity = 0.01
	env := kbtest.New(t, service.WithEmbedder(fake, fake.FakeConfig()), service.WithR2(r2), service.WithReranker(fr, fr.Config()), service.WithR3(ro))
	tm := env.NewTeam()
	if _, err := env.KB.CreateSpace(env.Ctx(), env.ID(tm.Adm), service.CreateSpaceInput{Slug: "arq", Owner: "ana"}); err != nil {
		t.Fatal(err)
	}
	f := r2Fixture{env: env, tm: tm, fake: fake}
	f.publish(t, "arq", r2Docs...)
	if d := f.drain(t); len(d.Activated) != 1 {
		t.Skipf("pgvector no disponible en la base de pruebas: %+v", d)
	}
	return f, fr
}

func (f r2Fixture) searchRerank(t *testing.T, who, q, mode, rerankParam string, limit int) search.Result {
	t.Helper()
	res, err := f.env.KB.Search(f.env.Ctx(), f.env.ID(who), service.SearchRequest{Q: q, Mode: mode, Rerank: rerankParam, Limit: limit})
	if err != nil {
		t.Fatalf("search %q: %v", q, err)
	}
	return res
}

func docKeys(res search.Result) string {
	var out []string
	for _, h := range res.Hits {
		out = append(out, h.DocKey+"#"+h.Anchor)
	}
	return strings.Join(out, " | ")
}

func r3Options() search.RerankOptions {
	o := search.DefaultRerankOptions
	o.Blend = 1 // orden puro: las pruebas fuerzan el primer resultado con el reordenador simulado
	o.Budget = 3 * time.Second
	return o
}

const r3Query = "politica de reintentos y niveles de log"

func TestR3RerankReordersAndReports(t *testing.T) {
	f, fr := r3Setup(t, r3Options())
	tm := f.tm
	base := f.searchRerank(t, tm.Luis, r3Query, search.ModeHybrid, "off", 5)
	if base.Rerank != nil || len(base.Hits) < 3 || fr.Calls.Load() != 0 {
		t.Fatalf("rerank=off: sin informe ni llamadas: %+v (%d resultados, %d llamadas)", base.Rerank, len(base.Hits), fr.Calls.Load())
	}
	// Sin Default y sin pedirlo: la respuesta de siempre.
	if res := f.searchRerank(t, tm.Luis, r3Query, search.ModeHybrid, "", 5); res.Rerank != nil || fr.Calls.Load() != 0 {
		t.Fatalf("Default apagado: %+v", res.Rerank)
	}
	// Se fuerza un orden distinto: la nomenclatura de tablas queda primera.
	fr.SetBoost(map[string]float64{"Nomenclatura de base de datos": 1})
	res := f.searchRerank(t, tm.Luis, r3Query, search.ModeHybrid, "on", 5)
	if res.Rerank == nil || res.Rerank.Status != search.RerankUsed || res.Rerank.Provider != "fake" || res.Rerank.Model != fr.ModelName {
		t.Fatalf("informe: %+v", res.Rerank)
	}
	if res.Hits[0].DocKey != "datos/nomenclatura" || base.Hits[0].DocKey == "datos/nomenclatura" || !res.Rerank.TopChanged || res.Rerank.Reordered == 0 {
		t.Fatalf("el reordenador pone primera la nomenclatura:\n antes: %s\n ahora: %s\n%+v", docKeys(base), docKeys(res), res.Rerank)
	}
	if res.Rerank.Candidates != res.Rerank.Scored || res.Rerank.Candidates < 3 || res.Rerank.LatencyMS < 0 {
		t.Fatalf("candidatos y puntajes: %+v", res.Rerank)
	}
	for _, h := range res.Hits {
		if h.RerankScore == nil || h.PreRerankPosition == 0 || h.Citation == "" || h.Link == "" {
			t.Fatalf("cada resultado trae puntaje, posición previa, cita y enlace: %+v", h)
		}
	}
	// El proveedor vio el pasaje completo (ruta, título y cuerpo), no el extracto.
	var sawFull bool
	for _, p := range fr.Seen() {
		if strings.Contains(p, "Nomenclatura de base de datos") && strings.Contains(p, "Los nombres de tabla van en singular") {
			sawFull = true
		}
	}
	if !sawFull {
		t.Fatalf("pasaje con título y cuerpo: %q", fr.Seen())
	}
	if q := fr.Queries(); q[len(q)-1] != r3Query {
		t.Fatalf("pregunta enviada: %q", q)
	}
	// Mismo conjunto de resultados con y sin reordenar (solo cambia el orden).
	if len(res.Hits) != len(base.Hits) {
		t.Fatalf("el reordenador no agrega ni quita resultados: %d frente a %d", len(res.Hits), len(base.Hits))
	}
	// También sobre fts.
	fts := f.searchRerank(t, tm.Luis, "nombres de tabla", search.ModeFTS, "on", 5)
	if fts.Rerank == nil || (fts.Rerank.Status != search.RerankUsed && fts.Rerank.Status != search.RerankSkipped) {
		t.Fatalf("fts con reordenamiento: %+v", fts.Rerank)
	}
	// El modo exact no se reordena.
	ex := f.searchRerank(t, tm.Luis, "/Polly.*reintentos/", search.ModeExact, "on", 5)
	if ex.Rerank == nil || ex.Rerank.Status != search.RerankSkipped {
		t.Fatalf("exact: %+v", ex.Rerank)
	}
}

func TestR3FallsBackSafely(t *testing.T) {
	f, fr := r3Setup(t, r3Options())
	tm := f.tm
	base := f.searchRerank(t, tm.Luis, r3Query, search.ModeHybrid, "off", 5)
	cases := map[string]func(){
		"error del proveedor": func() { fr.SetFail(errors.Join(rerank.ErrUnavailable, errors.New("503 con la clave sk-secreta"))) },
		"respuesta no válida": func() { fr.SetFail(rerank.ErrMalformed) },
		"puntajes parciales":  func() { fr.SetPartial(true) },
		"tiempo agotado": func() {
			o := r3Options()
			o.Budget = 150 * time.Millisecond
			f.env.KB.ApplyR3(o)
			fr.SetDelay(5 * time.Second)
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			fr.SetFail(nil)
			fr.SetPartial(false)
			fr.SetDelay(0)
			f.env.KB.ApplyR3(r3Options())
			setup()
			fr.SetBoost(map[string]float64{"Nomenclatura de base de datos": 1}) // si reordenara, cambiaría el primero
			start := time.Now()
			res, err := f.env.KB.Search(f.env.Ctx(), f.env.ID(tm.Luis), service.SearchRequest{Q: r3Query, Mode: search.ModeHybrid, Rerank: "on", Limit: 5})
			if err != nil {
				t.Fatalf("un reordenador caído nunca es un error para quien busca: %v", err)
			}
			if time.Since(start) > 3*time.Second {
				t.Fatalf("la consulta no espera más que el presupuesto: %s", time.Since(start))
			}
			if docKeys(res) != docKeys(base) || res.NoResults {
				t.Fatalf("orden de la búsqueda intacto:\n esperado %s\n recibido %s", docKeys(base), docKeys(res))
			}
			if res.Rerank == nil || (res.Rerank.Status != search.RerankFailed && res.Rerank.Status != search.RerankPartial) || res.Rerank.Reason == "" {
				t.Fatalf("informe: %+v", res.Rerank)
			}
			if !strings.Contains(res.Notice, "Sin reordenamiento") || strings.Contains(res.Notice+res.Rerank.Reason, "sk-secreta") {
				t.Fatalf("aviso sin detalles del proveedor: %q", res.Notice)
			}
			if res.Vector == nil || res.Vector.Status != search.VectorUsed {
				t.Fatalf("la búsqueda por significado sigue igual: %+v", res.Vector)
			}
			for _, h := range res.Hits {
				if h.RerankScore != nil {
					t.Fatalf("sin reordenar no hay puntajes: %+v", h)
				}
			}
		})
	}
	// Recuperado el proveedor, la siguiente consulta reordena (no queda pegado).
	fr.SetFail(nil)
	fr.SetPartial(false)
	fr.SetDelay(0)
	f.env.KB.ApplyR3(r3Options())
	if res := f.searchRerank(t, tm.Luis, r3Query, search.ModeHybrid, "on", 5); res.Rerank.Status != search.RerankUsed {
		t.Fatalf("recuperado: %+v", res.Rerank)
	}
}

func TestR3NeverLowersTheThresholdNorSeesHiddenSpaces(t *testing.T) {
	f, fr := r3Setup(t, r3Options())
	env, tm := f.env, f.tm
	// Con el umbral de R2 por defecto, una pregunta sin respuesta sigue sin
	// respuesta aunque el reordenador "prefiera" cualquier cosa, y no hay ni
	// una llamada al proveedor.
	lo := f.env.KB.R2()
	o := lo
	o.MinSimilarity = 0.6
	env.KB.ApplyR2(o)
	fr.SetBoost(map[string]float64{"": 1}) // todo puntúa 1
	before := fr.Calls.Load()
	na := f.searchRerank(t, tm.Luis, "cuantas replicas de Kubernetes por pod", "", "on", 5)
	if !na.NoResults || fr.Calls.Load() != before {
		t.Fatalf("sin respuesta: %+v (llamadas %d -> %d)", na.Hits, before, fr.Calls.Load())
	}
	if na.Rerank == nil || na.Rerank.Status != search.RerankSkipped {
		t.Fatalf("informe: %+v", na.Rerank)
	}
	// Permisos: lo que no puede ver Luis nunca llega al proveedor.
	if _, err := env.KB.CreateSpace(env.Ctx(), env.ID(tm.Adm), service.CreateSpaceInput{Slug: "privado", Visibility: "restricted", Owner: "ana"}); err != nil {
		t.Fatal(err)
	}
	f.publish(t, "privado", service.PublishDoc{DocKey: "secreto/pagos", Category: "estandar", Content: "# Pagos reservados\n\n## Reintentos de pagos\n\nPOLITICA-RESERVADA-XYZ reintentos de pagos con tarjeta y niveles de log.\n"})
	f.drain(t)
	env.KB.ApplyR2(lo)
	fr.SetBoost(nil)
	for _, q := range []string{r3Query, "reintentos de pagos con tarjeta"} {
		for _, h := range f.searchRerank(t, tm.Luis, q, search.ModeHybrid, "on", 5).Hits {
			if h.DocKey == "secreto/pagos" {
				t.Fatalf("Luis no ve el espacio restringido: %+v", h)
			}
		}
	}
	for _, p := range fr.Seen() {
		if strings.Contains(p, "POLITICA-RESERVADA-XYZ") {
			t.Fatal("el texto de un espacio que Luis no ve no puede salir hacia el proveedor")
		}
	}
	res := f.searchRerank(t, tm.Ana, "reintentos de pagos con tarjeta", search.ModeHybrid, "on", 5)
	seen := false
	for _, p := range fr.Seen() {
		seen = seen || strings.Contains(p, "POLITICA-RESERVADA-XYZ")
	}
	if !hasDoc(res, "secreto/pagos") || !seen {
		t.Fatalf("Ana sí ve su espacio: %s", docKeys(res))
	}
}

func TestR3RequestParametersAndDefaults(t *testing.T) {
	// Sin reordenador: pedirlo avisa y responde igual; un valor inválido es 400.
	f := r2Setup(t)
	f.publish(t, "arq", r2Docs...)
	res := f.searchRerank(t, f.tm.Luis, "nombres de tabla", search.ModeFTS, "on", 5)
	if res.Rerank == nil || res.Rerank.Status != search.RerankUnavailable || !strings.Contains(res.Notice, "NEXO_KB_RERANKER") || len(res.Hits) == 0 {
		t.Fatalf("pedido sin reordenador: %+v %q", res.Rerank, res.Notice)
	}
	if f.env.KB.RerankEnabled() {
		t.Fatal("sin NEXO_KB_RERANKER no hay reordenador")
	}
	if res := f.searchRerank(t, f.tm.Luis, "nombres de tabla", search.ModeFTS, "", 5); res.Rerank != nil || res.Notice != "" {
		t.Fatalf("sin pedirlo y sin reordenador: %+v", res.Rerank)
	}
	if _, err := f.env.KB.Search(f.env.Ctx(), f.env.ID(f.tm.Luis), service.SearchRequest{Q: "x", Rerank: "quizas"}); !domain.IsKind(err, domain.KindInvalid) {
		t.Fatalf("valor inválido: %v", err)
	}
	// Con Default encendido y un proveedor, reordena sin pedirlo; off lo apaga.
	o := r3Options()
	o.Default = true
	g, _ := r3Setup(t, o)
	if res := g.searchRerank(t, g.tm.Luis, r3Query, search.ModeHybrid, "", 5); res.Rerank == nil || res.Rerank.Status != search.RerankUsed {
		t.Fatalf("Default encendido: %+v", res.Rerank)
	}
	if res := g.searchRerank(t, g.tm.Luis, r3Query, search.ModeHybrid, "off", 5); res.Rerank != nil {
		t.Fatalf("off apaga el valor por defecto: %+v", res.Rerank)
	}
	// Solo texto (sin embedder): el reordenamiento también funciona sobre fts.
	fr := kbtest.NewFakeReranker()
	env := kbtest.New(t, service.WithReranker(fr, fr.Config()), service.WithR3(r3Options()))
	tm := env.NewTeam()
	if _, err := env.KB.CreateSpace(env.Ctx(), env.ID(tm.Adm), service.CreateSpaceInput{Slug: "arq", Owner: "ana"}); err != nil {
		t.Fatal(err)
	}
	h := r2Fixture{env: env, tm: tm}
	h.publish(t, "arq", r2Docs...)
	fts := h.searchRerank(t, tm.Luis, "reintentos de logs", "", "on", 5)
	if fts.Rerank == nil || fts.Rerank.Status == search.RerankUnavailable {
		t.Fatalf("fts con reordenador: %+v", fts.Rerank)
	}
}

func TestR3AdminStateAndEvalTrace(t *testing.T) {
	// La clave de la configuración no sale en el estado ni en la corrida.
	ensureVectorExtension(t)
	fake := kbtest.NewFakeEmbedder()
	fr := kbtest.NewFakeReranker()
	rc := fr.Config()
	rc.APIKey = "sk-clave-que-no-debe-salir-123456"
	r2 := r2Options()
	r2.MinSimilarity = 0.01
	env := kbtest.New(t, service.WithEmbedder(fake, fake.FakeConfig()), service.WithR2(r2), service.WithReranker(fr, rc), service.WithR3(r3Options()))
	tm := env.NewTeam()
	if _, err := env.KB.CreateSpace(env.Ctx(), env.ID(tm.Adm), service.CreateSpaceInput{Slug: "arq", Owner: "ana"}); err != nil {
		t.Fatal(err)
	}
	f := r2Fixture{env: env, tm: tm, fake: fake}
	f.publish(t, "arq", r2Docs...)
	if d := f.drain(t); len(d.Activated) != 1 {
		t.Skipf("pgvector no disponible: %+v", d)
	}
	if _, err := env.KB.RerankState(env.Ctx(), env.ID(tm.Luis)); !domain.IsKind(err, domain.KindForbidden) {
		t.Fatalf("el estado exige platform.settings: %v", err)
	}
	st, err := env.KB.RerankState(env.Ctx(), env.ID(tm.Root))
	if err != nil || !st.Enabled || st.Reranker["provider"] != "fake" || st.Options.TopN != search.DefaultRerankOptions.TopN {
		t.Fatalf("estado: %+v %v", st, err)
	}
	raw, _ := json.Marshal(st)
	if strings.Contains(string(raw), "sk-clave") {
		t.Fatalf("la clave no puede salir en el estado: %s", raw)
	}

	// Rastro por pregunta y resumen en la corrida de evaluación.
	set, err := eval.Parse([]byte(`version: 2
name: r3
spaces: [arq]
questions:
  - {id: q1, class: normal, status: validated, question: "politica de reintentos para llamadas externas", expected: [{doc: backend/reintentos}]}
  - {id: q2, class: normal, status: validated, question: "niveles de log para errores recuperables", expected: [{doc: backend/logging, anchor: logging-en-microservicios.niveles-de-log}]}
  - {id: na, class: no_answer, status: validated, question: "cuantas replicas de Kubernetes por pod"}
`))
	if err != nil {
		t.Fatal(err)
	}
	r := service.NewEvalRetriever(env.KB, env.ID(tm.Luis))
	r.Mode, r.Rerank = "hybrid", "on"
	if r.Name() != "hybrid+rerank" {
		t.Fatalf("nombre del motor: %s", r.Name())
	}
	rep := eval.RunWith(context.Background(), set, r, eval.Options{})
	if rep.Rerank == nil || rep.Rerank.Questions < 2 || rep.Rerank.Used == 0 {
		t.Fatalf("resumen: %+v", rep.Rerank)
	}
	for _, q := range rep.Results {
		if q.Class == eval.ClassNormal && (q.Rerank == nil || q.Rerank.Status != search.RerankUsed || q.Rerank.RankBefore == 0) {
			t.Fatalf("rastro de %s: %+v", q.ID, q.Rerank)
		}
	}
	if line := rep.RerankLine(); !strings.Contains(line, "latencia agregada") {
		t.Fatalf("línea: %q", line)
	}
	run, err := env.KB.RecordEvalRun(env.Ctx(), env.ID(tm.Ana), service.RecordEvalRunInput{Report: rep, Config: env.KB.EvalConfig(), Mode: "hybrid+rerank", Spaces: []string{"arq"}, Note: "R3"})
	if err != nil || run.Mode != "hybrid+rerank" || run.Retriever != "hybrid+rerank" {
		t.Fatalf("corrida: %+v %v", run, err)
	}
	cfg, _ := json.Marshal(env.KB.EvalConfig())
	if !strings.Contains(string(cfg), `"reranker"`) || !strings.Contains(string(cfg), rerank.PromptVersion) || strings.Contains(string(cfg), "sk-clave") {
		t.Fatalf("configuración medida: %s", cfg)
	}
	// Lo guardado conserva el rastro por pregunta.
	var results string
	if err := env.DB.QueryRow(`SELECT results::text FROM nexo_kb_eval_runs WHERE id = $1::uuid`, run.ID).Scan(&results); err != nil || !strings.Contains(results, `"rank_before"`) || strings.Contains(results, "sk-clave") {
		t.Fatalf("results: %v %.200s", err, results)
	}
}
