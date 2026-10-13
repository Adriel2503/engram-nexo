package eval

// R3 (reordenamiento): qué hizo el reordenador en cada pregunta de una
// corrida y su resumen (estados, latencia agregada, preguntas que mejoraron o
// empeoraron). Se guarda con la corrida en nexo_kb_eval_runs.results.

import (
	"fmt"
	"sort"
)

// RerankTrace es lo que hizo el reordenador en una pregunta.
type RerankTrace struct {
	// Status: used, skipped, unavailable, failed o partial (search.Rerank*).
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	// LatencyMS es el tiempo agregado a la consulta (pasajes y proveedor).
	LatencyMS  int64 `json:"latency_ms"`
	Candidates int   `json:"candidates,omitempty"`
	Reordered  int   `json:"reordered,omitempty"`
	TopChanged bool  `json:"top_changed,omitempty"`
	Dropped    int   `json:"dropped,omitempty"`
	// RankBefore es la posición del primer acierto con el orden de la búsqueda
	// (0 = no apareció); la posición con el reordenamiento es QuestionResult.Rank.
	RankBefore int `json:"rank_before"`
}

// RerankSummary resume el reordenamiento de una corrida.
type RerankSummary struct {
	// Questions: preguntas por las que pasó el reordenador (búsquedas).
	Questions   int `json:"questions"`
	Used        int `json:"used"`
	Skipped     int `json:"skipped"`
	Unavailable int `json:"unavailable"`
	Failed      int `json:"failed"`
	Partial     int `json:"partial"`
	// Latencia agregada por consulta, en milisegundos, sobre las consultas que
	// llamaron al proveedor (used, failed, partial).
	LatencyP50MS  int64 `json:"latency_p50_ms"`
	LatencyP95MS  int64 `json:"latency_p95_ms"`
	LatencyMaxMS  int64 `json:"latency_max_ms"`
	LatencyMeanMS int64 `json:"latency_mean_ms"`
	// Preguntas con respuesta cuyo primer acierto subió, bajó o quedó igual.
	Improved  int `json:"improved"`
	Worsened  int `json:"worsened"`
	Unchanged int `json:"unchanged"`
	// TopChanged: consultas donde cambió el primer resultado.
	TopChanged int `json:"top_changed"`
}

// traceWithRanks completa el rastro con la posición del primer acierto antes
// de reordenar (la posición previa del primer resultado relevante; si el
// reordenador no tocó la lista, la misma).
func traceWithRanks(t *RerankTrace, hits []Hit, rel []bool, rank int) *RerankTrace {
	if t == nil {
		return nil
	}
	cp := *t
	before := 0
	for i, h := range hits {
		if i < len(rel) && rel[i] && h.PrePos > 0 && (before == 0 || h.PrePos < before) {
			before = h.PrePos
		}
	}
	if before == 0 {
		before = rank
	}
	cp.RankBefore = before
	return &cp
}

func summarizeRerank(rs []QuestionResult) *RerankSummary {
	var s RerankSummary
	var lat []int64
	var sum int64
	for _, r := range rs {
		t := r.Rerank
		if t == nil {
			continue
		}
		s.Questions++
		switch t.Status {
		case "used":
			s.Used++
		case "skipped":
			s.Skipped++
		case "unavailable":
			s.Unavailable++
		case "failed":
			s.Failed++
		case "partial":
			s.Partial++
		}
		switch t.Status {
		case "used", "failed", "partial":
			lat = append(lat, t.LatencyMS)
			sum += t.LatencyMS
		}
		if t.TopChanged {
			s.TopChanged++
		}
		if t.Status != "used" || r.Class == ClassNoAnswer || r.Class == ClassChanges {
			continue
		}
		switch {
		case r.Rank > 0 && (t.RankBefore == 0 || r.Rank < t.RankBefore):
			s.Improved++
		case t.RankBefore > 0 && (r.Rank == 0 || r.Rank > t.RankBefore):
			s.Worsened++
		default:
			s.Unchanged++
		}
	}
	if s.Questions == 0 {
		return nil
	}
	if n := len(lat); n > 0 {
		sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
		s.LatencyP50MS = percentile(lat, 0.50)
		s.LatencyP95MS = percentile(lat, 0.95)
		s.LatencyMaxMS = lat[n-1]
		s.LatencyMeanMS = sum / int64(n)
	}
	return &s
}

// percentile es el percentil p (0 a 1) por el método del rango más cercano.
func percentile(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p*float64(len(sorted))+0.999999) - 1
	idx = min(max(idx, 0), len(sorted)-1)
	return sorted[idx]
}

// RerankLine es una línea legible del reordenamiento de la corrida ("" si no
// participó).
func (r Report) RerankLine() string {
	s := r.Rerank
	if s == nil {
		return ""
	}
	return fmt.Sprintf("reordenamiento: %d consultas (usado %d, omitido %d, no disponible %d, fallido %d, parcial %d); mejoraron %d, empeoraron %d, igual %d; primer resultado cambió en %d; latencia agregada p50 %d ms, p95 %d ms, máx %d ms, media %d ms",
		s.Questions, s.Used, s.Skipped, s.Unavailable, s.Failed, s.Partial, s.Improved, s.Worsened, s.Unchanged, s.TopChanged, s.LatencyP50MS, s.LatencyP95MS, s.LatencyMaxMS, s.LatencyMeanMS)
}
