package eval_test

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/eval"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/kbtest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/service"
)

type fakeRetriever struct {
	answers map[string][]eval.Hit
	empty   map[string]bool
}

func (f fakeRetriever) Name() string { return "fake" }
func (f fakeRetriever) Search(_ context.Context, q eval.Question, _ []string, _, _ *time.Time, limit int) (eval.Outcome, error) {
	h := f.answers[q.ID]
	if len(h) > limit {
		h = h[:limit]
	}
	return eval.Outcome{Hits: h, NoResults: f.empty[q.ID]}, nil
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestMetrics(t *testing.T) {
	set, err := eval.Parse([]byte(`
version: 1
questions:
  - {id: a, question: uno, expected: [{doc: d1}]}
  - {id: b, question: dos, expected: [{doc: d2, anchor: x}]}
  - {id: c, question: tres, expected: [{doc: d3}]}
  - {id: d, question: cuatro, expected: [{doc: d4, anchor: s}]}
`))
	if err != nil {
		t.Fatal(err)
	}
	other := eval.Hit{DocKey: "otro"}
	r := fakeRetriever{answers: map[string][]eval.Hit{
		"a": {{DocKey: "d1"}},                                    // posición 1
		"b": {other, other, {DocKey: "d2", Anchor: "x.sub"}},     // posición 3 (subsección)
		"c": {other, other, other, other, other, {DocKey: "d3"}}, // posición 6: fuera de @5, cuenta en MRR
		"d": {{DocKey: "d4", Anchor: "otra"}},                    // no acierta
	}}
	rep := eval.Run(context.Background(), set, r, 5, 10)
	if rep.Hits != 2 || rep.RecallAtK != 0.5 {
		t.Fatalf("Recall@5: %+v", rep)
	}
	want := (1 + 1.0/3 + 1.0/6) / 4
	if !near(rep.MRR, want) {
		t.Fatalf("MRR = %f, want %f", rep.MRR, want)
	}
	if !strings.Contains(rep.Summary(), "Recall@5 (acierto) = 50.0 %") {
		t.Fatal(rep.Summary())
	}
}

// Métricas portadas de WeKnora con las correcciones de Nexo.
func TestPortedMetrics(t *testing.T) {
	rel := []bool{false, true, false, true}
	// MAP dividido por el total de esperados (3), no por los encontrados (2).
	if got, want := eval.AveragePrecision(rel, 3), (1.0/2+2.0/4)/3; !near(got, want) {
		t.Fatalf("AP = %f, want %f", got, want)
	}
	// nDCG@10 con ideal min(3, 10): 3 aciertos ideales aunque haya 4 resultados.
	idcg := 1 + 1/math.Log2(3) + 1/math.Log2(4)
	dcg := 1/math.Log2(3) + 1/math.Log2(5)
	if got := eval.NDCGAtK(rel, 10, 3); !near(got, dcg/idcg) {
		t.Fatalf("nDCG = %f, want %f", got, dcg/idcg)
	}
	// El ideal no depende de cuántos resultados se devolvieron.
	if got := eval.NDCGAtK([]bool{true}, 10, 2); !near(got, 1/(1+1/math.Log2(3))) {
		t.Fatalf("nDCG con un resultado: %f", got)
	}
	if eval.RecallAtK(rel, 5, 4) != 0.5 || eval.HitAtK(rel, 1) != 0 || eval.HitAtK(rel, 2) != 1 || eval.PrecisionAtK(rel, 2) != 0.5 {
		t.Fatal("recall, hit o precisión")
	}
	if eval.ReciprocalRank(rel) != 0.5 || eval.ReciprocalRank(nil) != 0 {
		t.Fatal("RR")
	}
}

func TestClassesAndNoAnswer(t *testing.T) {
	set, err := eval.Parse([]byte(`
version: 2
name: prueba
questions:
  - {id: n, class: normal, question: uno, expected: [{doc: d1}]}
  - {id: x, class: no_answer, question: nada}
  - {id: y, class: no_answer, question: otra nada}
  - {id: t, class: temporal, question: antes, at: 2022-01-01, expected: [{doc: d1, version: 1}]}
  - {id: z, class: normal, question: falso no hay, expected: [{doc: d9}]}
`))
	if err != nil {
		t.Fatal(err)
	}
	r := fakeRetriever{answers: map[string][]eval.Hit{
		"n": {{DocKey: "d1"}},
		"y": {{DocKey: "d1"}},             // respondió algo: mal
		"t": {{DocKey: "d1", Version: 2}}, // versión equivocada
	}, empty: map[string]bool{"x": true, "z": true}}
	rep := eval.RunWith(context.Background(), set, r, eval.Options{})
	na := rep.ByClass[eval.ClassNoAnswer]
	if na.NoAnswerAccuracy == nil || *na.NoAnswerAccuracy != 0.5 {
		t.Fatalf("sin respuesta: %+v", na)
	}
	if rep.ByClass[eval.ClassTemporal].HitAt5 != 0 {
		t.Fatal("la versión debe coincidir en las temporales")
	}
	if rep.Total.FalseNoAnswer != 1 || rep.ByClass[eval.ClassNormal].HitAt5 != 0.5 {
		t.Fatalf("total: %+v", rep.Total)
	}
	if !strings.Contains(rep.Table(), "| no_answer | 2 |") {
		t.Fatal(rep.Table())
	}
	// Solo validadas: ninguna lo está.
	if rep := eval.RunWith(context.Background(), set, r, eval.Options{ValidatedOnly: true}); rep.Questions != 0 {
		t.Fatal("validated_only")
	}
	// changes sin ChangeLister: error contado, no pánico.
	cs, _ := eval.Parse([]byte("version: 2\nquestions: [{id: c, class: changes, question: q, since: 2026-01-01, expected: [{doc: d1, change: added}]}]"))
	if rep := eval.RunWith(context.Background(), cs, r, eval.Options{}); rep.Total.Errors != 1 {
		t.Fatal("changes sin lister")
	}
}

func TestParseValidation(t *testing.T) {
	bad := []string{
		"version: 3\nquestions: [{question: x, expected: [{doc: a}]}]",
		"version: 1\nquestions: []",
		"questions: [{question: '', expected: [{doc: a}]}]",
		"questions: [{question: x}]",
		"questions: [{id: a, question: x, expected: [{doc: a}]}, {id: a, question: y, expected: [{doc: b}]}]",
		"questions: [{question: x, at: mañana, expected: [{doc: a}]}]",
		"questions: [{question: x, class: rara, expected: [{doc: a}]}]",
		"questions: [{question: x, class: no_answer, expected: [{doc: a}]}]",
		"questions: [{question: x, class: changes, expected: [{doc: a}]}]",
	}
	for _, b := range bad {
		if _, err := eval.Parse([]byte(b)); err == nil {
			t.Errorf("debía rechazar %q", b)
		}
	}
}

// Línea base de calidad (C-10) del pgfts sobre los lineamientos de prueba.
func TestBaselineAgainstPGFTS(t *testing.T) {
	env := kbtest.New(t)
	tm := env.NewTeam()
	if _, err := env.KB.CreateSpace(env.Ctx(), env.ID(tm.Adm), service.CreateSpaceInput{Slug: "lineamientos-frontend", Owner: "ana"}); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join("..", "ingest", "testdata", "lineamientos-frontend")
	var docs []service.PublishDoc
	for _, rel := range []string{"frontend/formularios.md", "frontend/componentes.md", "glosario.txt"} {
		b, err := os.ReadFile(filepath.Join(base, rel))
		if err != nil {
			t.Fatal(err)
		}
		docs = append(docs, service.PublishDoc{Path: rel, Content: string(b), Category: "glosario"})
	}
	if _, err := env.KB.PublishBatch(env.Ctx(), env.ID(tm.Ana), "lineamientos-frontend", service.BatchRequest{Documents: docs}); err != nil {
		t.Fatal(err)
	}
	set, err := eval.Load(filepath.Join("testdata", "preguntas.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	rep := eval.Run(context.Background(), set, service.NewEvalRetriever(env.KB, env.ID(tm.Luis)), 5, 10)
	t.Log(rep.Summary())
	for _, r := range rep.Results {
		if r.Rank == 0 || r.Rank > 5 {
			t.Logf("sin acierto en @5: %s (%q) rank=%d", r.ID, r.Question, r.Rank)
		}
	}
	if rep.Errors != 0 || rep.RecallAtK < 0.6 {
		t.Fatalf("línea base por debajo de lo esperado: %s", rep.Summary())
	}
}
