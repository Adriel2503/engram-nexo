package search

// Nexo IA · NX-10 R3 (RC-13, RC-21, RC-29, design §21) — reordenamiento de los
// candidatos con un modelo externo (paquete rerank).
//
// Dónde actúa: después del filtro común (permisos y tiempo) y del umbral de
// "no hay estándar", antes de la diversidad por documento y del límite. Eso
// da las garantías de RC-21:
//
//   - solo reordena lo que la búsqueda ya autorizó y que pasó el umbral: nunca
//     agrega un resultado ni rescata uno que el umbral descartó, y la
//     decisión "no existe un estándar oficial" no depende del reordenador;
//   - una pregunta sin respuesta (todo bajo el umbral) no hace ninguna
//     llamada al proveedor;
//   - ante cualquier error, tiempo agotado o respuesta parcial, el orden es
//     exactamente el de la búsqueda, con Result.Rerank.Status y un aviso,
//     igual que la caída segura del vector en R2. Nunca es un error para quien
//     busca.
//
// Orden final: Blend * puntaje del reordenador + (1 - Blend) * puntaje de la
// búsqueda (normalizado entre los candidatos); Blend = 1 es el orden puro del
// reordenador, con el orden de la búsqueda como desempate. MinScore (por
// defecto 0 = apagado) descarta los pasajes con puntaje de reordenador menor:
// es una opción medida en design §21, nunca el comportamiento por defecto.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/rerank"
)

// Valores del parámetro de consulta rerank.
const (
	RerankOn  = "on"
	RerankOff = "off"
)

// Estados del reordenamiento en una consulta.
const (
	RerankUsed        = "used"
	RerankSkipped     = "skipped"     // no había nada que reordenar (menos de 2 candidatos) o el modo no lo admite
	RerankUnavailable = "unavailable" // se pidió y el servidor no tiene reordenador
	RerankFailed      = "failed"      // error, tiempo agotado o respuesta no válida: orden de la búsqueda
	RerankPartial     = "partial"     // faltaron puntajes de parte de los candidatos: orden de la búsqueda
)

// MaxRerankTopN es el máximo de candidatos que se reordenan (el límite de la
// búsqueda).
const MaxRerankTopN = MaxLimit

