package search

// Pruebas del reordenamiento (R3) sin base de datos: un motor simulado
// devuelve candidatos fijos y un reordenador simulado puntúa. Cubren el orden,
// la mezcla, la caída segura (error, respuesta parcial, pánico, tiempo
// agotado), que el umbral y el filtro común no los pueda saltar el
// reordenador (RC-21) y los parámetros de la consulta.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/rerank"
)

// fixedRetriever devuelve hits fijos y registra la consulta recibida.
type fixedRetriever struct {
	hits []Hit
	got  Query
	n    int
}

func (r *fixedRetriever) Name() string { return "fixed" }
func (r *fixedRetriever) Search(_ context.Context, q Query, _ Filter) ([]Hit, error) {
	r.got = q
	r.n++
	return append([]Hit(nil), r.hits...), nil
}

// spyReranker puntúa con score(id) y registra qué recibió.
type spyReranker struct {
	mu       sync.Mutex
	calls    int
	received [][]rerank.Passage
	queries  []string
	score    func(p rerank.Passage) rerank.Score
	err      error
	panics   bool
	block    bool
}

func (s *spyReranker) Name() string  { return "spy" }
func (s *spyReranker) Model() string { return "spy-1" }
func (s *spyReranker) Rerank(ctx context.Context, q string, ps []rerank.Passage) ([]rerank.Score, error) {
	s.mu.Lock()
	s.calls++
	s.received = append(s.received, append([]rerank.Passage(nil), ps...))
	s.queries = append(s.queries, q)
	s.mu.Unlock()
	if s.panics {
		panic("boom")
	}
	if s.block {
		<-ctx.Done()
		return make([]rerank.Score, len(ps)), ctx.Err()
	}
	out := make([]rerank.Score, len(ps))
	for i, p := range ps {
		out[i] = s.score(p)
	}
	return out, s.err
}

func scoreByID(m map[string]float64) func(rerank.Passage) rerank.Score {
	return func(p rerank.Passage) rerank.Score {
		v, ok := m[p.ID]
		return rerank.Score{Value: v, OK: ok}
	}
}

// ropts parte de los valores por defecto (orden puro del reordenador).
func ropts(f func(*RerankOptions)) RerankOptions {
	o := DefaultRerankOptions
	o.Blend = 1 // las pruebas de orden esperan el orden puro del reordenador
	f(&o)
	return o
}

var rerankNow = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

func rhit(id, doc string, rank, cov float64) Hit {
	d := 24 * time.Hour
	return Hit{SectionID: id, SpaceID: "visible", DocumentID: doc, DocKey: doc, Title: "Doc " + doc, HeadingPath: []string{"H " + id}, Excerpt: "extracto " + id,
		Rank: rank, Coverage: cov, ValidFrom: rerankNow.Add(-30 * d), RecordedFrom: rerankNow.Add(-30 * d), Anchor: id, Version: 1}
}

func newRerankSearcher(r Retriever, rr rerank.Reranker, o RerankOptions) (*Searcher, *[]string) {
	s := NewSearcher(r)
	s.now = func() time.Time { return rerankNow }
	var loaded []string
	load := func(_ context.Context, ids []string) (map[string]string, error) {
		loaded = append(loaded, ids...)
		out := map[string]string{}
		for _, id := range ids {
			out[id] = "texto completo de " + id
		}
		return out, nil
	}
	s.SetReranker(rr, load, o)
	return s, &loaded
}

func ids(hits []Hit) string {
	var out []string
	for _, h := range hits {
		out = append(out, h.SectionID)
	}
	return strings.Join(out, ",")
}

