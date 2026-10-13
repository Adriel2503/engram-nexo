package service_test

// Medición R3 (reordenamiento) sobre la wiki, fuera del CI: se suma a
// TestEvalWikiCorpus (wiki_eval_test.go). Variables de entorno:
//
//	NEXO_KB_RERANKER y NEXO_KB_RERANK_*  proveedor (por ejemplo gemini con GEMINI_API_KEY)
//	NEXO_KB_RERANK_CACHE_FILE                 caché de puntajes (por modelo, pregunta y pasaje): una
//	                                     corrida nueva con la misma pregunta y los mismos
//	                                     pasajes no vuelve a llamar al proveedor
//	NEXO_KB_EVAL_RERANKS                 variantes a comparar, separadas por ";":
//	                                     nombre=topN:mezcla:puntaje_minimo (siempre se mide antes
//	                                     la línea base hybrid, sin reordenar)
//	NEXO_KB_EVAL_MD                      archivo Markdown con tablas comparativas y diferencias
//	                                     por pregunta (evidencia)

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/eval"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/rerank"
)

// cachedReranker guarda los puntajes en un archivo (una línea JSON por
// pasaje; clave = hash del proveedor, modelo, versión del prompt, pregunta y
// texto del pasaje) para no volver a pagar la misma llamada entre corridas.
// Además mide la latencia de las llamadas que sí llegaron al proveedor.
type cachedReranker struct {
	rerank.Reranker
	path string
	mu   sync.Mutex
	vals map[string]float64
	// Hits y Misses cuentan pasajes; Live, la duración de cada llamada real.
	Hits, Misses int
	Live         []time.Duration
}

func newCachedReranker(t testing.TB, r rerank.Reranker, path string) *cachedReranker {
	c := &cachedReranker{Reranker: r, path: path, vals: map[string]float64{}}
	if path == "" {
		return c
	}
	f, err := os.Open(path)
	if err != nil {
		return c
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var row struct {
			K string  `json:"k"`
			V float64 `json:"v"`
		}
		if json.Unmarshal(sc.Bytes(), &row) == nil && row.K != "" {
			c.vals[row.K] = row.V
		}
	}
	t.Logf("caché de puntajes: %d en %s", len(c.vals), path)
	return c
}

func (c *cachedReranker) key(query, text string) string {
	h := sha256.Sum256([]byte(strings.Join([]string{c.Name(), c.Model(), rerank.PromptVersion, strings.TrimSpace(query), text}, "\x00")))
	return hex.EncodeToString(h[:])
}

func (c *cachedReranker) Rerank(ctx context.Context, query string, passages []rerank.Passage) ([]rerank.Score, error) {
	out := make([]rerank.Score, len(passages))
	var miss []rerank.Passage
	var missIdx []int
	c.mu.Lock()
	for i, p := range passages {
		if v, ok := c.vals[c.key(query, p.Text)]; ok {
			out[i] = rerank.Score{Value: v, OK: true}
			c.Hits++
		} else {
			miss, missIdx = append(miss, p), append(missIdx, i)
		}
	}
	c.mu.Unlock()
	if len(miss) == 0 {
		return out, nil
	}
	start := time.Now()
	got, err := c.Reranker.Rerank(ctx, query, miss)
	took := time.Since(start)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Live = append(c.Live, took)
	c.Misses += len(miss)
	var f *os.File
	if c.path != "" {
		f, _ = os.OpenFile(c.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	}
	for j, i := range missIdx {
		if j >= len(got) || !got[j].OK {
			continue
		}
		out[i] = got[j]
		k := c.key(query, miss[j].Text)
		c.vals[k] = got[j].Value
		if f != nil {
			raw, _ := json.Marshal(map[string]any{"k": k, "v": got[j].Value})
			_, _ = f.Write(append(raw, '\n'))
		}
	}
	if f != nil {
		_ = f.Close()
	}
	return out, err
}

// Stats reenvía el uso del proveedor (llamadas, tokens).
func (c *cachedReranker) Stats() rerank.Stats {
	if s, ok := c.Reranker.(rerank.StatsReporter); ok {
		return s.Stats()
	}
	return rerank.Stats{}
}

// liveLatency resume las llamadas reales al proveedor desde el índice from.
func (c *cachedReranker) liveLatency(from int) (n int, p50, p95, worst time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if from > len(c.Live) {
		from = len(c.Live)
	}
	l := append([]time.Duration(nil), c.Live[from:]...)
	if len(l) == 0 {
		return 0, 0, 0, 0
	}
	sort.Slice(l, func(i, j int) bool { return l[i] < l[j] })
	at := func(p float64) time.Duration {
		i := int(p*float64(len(l))+0.999999) - 1
		return l[min(max(i, 0), len(l)-1)]
	}
	return len(l), at(0.5), at(0.95), l[len(l)-1]
}

func (c *cachedReranker) liveCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.Live)
}

