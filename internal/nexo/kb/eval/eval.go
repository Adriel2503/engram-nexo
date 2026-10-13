// Package eval mide la calidad de la búsqueda de conocimiento (NX-10, design
// §14.5): un conjunto de preguntas reales con las secciones esperadas
// (eval/preguntas.yaml, versionado junto a los lineamientos) y dos métricas:
// Recall@5 (acierto en los 5 primeros) y MRR (posición media del primer
// acierto, como 1/posición). El mismo conjunto se corre contra cualquier
// motor que implemente Retriever: el servidor (pgfts) hoy; la copia local con
// BM25 (SQLite FTS5) y el híbrido llegan después sin cambiar este paquete.
package eval

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Set es el archivo eval/preguntas.yaml.
//
//	version: 1
//	spaces: [lineamientos-frontend]     # por defecto para todas las preguntas
//	questions:
//	  - id: formularios-validacion
//	    question: ¿Dónde se validan los formularios?
//	    expected:
//	      - doc: frontend/formularios
//	        anchor: formularios-en-aplicaciones-web.validacion   # opcional
//	        space: lineamientos-frontend                         # opcional
//	    at: 2026-11-01                     # opcional (vigencia)
//	    tags: [paráfrasis]                 # opcional (para agrupar resultados)
type Set struct {
	Version   int        `yaml:"version" json:"version"`
	Spaces    []string   `yaml:"spaces" json:"spaces,omitempty"`
	Questions []Question `yaml:"questions" json:"questions"`
}

// Question es una pregunta con sus secciones esperadas.
type Question struct {
	ID       string     `yaml:"id" json:"id"`
	Question string     `yaml:"question" json:"question"`
	Spaces   []string   `yaml:"spaces" json:"spaces,omitempty"`
	Expected []Expected `yaml:"expected" json:"expected"`
	At       string     `yaml:"at" json:"at,omitempty"`
	Tags     []string   `yaml:"tags" json:"tags,omitempty"`
}

// Expected es una sección (o un documento, sin ancla) que debe aparecer.
type Expected struct {
	Space  string `yaml:"space" json:"space,omitempty"`
	Doc    string `yaml:"doc" json:"doc"`
	Anchor string `yaml:"anchor" json:"anchor,omitempty"`
}

// Hit es un resultado de un motor, reducido a lo que se compara.
type Hit struct {
	Space  string `json:"space"`
	DocKey string `json:"doc_key"`
	Anchor string `json:"anchor"`
}

// Retriever es un motor a evaluar.
type Retriever interface {
	Name() string
	Search(ctx context.Context, q Question, spaces []string, at *time.Time, limit int) ([]Hit, error)
}

// Load lee y valida un archivo de preguntas.
func Load(path string) (Set, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Set{}, err
	}
	return Parse(raw)
}

// Parse valida el contenido de un archivo de preguntas.
func Parse(raw []byte) (Set, error) {
	var s Set
	if err := yaml.Unmarshal(raw, &s); err != nil {
		return Set{}, fmt.Errorf("eval: YAML inválido: %w", err)
	}
	if s.Version == 0 {
		s.Version = 1
	}
	if s.Version != 1 {
		return Set{}, fmt.Errorf("eval: versión %d no soportada (use version: 1)", s.Version)
	}
	if len(s.Questions) == 0 {
		return Set{}, fmt.Errorf("eval: el archivo no tiene preguntas")
	}
	seen := map[string]bool{}
	for i, q := range s.Questions {
		if strings.TrimSpace(q.Question) == "" {
			return Set{}, fmt.Errorf("eval: la pregunta %d no tiene texto", i+1)
		}
		if q.ID == "" {
			s.Questions[i].ID = fmt.Sprintf("q%d", i+1)
		}
		if seen[s.Questions[i].ID] {
			return Set{}, fmt.Errorf("eval: id repetido %q", s.Questions[i].ID)
		}
		seen[s.Questions[i].ID] = true
		if len(q.Expected) == 0 {
			return Set{}, fmt.Errorf("eval: la pregunta %q no tiene secciones esperadas", s.Questions[i].ID)
		}
		for _, e := range q.Expected {
			if strings.TrimSpace(e.Doc) == "" {
				return Set{}, fmt.Errorf("eval: la pregunta %q tiene un esperado sin doc", s.Questions[i].ID)
			}
		}
		if q.At != "" {
			if _, err := parseAt(q.At); err != nil {
				return Set{}, fmt.Errorf("eval: la pregunta %q: %w", s.Questions[i].ID, err)
			}
		}
	}
	return s, nil
}

