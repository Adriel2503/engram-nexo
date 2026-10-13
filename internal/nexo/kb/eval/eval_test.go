package eval_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/eval"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/kbtest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/service"
)

type fakeRetriever struct{ answers map[string][]eval.Hit }

func (f fakeRetriever) Name() string { return "fake" }
func (f fakeRetriever) Search(_ context.Context, q eval.Question, _ []string, _ *time.Time, limit int) ([]eval.Hit, error) {
	h := f.answers[q.ID]
	if len(h) > limit {
		h = h[:limit]
	}
	return h, nil
}

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
	if diff := rep.MRR - want; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("MRR = %f, want %f", rep.MRR, want)
	}
	if !strings.Contains(rep.Summary(), "Recall@5 = 50.0 %") {
		t.Fatal(rep.Summary())
	}
}

func TestParseValidation(t *testing.T) {
	bad := []string{
		"version: 2\nquestions: [{question: x, expected: [{doc: a}]}]",
		"version: 1\nquestions: []",
		"questions: [{question: '', expected: [{doc: a}]}]",
		"questions: [{question: x}]",
		"questions: [{id: a, question: x, expected: [{doc: a}]}, {id: a, question: y, expected: [{doc: b}]}]",
		"questions: [{question: x, at: mañana, expected: [{doc: a}]}]",
	}
	for _, b := range bad {
		if _, err := eval.Parse([]byte(b)); err == nil {
			t.Errorf("debía rechazar %q", b)
		}
	}
}

// serviceRetriever evalúa el servicio en proceso (pgfts real, mismos
// filtros que el servidor).
type serviceRetriever struct {
	svc *service.Service
	id  authz.Identity
}

func (s serviceRetriever) Name() string { return "pgfts" }
func (s serviceRetriever) Search(ctx context.Context, q eval.Question, spaces []string, at *time.Time, limit int) ([]eval.Hit, error) {
	res, err := s.svc.Search(authz.WithRequestCache(ctx), s.id, service.SearchRequest{Q: q.Question, Spaces: spaces, At: at, Limit: limit})
	if err != nil {
		return nil, err
	}
	out := make([]eval.Hit, 0, len(res.Hits))
	for _, h := range res.Hits {
		out = append(out, eval.Hit{Space: h.Space, DocKey: h.DocKey, Anchor: h.Anchor})
	}
	return out, nil
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
	rep := eval.Run(context.Background(), set, serviceRetriever{svc: env.KB, id: env.ID(tm.Luis)}, 5, 10)
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