// parseRerankSpecs lee NEXO_KB_EVAL_RERANKS: nombre=topN:mezcla:puntaje_minimo.
func parseRerankSpecs(t testing.TB, raw string) []evalConfig {
	var out []evalConfig
	for _, part := range strings.Split(raw, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, spec, ok := strings.Cut(part, "=")
		nums := strings.Split(spec, ":")
		if !ok || len(nums) != 3 {
			t.Fatalf("NEXO_KB_EVAL_RERANKS: %q (nombre=topN:mezcla:puntaje_minimo)", part)
		}
		topN, err1 := strconv.Atoi(nums[0])
		blend, err2 := strconv.ParseFloat(nums[1], 64)
		minScore, err3 := strconv.ParseFloat(nums[2], 64)
		if err1 != nil || err2 != nil || err3 != nil {
			t.Fatalf("NEXO_KB_EVAL_RERANKS: %q", part)
		}
		out = append(out, evalConfig{name: name, rerank: "on", topN: topN, blend: blend, minScore: minScore})
	}
	return out
}

// runOutcome es una corrida medida, para las tablas comparativas.
type runOutcome struct {
	name       string
	mode       string
	rep        eval.Report
	liveCalls  int
	liveP50    time.Duration
	liveP95    time.Duration
	liveMax    time.Duration
	tokensIn   int64
	tokensOut  int64
	apiCalls   int64
	cacheHits  int
	cacheMiss  int
	spec       evalConfig
	runID      string
	duration   time.Duration
	r3Settings string
}

func pct(v float64) string { return fmt.Sprintf("%.1f %%", 100*v) }

func classHit(rep eval.Report, class string) string {
	m, ok := rep.ByClass[class]
	if !ok {
		return "-"
	}
	if class == eval.ClassNoAnswer {
		if m.NoAnswerAccuracy == nil {
			return "-"
		}
		return pct(*m.NoAnswerAccuracy)
	}
	return pct(m.HitAt5)
}

func classMRR(rep eval.Report, class string) string {
	m, ok := rep.ByClass[class]
	if !ok || class == eval.ClassNoAnswer {
		return "-"
	}
	return fmt.Sprintf("%.3f", m.MRR)
}