// RerankInfo informa cómo participó el reordenamiento en una consulta.
type RerankInfo struct {
	Status   string `json:"status"`
	Reason   string `json:"reason,omitempty"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	// TopN es el tope configurado y Candidates los que se enviaron al
	// reordenador; Scored, los que volvieron con puntaje.
	TopN       int `json:"top_n,omitempty"`
	Candidates int `json:"candidates,omitempty"`
	Scored     int `json:"scored,omitempty"`
	// Reordered: candidatos que cambiaron de posición; TopChanged: el primero
	// ya no es el que habría respondido la búsqueda.
	Reordered  int  `json:"reordered,omitempty"`
	TopChanged bool `json:"top_changed,omitempty"`
	// Dropped: descartados por MinScore.
	Dropped int     `json:"dropped,omitempty"`
	Blend   float64 `json:"blend,omitempty"`
	// LatencyMS es el tiempo agregado a la consulta (lectura de pasajes y
	// llamada al proveedor).
	LatencyMS int64 `json:"latency_ms"`
}

// RerankOptions son los parámetros del reordenamiento.
type RerankOptions struct {
	// TopN: cuántos candidatos (los primeros de la búsqueda tras el umbral) se
	// envían al reordenador (1 a MaxRerankTopN).
	TopN int `json:"top_n"`
	// Budget es el tiempo total que la consulta espera al reordenador (lectura
	// de pasajes, llamadas y reintentos); pasado ese tiempo se responde con el
	// orden de la búsqueda.
	Budget time.Duration `json:"budget"`
	// Blend en [0, 1]: peso del puntaje del reordenador (1 = orden puro).
	Blend float64 `json:"blend"`
	// MinScore en [0, 1]: descarta los pasajes cuyo puntaje de reordenador sea
	// menor. 0 = apagado (por defecto).
	MinScore float64 `json:"min_score"`
	// Default: reordenar cuando la consulta no lo pide.
	Default bool `json:"default"`
}

// DefaultRerankOptions: 50 candidatos, 10 s, orden puro y sin descarte. El
// valor de Default sale de la medición (design §21.6).
var DefaultRerankOptions = RerankOptions{TopN: 50, Budget: 10 * time.Second, Blend: 1}

// PassageLoader devuelve el pasaje (título, ruta de títulos y cuerpo) de cada
// sección. Solo se le pasan secciones que ya pasaron el filtro común.
type PassageLoader func(ctx context.Context, sectionIDs []string) (map[string]string, error)

// SetReranker configura el reordenamiento (nil lo desactiva).
func (s *Searcher) SetReranker(rr rerank.Reranker, load PassageLoader, o RerankOptions) {
	s.rr, s.loadPassages, s.ropts = rr, load, o
}

// rerankTopN es el tope efectivo de candidatos a reordenar.
func (s *Searcher) rerankTopN() int {
	n := s.ropts.TopN
	if n <= 0 {
		n = DefaultRerankOptions.TopN
	}
	return min(n, MaxRerankTopN)
}

// Reranker devuelve el reordenador configurado (nil si no hay).
func (s *Searcher) Reranker() rerank.Reranker { return s.rr }

// RerankOptions devuelve los parámetros del reordenamiento.
func (s *Searcher) RerankOptions() RerankOptions { return s.ropts }

// NormalizeRerank interpreta el parámetro rerank: vacío o auto (decide el
// servidor), on o off. ok es falso si el valor no es válido.
func NormalizeRerank(v string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "auto", "default":
		return "", true
	case "on", "1", "true", "yes", "si", "sí":
		return RerankOn, true
	case "off", "0", "false", "no":
		return RerankOff, true
	}
	return "", false
}

// rerankHits reordena los candidatos (los que pasaron filtro y umbral, en el
// orden de la búsqueda) y devuelve la nueva lista y el informe. Nunca falla:
// ante cualquier problema devuelve cands tal cual.
func (s *Searcher) rerankHits(ctx context.Context, q Query, cands []Hit) ([]Hit, *RerankInfo) {
	o := s.ropts
	topN := s.rerankTopN()
	blend := math.Min(math.Max(o.Blend, 0), 1)
	info := &RerankInfo{Status: RerankSkipped, Provider: s.rr.Name(), Model: s.rr.Model(), TopN: topN, Blend: blend, Candidates: min(len(cands), topN)}
	if len(cands) < 2 {
		info.Reason = "hay menos de dos resultados para reordenar"
		return cands, info
	}
	start := time.Now()
	defer func() { info.LatencyMS = time.Since(start).Milliseconds() }()
	budget := o.Budget
	if budget <= 0 {
		budget = DefaultRerankOptions.Budget
	}
	rctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	head := cands[:info.Candidates]
	passages, err := s.passagesFor(rctx, head)
	if err != nil {
		log.Printf("[nexo] kb: reordenamiento omitido, no se pudieron leer los pasajes: %v", err)
		info.Status, info.Reason = RerankFailed, "no se pudieron leer los pasajes"
		return cands, info
	}
	scores, err := safeRerank(rctx, s.rr, q.Text, passages)
	for _, sc := range scores {
		if sc.OK {
			info.Scored++
		}
	}
	if err == nil && (len(scores) != len(head) || info.Scored != len(head)) {
		err = errors.New("el reordenador no devolvió un puntaje por pasaje")
	}
	if err != nil {
		log.Printf("[nexo] kb: reordenamiento fallido (%s %s), se conserva el orden de la búsqueda: %v", s.rr.Name(), s.rr.Model(), err)
		info.Status, info.Reason = RerankFailed, rerankReason(rctx, err)
		if info.Scored > 0 {
			info.Status = RerankPartial
			info.Reason = fmt.Sprintf("el reordenador devolvió puntaje para %d de %d resultados", info.Scored, len(head))
		}
		return cands, info
	}
	// Puntaje de la búsqueda normalizado entre los candidatos (min-max): el
	// RRF por sí solo varía muy poco entre posiciones vecinas.
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, h := range head {
		lo, hi = math.Min(lo, h.Rank), math.Max(hi, h.Rank)
	}
	final := make([]float64, len(head))
	for i, h := range head {
		fused := 1.0
		if hi > lo {
			fused = (h.Rank - lo) / (hi - lo)
		}
		final[i] = blend*scores[i].Value + (1-blend)*fused
	}
	order := make([]int, len(head))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return final[order[a]] > final[order[b]] })
	out := make([]Hit, 0, len(cands))
	for pos, i := range order {
		h := head[i]
		v := scores[i].Value
		h.RerankScore, h.PreRerankPosition = &v, i+1
		if o.MinScore > 0 && v < o.MinScore {
			info.Dropped++
			continue
		}
		if pos != i {
			info.Reordered++
		}
		out = append(out, h)
	}
	info.TopChanged = order[0] != 0
	info.Status = RerankUsed
	return append(out, cands[len(head):]...), info
}

// passagesFor arma el pasaje de cada candidato: el texto que lee el cargador
// y, si falta, título, ruta y extracto del resultado.
func (s *Searcher) passagesFor(ctx context.Context, head []Hit) ([]rerank.Passage, error) {
	var texts map[string]string
	if s.loadPassages != nil {
		ids := make([]string, len(head))
		for i, h := range head {
			ids[i] = h.SectionID
		}
		var err error
		if texts, err = s.loadPassages(ctx, ids); err != nil {
			return nil, err
		}
	}
	out := make([]rerank.Passage, len(head))
	for i, h := range head {
		t := strings.TrimSpace(texts[h.SectionID])
		if t == "" {
			t = h.Title
			if len(h.HeadingPath) > 0 {
				t += " > " + strings.Join(h.HeadingPath, " > ")
			}
			t += "\n\n" + h.Excerpt
		}
		out[i] = rerank.Passage{ID: h.SectionID, Text: t}
	}
	return out, nil
}

// safeRerank llama al reordenador sin dejar que un pánico llegue a la
// búsqueda.
func safeRerank(ctx context.Context, rr rerank.Reranker, query string, passages []rerank.Passage) (scores []rerank.Score, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			scores, err = nil, fmt.Errorf("pánico en el reordenador: %v", rec)
		}
	}()
	return rr.Rerank(ctx, query, passages)
}

// rerankReason es el motivo, sin detalles del proveedor, que ve quien busca.
func rerankReason(ctx context.Context, err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
		return "se agotó el tiempo del reordenamiento"
	case errors.Is(err, context.Canceled):
		return "la consulta se canceló"
	case errors.Is(err, rerank.ErrMalformed):
		return "el reordenador devolvió una respuesta no válida"
	case rerank.IsTransient(err):
		return "el proveedor de reordenamiento no respondió"
	}
	return "el reordenamiento falló"
}

func joinNotice(a, b string) string {
	if a == "" {
		return b
	}
	return a + " " + b
}