func parseAt(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return &t, nil
		}
	}
	return nil, fmt.Errorf("at %q no es una fecha (YYYY-MM-DD o RFC 3339)", s)
}

// Matches informa si un resultado cumple lo esperado. Sin ancla basta el
// documento; con ancla vale la sección o una subsección o parte suya.
func (e Expected) Matches(h Hit) bool {
	if e.Space != "" && e.Space != h.Space {
		return false
	}
	if e.Doc != h.DocKey {
		return false
	}
	if e.Anchor == "" || e.Anchor == h.Anchor {
		return true
	}
	return strings.HasPrefix(h.Anchor, e.Anchor+".") || strings.HasPrefix(h.Anchor, e.Anchor+"-p")
}

// QuestionResult es el resultado de una pregunta.
type QuestionResult struct {
	ID       string `json:"id"`
	Question string `json:"question"`
	Rank     int    `json:"rank"` // posición del primer acierto (0 = no apareció)
	Top      []Hit  `json:"top"`
	Error    string `json:"error,omitempty"`
}

// Report es el resultado de una evaluación.
type Report struct {
	Retriever  string           `json:"retriever"`
	K          int              `json:"k"`
	Depth      int              `json:"depth"`
	Questions  int              `json:"questions"`
	RecallAtK  float64          `json:"recall_at_k"`
	MRR        float64          `json:"mrr"`
	Hits       int              `json:"hits_at_k"`
	Errors     int              `json:"errors"`
	Results    []QuestionResult `json:"results"`
	StartedAt  time.Time        `json:"started_at"`
	DurationMS int64            `json:"duration_ms"`
}

// Run evalúa el conjunto: Recall@k (por defecto 5) y MRR sobre los depth
// primeros resultados (por defecto 10).
func Run(ctx context.Context, set Set, r Retriever, k, depth int) Report {
	if k <= 0 {
		k = 5
	}
	if depth < k {
		depth = 10
		if depth < k {
			depth = k
		}
	}
	start := time.Now()
	rep := Report{Retriever: r.Name(), K: k, Depth: depth, Questions: len(set.Questions), StartedAt: start.UTC()}
	var rr float64
	for _, q := range set.Questions {
		spaces := q.Spaces
		if len(spaces) == 0 {
			spaces = set.Spaces
		}
		at, _ := parseAt(q.At)
		res := QuestionResult{ID: q.ID, Question: q.Question}
		hits, err := r.Search(ctx, q, spaces, at, depth)
		if err != nil {
			res.Error = err.Error()
			rep.Errors++
			rep.Results = append(rep.Results, res)
			continue
		}
		if len(hits) > depth {
			hits = hits[:depth]
		}
		res.Top = hits
		for i, h := range hits {
			if matchesAny(q.Expected, h) {
				res.Rank = i + 1
				break
			}
		}
		if res.Rank > 0 {
			rr += 1 / float64(res.Rank)
			if res.Rank <= k {
				rep.Hits++
			}
		}
		rep.Results = append(rep.Results, res)
	}
	if rep.Questions > 0 {
		rep.RecallAtK = float64(rep.Hits) / float64(rep.Questions)
		rep.MRR = rr / float64(rep.Questions)
	}
	rep.DurationMS = time.Since(start).Milliseconds()
	return rep
}

func matchesAny(exp []Expected, h Hit) bool {
	for _, e := range exp {
		if e.Matches(h) {
			return true
		}
	}
	return false
}

// Summary es una línea legible del informe.
func (r Report) Summary() string {
	return fmt.Sprintf("motor %s: %d preguntas, Recall@%d = %.1f %% (%d), MRR = %.3f, errores %d, %d ms",
		r.Retriever, r.Questions, r.K, 100*r.RecallAtK, r.Hits, r.MRR, r.Errors, r.DurationMS)
}