// writeRerankMarkdown escribe las tablas comparativas y las diferencias por
// pregunta frente a la primera corrida (la línea base).
func writeRerankMarkdown(path string, runs []runOutcome) error {
	if path == "" || len(runs) == 0 {
		return nil
	}
	var b strings.Builder
	base := runs[0]
	b.WriteString("# R3: medición del reordenamiento (wiki de arquitectura, 50 preguntas borrador)\n\n")
	b.WriteString("## Resumen\n\n")
	b.WriteString("| Configuración | Hit@5 | MRR | nDCG@3 | nDCG@10 | normal | exact | paráfrasis | temporal | cambios | \"no hay\" | latencia p50 | latencia p95 | latencia máx |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, r := range runs {
		lat := func(f func(*eval.RerankSummary) int64) string {
			if r.rep.Rerank == nil || r.rep.Rerank.Used+r.rep.Rerank.Failed+r.rep.Rerank.Partial == 0 {
				return "-"
			}
			return fmt.Sprintf("%d ms", f(r.rep.Rerank))
		}
		t := r.rep.Total
		fmt.Fprintf(&b, "| %s | %s | %.3f | %.3f | %.3f | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n", r.name, pct(t.HitAt5), t.MRR, t.NDCG3, t.NDCG10,
			classHit(r.rep, eval.ClassNormal), classHit(r.rep, eval.ClassExact), classHit(r.rep, eval.ClassParaphrase), classHit(r.rep, eval.ClassTemporal),
			classHit(r.rep, eval.ClassChanges), classHit(r.rep, eval.ClassNoAnswer),
			lat(func(s *eval.RerankSummary) int64 { return s.LatencyP50MS }), lat(func(s *eval.RerankSummary) int64 { return s.LatencyP95MS }),
			lat(func(s *eval.RerankSummary) int64 { return s.LatencyMaxMS }))
	}
	b.WriteString("\nMRR por clase:\n\n| Configuración | normal | exact | paráfrasis | temporal |\n|---|---|---|---|---|\n")
	for _, r := range runs {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", r.name, classMRR(r.rep, eval.ClassNormal), classMRR(r.rep, eval.ClassExact), classMRR(r.rep, eval.ClassParaphrase), classMRR(r.rep, eval.ClassTemporal))
	}
	b.WriteString("\nnDCG@3 por clase:\n\n| Configuración | normal | paráfrasis | temporal |\n|---|---|---|---|\n")
	for _, r := range runs {
		g := func(c string) string {
			if m, ok := r.rep.ByClass[c]; ok {
				return fmt.Sprintf("%.3f", m.NDCG3)
			}
			return "-"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", r.name, g(eval.ClassNormal), g(eval.ClassParaphrase), g(eval.ClassTemporal))
	}
	for _, r := range runs {
		fmt.Fprintf(&b, "\n## %s\n\n", r.name)
		fmt.Fprintf(&b, "Motor `%s`. %s\n\n", r.rep.Retriever, r.rep.Summary())
		if r.spec.rerank == "on" {
			fmt.Fprintf(&b, "Parámetros: topN %d, mezcla %.2f, puntaje mínimo %.2f. %s\n\n", r.spec.topN, r.spec.blend, r.spec.minScore, r.r3Settings)
			if line := r.rep.RerankLine(); line != "" {
				b.WriteString(line + "\n\n")
			}
			fmt.Fprintf(&b, "Proveedor (acumulado hasta esta corrida): %d llamadas, %d tokens de entrada, %d de salida; caché: %d pasajes con puntaje guardado, %d pedidos al proveedor.\n\n",
				r.apiCalls, r.tokensIn, r.tokensOut, r.cacheHits, r.cacheMiss)
			if r.liveCalls > 0 {
				fmt.Fprintf(&b, "Llamadas reales en esta corrida: %d, p50 %d ms, p95 %d ms, máx %d ms.\n\n", r.liveCalls, r.liveP50.Milliseconds(), r.liveP95.Milliseconds(), r.liveMax.Milliseconds())
			} else {
				b.WriteString("Todos los puntajes salieron de la caché: la latencia de esta corrida no es la del proveedor.\n\n")
			}
		}
		b.WriteString(r.rep.Table())
		if r.name == base.name {
			continue
		}
		b.WriteString("\nDiferencias por pregunta frente a `" + base.name + "` (posición del primer acierto; 0 = no apareció):\n\n")
		b.WriteString("| Pregunta | Clase | " + base.name + " | " + r.name + " | Cambio |\n|---|---|---|---|---|\n")
		rows := 0
		for i, q := range r.rep.Results {
			if i >= len(base.rep.Results) {
				break
			}
			bq := base.rep.Results[i]
			if q.Class == eval.ClassNoAnswer {
				if q.Correct != bq.Correct {
					fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", q.ID, q.Class, yesNo(bq.Correct, "no hay", "respondió algo"), yesNo(q.Correct, "no hay", "respondió algo"),
						yesNo(q.Correct, "mejora", "empeora"))
					rows++
				}
				continue
			}
			if q.Rank == bq.Rank {
				continue
			}
			ch := "mejora"
			if q.Rank == 0 || (bq.Rank != 0 && q.Rank > bq.Rank) {
				ch = "empeora"
			}
			fmt.Fprintf(&b, "| %s | %s | %d | %d | %s |\n", q.ID, q.Class, bq.Rank, q.Rank, ch)
			rows++
		}
		if rows == 0 {
			b.WriteString("| (sin diferencias) | | | | |\n")
		}
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

func yesNo(v bool, a, b string) string {
	if v {
		return a
	}
	return b
}