func TestRerankReordersByScoreWithSearchOrderAsTieBreak(t *testing.T) {
	r := &fixedRetriever{hits: []Hit{rhit("a", "d1", 1, 1), rhit("b", "d2", 0.9, 1), rhit("c", "d3", 0.8, 1), rhit("d", "d4", 0.7, 1)}}
	spy := &spyReranker{score: scoreByID(map[string]float64{"a": 0.2, "b": 0.9, "c": 0.9, "d": 0.5})}
	s, loaded := newRerankSearcher(r, spy, RerankOptions{TopN: 50, Blend: 1})
	res, err := s.Search(context.Background(), Query{Text: "pregunta", Rerank: "on"}, []string{"visible"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(res.Hits); got != "b,c,d,a" {
		t.Fatalf("orden %s: b y c empatan y conservan el orden de la búsqueda; a baja", got)
	}
	info := res.Rerank
	if info == nil || info.Status != RerankUsed || info.Candidates != 4 || info.Scored != 4 || info.Reordered != 4 || !info.TopChanged || info.Provider != "spy" || info.Model != "spy-1" || info.Blend != 1 {
		t.Fatalf("informe: %+v", info)
	}
	if res.Hits[0].RerankScore == nil || *res.Hits[0].RerankScore != 0.9 || res.Hits[0].PreRerankPosition != 2 || res.Hits[3].PreRerankPosition != 1 {
		t.Fatalf("puntaje y posición previa: %+v", res.Hits[0])
	}
	if res.Notice != "" || res.NoResults {
		t.Fatalf("sin aviso: %q", res.Notice)
	}
	// El reordenador recibió el pasaje completo (no el extracto) y la pregunta.
	if spy.queries[0] != "pregunta" || spy.received[0][0].Text != "texto completo de a" || strings.Join(*loaded, ",") != "a,b,c,d" {
		t.Fatalf("pasajes: %+v cargados %v", spy.received[0], *loaded)
	}
	// El motor recibió un límite que alcanza para los N candidatos.
	if r.got.Limit < 50 {
		t.Fatalf("el motor debe traer al menos TopN candidatos: %d", r.got.Limit)
	}
}

func TestRerankBlendMixesSearchOrder(t *testing.T) {
	hits := []Hit{rhit("a", "d1", 1, 1), rhit("b", "d2", 0.5, 1), rhit("c", "d3", 0, 1)}
	score := map[string]float64{"a": 0.5, "b": 0.6, "c": 0.9}
	order := func(blend float64) string {
		s, _ := newRerankSearcher(&fixedRetriever{hits: hits}, &spyReranker{score: scoreByID(score)}, RerankOptions{Blend: blend})
		res, err := s.Search(context.Background(), Query{Text: "q", Rerank: "on"}, []string{"visible"}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		return ids(res.Hits)
	}
	if got := order(1); got != "c,b,a" {
		t.Fatalf("orden puro del reordenador: %s", got)
	}
	// 0,3 del reordenador y 0,7 de la búsqueda (min-max: a=1, b=0,5, c=0).
	if got := order(0.3); got != "a,b,c" {
		t.Fatalf("mezcla 0,3: %s", got)
	}
	// 0,6: a = 0,3+0,4 = 0,70; b = 0,36+0,2 = 0,56; c = 0,54.
	if got := order(0.6); got != "a,b,c" {
		t.Fatalf("mezcla 0,6: %s", got)
	}
	if got := order(0); got != "a,b,c" {
		t.Fatalf("Blend 0 = orden de la búsqueda: %s", got)
	}
}

func TestRerankFallsBackToSearchOrder(t *testing.T) {
	base := []Hit{rhit("a", "d1", 1, 1), rhit("b", "d2", 0.9, 1), rhit("c", "d3", 0.8, 1)}
	cases := map[string]struct {
		spy    *spyReranker
		status string
		reason string
	}{
		"error transitorio":              {&spyReranker{score: scoreByID(nil), err: fmt.Errorf("%w: caído", rerank.ErrUnavailable)}, RerankFailed, "no respondió"},
		"respuesta no válida":            {&spyReranker{score: scoreByID(nil), err: fmt.Errorf("%w: basura", rerank.ErrMalformed)}, RerankFailed, "no válida"},
		"parcial":                        {&spyReranker{score: scoreByID(map[string]float64{"a": 0.1, "b": 0.9}), err: errors.New("lote 2 falló")}, RerankPartial, "2 de 3"},
		"sin error pero faltan puntajes": {&spyReranker{score: scoreByID(map[string]float64{"a": 0.1, "b": 0.9})}, RerankPartial, "2 de 3"},
		"pánico":                         {&spyReranker{score: scoreByID(nil), panics: true}, RerankFailed, "falló"},
		"error raro":                     {&spyReranker{score: scoreByID(nil), err: errors.New("sk-clave-secreta explotó")}, RerankFailed, "falló"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s, _ := newRerankSearcher(&fixedRetriever{hits: base}, tc.spy, ropts(func(*RerankOptions) {}))
			res, err := s.Search(context.Background(), Query{Text: "q", Rerank: "on"}, []string{"visible"}, nil, nil)
			if err != nil {
				t.Fatalf("nunca es un error para quien busca: %v", err)
			}
			if got := ids(res.Hits); got != "a,b,c" {
				t.Fatalf("orden de la búsqueda intacto: %s", got)
			}
			for _, h := range res.Hits {
				if h.RerankScore != nil || h.PreRerankPosition != 0 {
					t.Fatalf("sin reordenar no hay puntaje: %+v", h)
				}
			}
			if res.Rerank == nil || res.Rerank.Status != tc.status || !strings.Contains(res.Rerank.Reason, tc.reason) || res.NoResults {
				t.Fatalf("informe: %+v", res.Rerank)
			}
			if !strings.Contains(res.Notice, "Sin reordenamiento") || !strings.Contains(res.Notice, "orden de la búsqueda") {
				t.Fatalf("aviso: %q", res.Notice)
			}
			// Quien busca no ve el detalle del proveedor.
			if strings.Contains(res.Notice+res.Rerank.Reason, "sk-clave") {
				t.Fatalf("el motivo no repite el error del proveedor: %q", res.Rerank.Reason)
			}
		})
	}
}

func TestRerankBudgetTimeoutFallsBack(t *testing.T) {
	spy := &spyReranker{block: true}
	s, _ := newRerankSearcher(&fixedRetriever{hits: []Hit{rhit("a", "d1", 1, 1), rhit("b", "d2", 0.9, 1)}}, spy, RerankOptions{Budget: 60 * time.Millisecond})
	start := time.Now()
	res, err := s.Search(context.Background(), Query{Text: "q", Rerank: "on"}, []string{"visible"}, nil, nil)
	if err != nil || time.Since(start) > 2*time.Second {
		t.Fatalf("la consulta no espera más que el presupuesto: %v en %s", err, time.Since(start))
	}
	if ids(res.Hits) != "a,b" || res.Rerank.Status != RerankFailed || !strings.Contains(res.Rerank.Reason, "tiempo") {
		t.Fatalf("tiempo agotado: %+v %s", res.Rerank, ids(res.Hits))
	}
	if res.Rerank.LatencyMS < 50 {
		t.Fatalf("la latencia agregada queda registrada: %d ms", res.Rerank.LatencyMS)
	}
}

func TestRerankNeverLowersTheThresholdNorBypassesFilters(t *testing.T) {
	good := rhit("good", "d1", 1, 0.9)
	weak := rhit("weak", "d2", 0.9, 0.1) // cobertura bajo el umbral
	other := rhit("other", "d3", 0.8, 1)
	other.SpaceID = "restringido"
	r := &fixedRetriever{hits: []Hit{good, weak, other, rhit("ok2", "d4", 0.7, 0.8)}}
	// El reordenador "prefiere" lo descartado: no debe volver.
	spy := &spyReranker{score: func(p rerank.Passage) rerank.Score {
		if p.ID == "weak" || p.ID == "other" {
			return rerank.Score{Value: 1, OK: true}
		}
		return rerank.Score{Value: 0.1, OK: true}
	}}
	s, _ := newRerankSearcher(r, spy, ropts(func(*RerankOptions) {}))
	s.SetOptions(SearcherOptions{MinCoverage: 0.3})
	res, err := s.Search(context.Background(), Query{Text: "q", Rerank: "on"}, []string{"visible"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ids(res.Hits) != "good,ok2" {
		t.Fatalf("lo bajo el umbral o fuera de los permisos no se rescata: %s", ids(res.Hits))
	}
	for _, p := range spy.received[0] {
		if p.ID == "weak" || p.ID == "other" {
			t.Fatalf("el reordenador no debe recibir lo descartado por el umbral ni por permisos: %v", spy.received[0])
		}
	}
	if res.Dropped != 1 || res.Relevance == nil || res.Relevance.Below != 1 || res.Relevance.Above != 2 {
		t.Fatalf("el umbral se cuenta igual: dropped %d, %+v", res.Dropped, res.Relevance)
	}

	// Todo bajo el umbral: "no hay estándar" sin una sola llamada al proveedor.
	r2 := &fixedRetriever{hits: []Hit{rhit("w1", "d1", 1, 0.1), rhit("w2", "d2", 0.9, 0.2)}}
	spy2 := &spyReranker{score: scoreByID(map[string]float64{"w1": 1, "w2": 1})}
	s2, _ := newRerankSearcher(r2, spy2, ropts(func(*RerankOptions) {}))
	s2.SetOptions(SearcherOptions{MinCoverage: 0.3})
	res, err = s2.Search(context.Background(), Query{Text: "q", Rerank: "on"}, []string{"visible"}, nil, nil)
	if err != nil || !res.NoResults || res.Message != NoResultsMessage || spy2.calls != 0 {
		t.Fatalf("sin respuesta: %+v llamadas %d %v", res, spy2.calls, err)
	}
	if res.Rerank == nil || res.Rerank.Status != RerankSkipped {
		t.Fatalf("informe: %+v", res.Rerank)
	}
}

func TestRerankRequestParameters(t *testing.T) {
	hits := []Hit{rhit("a", "d1", 1, 1), rhit("b", "d2", 0.9, 1)}
	score := scoreByID(map[string]float64{"a": 0.1, "b": 0.9})

	// Sin reordenador: pedirlo avisa, no pedirlo no dice nada.
	s := NewSearcher(&fixedRetriever{hits: hits})
	s.now = func() time.Time { return rerankNow }
	res, _ := s.Search(context.Background(), Query{Text: "q", Rerank: "on"}, []string{"visible"}, nil, nil)
	if res.Rerank == nil || res.Rerank.Status != RerankUnavailable || !strings.Contains(res.Notice, "NEXO_KB_RERANKER") || ids(res.Hits) != "a,b" {
		t.Fatalf("pedido sin reordenador: %+v %q", res.Rerank, res.Notice)
	}
	res, _ = s.Search(context.Background(), Query{Text: "q"}, []string{"visible"}, nil, nil)
	if res.Rerank != nil || res.Notice != "" {
		t.Fatalf("sin pedirlo y sin reordenador, la respuesta es la de siempre: %+v", res.Rerank)
	}

	// Por defecto apagado: solo con rerank=on. Por defecto encendido: rerank=off lo apaga.
	spy := &spyReranker{score: score}
	s, _ = newRerankSearcher(&fixedRetriever{hits: hits}, spy, ropts(func(*RerankOptions) {}))
	res, _ = s.Search(context.Background(), Query{Text: "q"}, []string{"visible"}, nil, nil)
	if spy.calls != 0 || res.Rerank != nil || ids(res.Hits) != "a,b" {
		t.Fatalf("Default=false sin pedirlo: llamadas %d", spy.calls)
	}
	res, _ = s.Search(context.Background(), Query{Text: "q", Rerank: "TRUE"}, []string{"visible"}, nil, nil)
	if spy.calls != 1 || ids(res.Hits) != "b,a" {
		t.Fatalf("rerank=on: %s", ids(res.Hits))
	}
	spy = &spyReranker{score: score}
	s, _ = newRerankSearcher(&fixedRetriever{hits: hits}, spy, ropts(func(o *RerankOptions) { o.Default = true }))
	res, _ = s.Search(context.Background(), Query{Text: "q"}, []string{"visible"}, nil, nil)
	if spy.calls != 1 || ids(res.Hits) != "b,a" || res.Rerank.Status != RerankUsed {
		t.Fatalf("Default=true: %s", ids(res.Hits))
	}
	res, _ = s.Search(context.Background(), Query{Text: "q", Rerank: "off"}, []string{"visible"}, nil, nil)
	if spy.calls != 1 || ids(res.Hits) != "a,b" || res.Rerank != nil {
		t.Fatalf("rerank=off con Default=true: llamadas %d", spy.calls)
	}
	// "auto" y vacío = lo que decide el servidor.
	if _, err := s.Search(context.Background(), Query{Text: "q", Rerank: "auto"}, []string{"visible"}, nil, nil); err != nil || spy.calls != 2 {
		t.Fatalf("auto: %v %d", err, spy.calls)
	}
	// Valor inválido: 400.
	if _, err := s.Search(context.Background(), Query{Text: "q", Rerank: "quizá"}, []string{"visible"}, nil, nil); !domain.IsKind(err, domain.KindInvalid) {
		t.Fatalf("valor inválido: %v", err)
	}
	// El modo exact no se reordena (aunque se pida, con aviso en el informe).
	spy = &spyReranker{score: score}
	s, _ = newRerankSearcher(&fixedRetriever{hits: hits}, spy, ropts(func(o *RerankOptions) { o.Default = true }))
	res, _ = s.Search(context.Background(), Query{Text: "q", Mode: ModeExact, Rerank: "on"}, []string{"visible"}, nil, nil)
	if spy.calls != 0 || res.Rerank == nil || res.Rerank.Status != RerankSkipped || ids(res.Hits) != "a,b" {
		t.Fatalf("exact: %+v llamadas %d", res.Rerank, spy.calls)
	}
	res, _ = s.Search(context.Background(), Query{Text: "q", Mode: ModeExact}, []string{"visible"}, nil, nil)
	if spy.calls != 0 || res.Rerank != nil {
		t.Fatalf("exact con el valor por defecto: %+v", res.Rerank)
	}
}

func TestRerankTopNLimitsWhatIsSent(t *testing.T) {
	hits := []Hit{rhit("a", "d1", 1, 1), rhit("b", "d2", 0.9, 1), rhit("c", "d3", 0.8, 1), rhit("d", "d4", 0.7, 1)}
	spy := &spyReranker{score: scoreByID(map[string]float64{"a": 0.1, "b": 0.9, "c": 0.5, "d": 1})}
	s, _ := newRerankSearcher(&fixedRetriever{hits: hits}, spy, RerankOptions{TopN: 3, Blend: 1})
	res, _ := s.Search(context.Background(), Query{Text: "q", Rerank: "on"}, []string{"visible"}, nil, nil)
	// d queda fuera del TopN: el reordenador no lo ve y conserva su lugar al final.
	if len(spy.received[0]) != 3 || ids(res.Hits) != "b,c,a,d" || res.Rerank.Candidates != 3 || res.Rerank.TopN != 3 {
		t.Fatalf("TopN=3: recibió %d, orden %s, %+v", len(spy.received[0]), ids(res.Hits), res.Rerank)
	}
	if res.Hits[3].RerankScore != nil {
		t.Fatalf("lo que no se envió no tiene puntaje: %+v", res.Hits[3])
	}
	// Un solo candidato: no hay nada que reordenar.
	spy = &spyReranker{score: scoreByID(nil)}
	s, _ = newRerankSearcher(&fixedRetriever{hits: hits[:1]}, spy, ropts(func(*RerankOptions) {}))
	res, _ = s.Search(context.Background(), Query{Text: "q", Rerank: "on"}, []string{"visible"}, nil, nil)
	if spy.calls != 0 || res.Rerank.Status != RerankSkipped || ids(res.Hits) != "a" {
		t.Fatalf("un candidato: %+v", res.Rerank)
	}
	// TopN por encima del máximo se recorta al límite de la búsqueda.
	if s.rerankTopN() != DefaultRerankOptions.TopN || DefaultRerankOptions.TopN != 20 {
		t.Fatalf("tope %d", s.rerankTopN())
	}
	s.SetReranker(spy, nil, RerankOptions{TopN: 500})
	if s.rerankTopN() != MaxRerankTopN {
		t.Fatalf("tope %d", s.rerankTopN())
	}
}

func TestRerankMinScoreDropsLowScoresOnlyWhenConfigured(t *testing.T) {
	hits := []Hit{rhit("a", "d1", 1, 1), rhit("b", "d2", 0.9, 1), rhit("c", "d3", 0.8, 1)}
	score := scoreByID(map[string]float64{"a": 0.05, "b": 0.8, "c": 0.2})
	s, _ := newRerankSearcher(&fixedRetriever{hits: hits}, &spyReranker{score: score}, ropts(func(o *RerankOptions) { o.MinScore = 0.15 }))
	res, _ := s.Search(context.Background(), Query{Text: "q", Rerank: "on"}, []string{"visible"}, nil, nil)
	if ids(res.Hits) != "b,c" || res.Rerank.Dropped != 1 || res.Rerank.Status != RerankUsed {
		t.Fatalf("MinScore 0,15: %s %+v", ids(res.Hits), res.Rerank)
	}
	// Todo por debajo: el reordenador calibrado puede decir "no hay".
	s, _ = newRerankSearcher(&fixedRetriever{hits: hits}, &spyReranker{score: score}, ropts(func(o *RerankOptions) { o.MinScore = 0.99 }))
	res, _ = s.Search(context.Background(), Query{Text: "q", Rerank: "on"}, []string{"visible"}, nil, nil)
	if !res.NoResults || res.Message != NoResultsMessage || res.Rerank.Dropped != 3 {
		t.Fatalf("todo descartado: %+v", res.Rerank)
	}
	// Apagado (0): nada se descarta aunque el puntaje sea bajo.
	s, _ = newRerankSearcher(&fixedRetriever{hits: hits}, &spyReranker{score: score}, ropts(func(*RerankOptions) {}))
	res, _ = s.Search(context.Background(), Query{Text: "q", Rerank: "on"}, []string{"visible"}, nil, nil)
	if len(res.Hits) != 3 || res.Rerank.Dropped != 0 {
		t.Fatalf("MinScore apagado: %+v", res.Rerank)
	}
}

func TestRerankThenDiversityAndLimit(t *testing.T) {
	// Tres secciones del mismo documento y una de otro; el reordenador pone a
	// la tercera del documento 1 arriba: el tope por documento (2) se aplica
	// sobre el orden nuevo y el límite corta al final.
	hits := []Hit{rhit("a1", "d1", 1, 1), rhit("a2", "d1", 0.9, 1), rhit("a3", "d1", 0.8, 1), rhit("b1", "d2", 0.7, 1)}
	spy := &spyReranker{score: scoreByID(map[string]float64{"a1": 0.1, "a2": 0.2, "a3": 0.9, "b1": 0.5})}
	s, _ := newRerankSearcher(&fixedRetriever{hits: hits}, spy, ropts(func(*RerankOptions) {}))
	s.SetOptions(SearcherOptions{MaxPerDoc: 2})
	res, _ := s.Search(context.Background(), Query{Text: "q", Limit: 3, Rerank: "on"}, []string{"visible"}, nil, nil)
	if ids(res.Hits) != "a3,b1,a2" {
		t.Fatalf("diversidad y límite sobre el orden reordenado: %s", ids(res.Hits))
	}
	if res.Hits[0].Citation == "" || res.Hits[0].Link == "" {
		t.Fatalf("los resultados reordenados siguen citables: %+v", res.Hits[0])
	}
}
