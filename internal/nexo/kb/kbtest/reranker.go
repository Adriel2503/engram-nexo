package kbtest

// Reordenador simulado y determinista para las pruebas sin red (NX-10 R3): el
// puntaje de un pasaje es la fracción de los conceptos de la pregunta que el
// pasaje contiene (los mismos conceptos y raíces de 5 letras del embedder
// simulado, FakeEmbedder), en [0, 1]. Sirve para probar la tubería (orden,
// mezcla, umbral, permisos, caída segura) y la compuerta de calidad de CI; la
// calidad real se mide con un modelo (design §21).

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/ingest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/rerank"
)

// FakeReranker implementa rerank.Reranker.
type FakeReranker struct {
	ModelName string
	// Calls y Passages cuentan las llamadas y los pasajes puntuados.
	Calls    atomic.Int64
	Passages atomic.Int64

	mu sync.Mutex
	// Fail hace fallar todas las llamadas con ese error (proveedor caído).
	fail error
	// delay hace esperar cada llamada (o hasta que venza el contexto).
	delay time.Duration
	// partial deja sin puntaje (OK falso) al último pasaje de cada llamada.
	partial bool
	// seen guarda los textos de todas las llamadas, para comprobar qué ve el
	// proveedor.
	seen []string
	// queries guarda las preguntas recibidas.
	queries []string
	// boost suma (o resta, si es negativo) puntaje a los pasajes que contienen la cadena (fuerza un
	// orden distinto del de la búsqueda).
	boost map[string]float64
}

// NewFakeReranker crea el reordenador simulado.
func NewFakeReranker() *FakeReranker { return &FakeReranker{ModelName: "fake-overlap-v1"} }

// Config es la configuración que acompaña al reordenador simulado.
func (f *FakeReranker) Config() rerank.Config {
	return rerank.Config{Provider: "fake", Model: f.ModelName, Batch: 25, Concurrency: 1}
}

// SetFail cambia el error de todas las llamadas (nil = funciona).
func (f *FakeReranker) SetFail(err error) {
	f.mu.Lock()
	f.fail = err
	f.mu.Unlock()
}

// SetDelay hace esperar cada llamada.
func (f *FakeReranker) SetDelay(d time.Duration) {
	f.mu.Lock()
	f.delay = d
	f.mu.Unlock()
}

// SetPartial deja el último pasaje de cada llamada sin puntaje.
func (f *FakeReranker) SetPartial(v bool) {
	f.mu.Lock()
	f.partial = v
	f.mu.Unlock()
}

// SetBoost suma o resta puntaje (el resultado se acota a [0, 1]) a los pasajes que contienen cada cadena.
func (f *FakeReranker) SetBoost(m map[string]float64) {
	f.mu.Lock()
	f.boost = m
	f.mu.Unlock()
}

// Seen devuelve los textos de todos los pasajes que recibió.
func (f *FakeReranker) Seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...)
}

// Queries devuelve las preguntas que recibió.
func (f *FakeReranker) Queries() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.queries...)
}

// Name implementa rerank.Reranker.
func (f *FakeReranker) Name() string { return "fake" }

// Model implementa rerank.Reranker.
func (f *FakeReranker) Model() string { return f.ModelName }

// Rerank implementa rerank.Reranker.
func (f *FakeReranker) Rerank(ctx context.Context, query string, passages []rerank.Passage) ([]rerank.Score, error) {
	f.Calls.Add(1)
	f.mu.Lock()
	fail, delay, partial, boost := f.fail, f.delay, f.partial, f.boost
	f.queries = append(f.queries, query)
	for _, p := range passages {
		f.seen = append(f.seen, p.Text)
	}
	f.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return make([]rerank.Score, len(passages)), ctx.Err()
		}
	}
	if fail != nil {
		return make([]rerank.Score, len(passages)), fail
	}
	q := fakeConceptSet(query)
	out := make([]rerank.Score, len(passages))
	for i, p := range passages {
		v := 0.0
		if len(q) > 0 {
			ps := fakeConceptSet(p.Text)
			n := 0
			for c := range q {
				if ps[c] {
					n++
				}
			}
			v = float64(n) / float64(len(q))
		}
		for sub, b := range boost {
			if strings.Contains(p.Text, sub) {
				v = min(1, max(0, v+b))
			}
		}
		out[i] = rerank.Score{Value: v, OK: true}
	}
	if partial && len(out) > 0 {
		out[len(out)-1] = rerank.Score{}
	}
	f.Passages.Add(int64(len(passages)))
	return out, nil
}

// fakeConceptSet son los conceptos de un texto (mismo léxico que FakeVector).
func fakeConceptSet(text string) map[string]bool {
	out := map[string]bool{}
	for _, w := range fakeWordRe.FindAllString(strings.ToLower(ingest.Fold(text)), -1) {
		if fakeStop[w] || len(w) < 2 {
			continue
		}
		c, ok := fakeConcepts[w]
		if !ok {
			c = w
			if len([]rune(c)) > 5 {
				c = string([]rune(c)[:5])
			}
		}
		out[c] = true
	}
	return out
}
