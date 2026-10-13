// Package eval mide la calidad de la búsqueda de conocimiento (NX-10, design
// §14.5 y §17.5, R0): un conjunto de preguntas con las secciones esperadas,
// comparadas por ancla de sección (doc_key + ancla, y versión en las
// temporales), nunca por coincidencia de texto. El mismo conjunto se corre
// contra cualquier motor que implemente Retriever (el servidor hoy; la copia
// local y el híbrido después).
//
// Clases de pregunta (las métricas se informan por clase):
//
//	normal      con respuesta
//	exact       término exacto (componentes, códigos, siglas)
//	paraphrase  paráfrasis
//	no_answer   sin respuesta: debe dar no_results
//	temporal    at pasado, known_at, reemplazado, programado, retirado (acierto de versión)
//	changes     "qué cambió desde X": secciones agregadas, modificadas o retiradas
//
// Métricas: Hit@5 (el "Recall@5" de 2a: algún esperado entre los 5
// primeros), Recall@5 (fracción de esperados), MRR, nDCG@3, nDCG@10, MAP y
// acierto de "no hay respuesta"; en temporales, el acierto exige la versión.
package eval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Clases de pregunta.
const (
	ClassNormal     = "normal"
	ClassExact      = "exact"
	ClassNoAnswer   = "no_answer"
	ClassTemporal   = "temporal"
	ClassChanges    = "changes"
	ClassParaphrase = "paraphrase"
)

// Classes en orden de informe.
var Classes = []string{ClassNormal, ClassExact, ClassParaphrase, ClassNoAnswer, ClassTemporal, ClassChanges}

// Set es el archivo de preguntas.
//
//	version: 2
//	name: wiki-arquitectura
//	status: draft                       # draft | validated (el conjunto)
//	spaces: [arquitectura]              # por defecto para todas las preguntas
//	questions:
//	  - id: logging-niveles
//	    class: normal                   # ver Classes (por defecto normal)
//	    status: draft                   # draft | validated: un borrador nunca es la verdad de referencia
//	    question: ¿Qué niveles de log usamos?
//	    expected:                       # vacío en no_answer
//	      - doc: lineamientos/desarrollo/logging
//	        anchor: logging.niveles     # opcional (sin ancla basta el documento)
//	        version: 2                  # opcional (temporales)
//	        change: modified            # solo changes: added | modified | removed
//	    at: 2022-06-01                  # opcional (vigencia)
//	    known_at: 2026-10-01            # opcional (registro)
//	    since: 2026-10-01               # changes
//	    until: 2026-10-13               # changes (opcional)
//	    mode: exact                     # opcional: modo de búsqueda pedido
type Set struct {
	Version   int        `yaml:"version" json:"version"`
	Name      string     `yaml:"name" json:"name,omitempty"`
	Status    string     `yaml:"status" json:"status,omitempty"`
	Spaces    []string   `yaml:"spaces" json:"spaces,omitempty"`
	Questions []Question `yaml:"questions" json:"questions"`
	// SHA256 del archivo (identifica el conjunto en nexo_kb_eval_runs).
	SHA256 string `yaml:"-" json:"sha256,omitempty"`
}

// Question es una pregunta con sus secciones esperadas.
type Question struct {
	ID       string     `yaml:"id" json:"id"`
	Class    string     `yaml:"class" json:"class,omitempty"`
	Status   string     `yaml:"status" json:"status,omitempty"`
	Question string     `yaml:"question" json:"question"`
	Spaces   []string   `yaml:"spaces" json:"spaces,omitempty"`
	Expected []Expected `yaml:"expected" json:"expected"`
	At       string     `yaml:"at" json:"at,omitempty"`
	KnownAt  string     `yaml:"known_at" json:"known_at,omitempty"`
	Since    string     `yaml:"since" json:"since,omitempty"`
	Until    string     `yaml:"until" json:"until,omitempty"`
	Mode     string     `yaml:"mode" json:"mode,omitempty"`
	Tags     []string   `yaml:"tags" json:"tags,omitempty"`
	Note     string     `yaml:"note" json:"note,omitempty"`
}

// Expected es una sección (o un documento, sin ancla) que debe aparecer.
type Expected struct {
	Space   string `yaml:"space" json:"space,omitempty"`
	Doc     string `yaml:"doc" json:"doc"`
	Anchor  string `yaml:"anchor" json:"anchor,omitempty"`
	Version int    `yaml:"version" json:"version,omitempty"`
	Change  string `yaml:"change" json:"change,omitempty"`
}

// Hit es un resultado de un motor, reducido a lo que se compara.
type Hit struct {
	Space   string `json:"space"`
	DocKey  string `json:"doc_key"`
	Anchor  string `json:"anchor"`
	Version int    `json:"version,omitempty"`
	Change  string `json:"change,omitempty"`
}

// Outcome es la respuesta de un motor a una pregunta.
type Outcome struct {
	Hits      []Hit
	NoResults bool
}

// Retriever es un motor a evaluar.
type Retriever interface {
	Name() string
	Search(ctx context.Context, q Question, spaces []string, at, knownAt *time.Time, limit int) (Outcome, error)
}

// ChangeLister lista cambios entre dos fechas (clase changes, RC-27).
type ChangeLister interface {
	Changes(ctx context.Context, q Question, spaces []string, since, until *time.Time) ([]Hit, error)
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
	if s.Version != 1 && s.Version != 2 {
		return Set{}, fmt.Errorf("eval: versión %d no soportada (use version: 2)", s.Version)
	}
	if len(s.Questions) == 0 {
		return Set{}, fmt.Errorf("eval: el archivo no tiene preguntas")
	}
	sum := sha256.Sum256(raw)
	s.SHA256 = hex.EncodeToString(sum[:])
	seen := map[string]bool{}
	for i := range s.Questions {
		q := &s.Questions[i]
		if strings.TrimSpace(q.Question) == "" {
			return Set{}, fmt.Errorf("eval: la pregunta %d no tiene texto", i+1)
		}
		if q.ID == "" {
			q.ID = fmt.Sprintf("q%d", i+1)
		}
		if seen[q.ID] {
			return Set{}, fmt.Errorf("eval: id repetido %q", q.ID)
		}
		seen[q.ID] = true
		if q.Class == "" {
			q.Class = ClassNormal
			for _, t := range q.Tags {
				if t == "parafrasis" || t == "paráfrasis" || t == ClassParaphrase {
					q.Class = ClassParaphrase
				}
			}
		}
		if !validClass(q.Class) {
			return Set{}, fmt.Errorf("eval: la pregunta %q tiene clase desconocida %q", q.ID, q.Class)
		}
		if q.Class != ClassNoAnswer && len(q.Expected) == 0 {
			return Set{}, fmt.Errorf("eval: la pregunta %q no tiene secciones esperadas", q.ID)
		}
		if q.Class == ClassNoAnswer && len(q.Expected) > 0 {
			return Set{}, fmt.Errorf("eval: la pregunta %q es no_answer y tiene esperados", q.ID)
		}
		for _, e := range q.Expected {
			if strings.TrimSpace(e.Doc) == "" {
				return Set{}, fmt.Errorf("eval: la pregunta %q tiene un esperado sin doc", q.ID)
			}
		}
		for name, v := range map[string]string{"at": q.At, "known_at": q.KnownAt, "since": q.Since, "until": q.Until} {
			if _, err := ParseTime(v); err != nil {
				return Set{}, fmt.Errorf("eval: la pregunta %q: %s: %w", q.ID, name, err)
			}
		}
		if q.Class == ClassChanges && q.Since == "" {
			return Set{}, fmt.Errorf("eval: la pregunta %q es changes y no tiene since", q.ID)
		}
	}
	return s, nil
}

func validClass(c string) bool {
	for _, x := range Classes {
		if x == c {
			return true
		}
	}
	return false
}

// Marcas de tiempo relativas a la carga del corpus: "@base" es el momento
// entre la carga base y la de las versiones nuevas (overlay). Quien corre la
// evaluación las reemplaza con Bind; sin reemplazo valen nil.
const MarkBase = "@base"

// Bind reemplaza las marcas de tiempo (@base) de las preguntas.
func (s *Set) Bind(marks map[string]time.Time) {
	for i := range s.Questions {
		q := &s.Questions[i]
		for _, f := range []*string{&q.At, &q.KnownAt, &q.Since, &q.Until} {
			if t, ok := marks[strings.TrimSpace(*f)]; ok {
				*f = t.UTC().Format(time.RFC3339Nano)
			}
		}
	}
}

// ParseTime acepta YYYY-MM-DD o RFC 3339 ("" = nil; una marca "@..." sin
// reemplazar = nil).
func ParseTime(s string) (*time.Time, error) {
	if strings.TrimSpace(s) == "" || strings.HasPrefix(strings.TrimSpace(s), "@") {
		return nil, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(s)); err == nil {
		u := t.UTC()
		return &u, nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, strings.TrimSpace(s)); err == nil {
			u := t.UTC()
			return &u, nil
		}
	}
	return nil, fmt.Errorf("%q no es una fecha (YYYY-MM-DD o RFC 3339)", s)
}

// Matches informa si un resultado cumple lo esperado. Sin ancla basta el
// documento; con ancla vale la sección o una subsección o parte suya (-pN
// por tamaño, -rN por filas de tabla). Con versión, la versión debe coincidir;
// con change, la clase del cambio.
func (e Expected) Matches(h Hit) bool {
	if e.Space != "" && e.Space != h.Space {
		return false
	}
	if e.Doc != h.DocKey {
		return false
	}
	if e.Version > 0 && e.Version != h.Version {
		return false
	}
	if e.Change != "" && e.Change != h.Change {
		return false
	}
	if e.Anchor == "" || e.Anchor == h.Anchor {
		return true
	}
	return strings.HasPrefix(h.Anchor, e.Anchor+".") || strings.HasPrefix(h.Anchor, e.Anchor+"-p") || strings.HasPrefix(h.Anchor, e.Anchor+"-r")
}

// relevance marca cada resultado que acierta un esperado todavía no
// acertado (cada esperado cuenta una vez).
func relevance(exp []Expected, hits []Hit) []bool {
	used := make([]bool, len(exp))
	rel := make([]bool, len(hits))
	for i, h := range hits {
		for j, e := range exp {
			if !used[j] && e.Matches(h) {
				used[j], rel[i] = true, true
				break
			}
		}
	}
	return rel
}

// QuestionResult es el resultado de una pregunta.
type QuestionResult struct {
	ID        string  `json:"id"`
	Class     string  `json:"class"`
	Status    string  `json:"status,omitempty"`
	Question  string  `json:"question"`
	Rank      int     `json:"rank"` // posición del primer acierto (0 = no apareció)
	NoResults bool    `json:"no_results"`
	Correct   bool    `json:"correct"` // acierto según la clase (Hit@k o no_results)
	Hit5      float64 `json:"hit_at_5"`
	Recall5   float64 `json:"recall_at_5"`
	RR        float64 `json:"rr"`
	NDCG3     float64 `json:"ndcg_at_3"`
	NDCG10    float64 `json:"ndcg_at_10"`
	AP        float64 `json:"ap"`
	Top       []Hit   `json:"top"`
	Error     string  `json:"error,omitempty"`
}

// Metrics son métricas agregadas.
type Metrics struct {
	Questions int     `json:"questions"`
	HitAt5    float64 `json:"hit_at_5"`    // Recall@5 de §14.5 (acierto en los 5 primeros)
	RecallAt5 float64 `json:"recall_at_5"` // fracción de esperados entre los 5 primeros
	MRR       float64 `json:"mrr"`
	NDCG3     float64 `json:"ndcg_at_3"`
	NDCG10    float64 `json:"ndcg_at_10"`
	MAP       float64 `json:"map"`
	// NoAnswerAccuracy: preguntas sin respuesta que dieron no_results.
	NoAnswerAccuracy *float64 `json:"no_answer_accuracy,omitempty"`
	// FalseNoAnswer: preguntas con respuesta que dieron no_results.
	FalseNoAnswer int `json:"false_no_answer"`
	Correct       int `json:"correct"`
	Errors        int `json:"errors"`
}

// Report es el resultado de una evaluación.
type Report struct {
	Retriever  string             `json:"retriever"`
	K          int                `json:"k"`
	Depth      int                `json:"depth"`
	Set        string             `json:"set,omitempty"`
	SetSHA256  string             `json:"set_sha256,omitempty"`
	Validated  bool               `json:"validated_only"`
	Questions  int                `json:"questions"`
	RecallAtK  float64            `json:"recall_at_k"` // compatibilidad 2a: Total.HitAt5
	MRR        float64            `json:"mrr"`
	Hits       int                `json:"hits_at_k"`
	Errors     int                `json:"errors"`
	Total      Metrics            `json:"total"`
	ByClass    map[string]Metrics `json:"by_class"`
	Results    []QuestionResult   `json:"results"`
	StartedAt  time.Time          `json:"started_at"`
	DurationMS int64              `json:"duration_ms"`
}

// Options de una corrida.
type Options struct {
	K             int  // por defecto 5
	Depth         int  // por defecto 10
	ValidatedOnly bool // solo preguntas validated
}

// Run evalúa el conjunto (compatibilidad 2a).
func Run(ctx context.Context, set Set, r Retriever, k, depth int) Report {
	return RunWith(ctx, set, r, Options{K: k, Depth: depth})
}

// RunWith evalúa el conjunto.
func RunWith(ctx context.Context, set Set, r Retriever, o Options) Report {
	k, depth := o.K, o.Depth
	if k <= 0 {
		k = 5
	}
	if depth < 10 {
		depth = 10
	}
	if depth < k {
		depth = k
	}
	start := time.Now()
	rep := Report{Retriever: r.Name(), K: k, Depth: depth, Set: set.Name, SetSHA256: set.SHA256, Validated: o.ValidatedOnly, StartedAt: start.UTC()}
	for _, q := range set.Questions {
		if o.ValidatedOnly && q.Status != "validated" {
			continue
		}
		spaces := q.Spaces
		if len(spaces) == 0 {
			spaces = set.Spaces
		}
		res := QuestionResult{ID: q.ID, Class: q.Class, Status: q.Status, Question: q.Question}
		var hits []Hit
		var err error
		if q.Class == ClassChanges {
			if cl, ok := r.(ChangeLister); ok {
				since, _ := ParseTime(q.Since)
				until, _ := ParseTime(q.Until)
				hits, err = cl.Changes(ctx, q, spaces, since, until)
			} else {
				err = fmt.Errorf("el motor %s no lista cambios", r.Name())
			}
		} else {
			at, _ := ParseTime(q.At)
			known, _ := ParseTime(q.KnownAt)
			var out Outcome
			out, err = r.Search(ctx, q, spaces, at, known, depth)
			hits, res.NoResults = out.Hits, out.NoResults
			if len(hits) > depth {
				hits = hits[:depth]
			}
		}
		if err != nil {
			res.Error = err.Error()
		}
		res.Top = hits
		if len(res.Top) > 10 {
			res.Top = res.Top[:10]
		}
		switch q.Class {
		case ClassNoAnswer:
			res.Correct = err == nil && (res.NoResults || len(hits) == 0)
		case ClassChanges:
			// Lista sin orden: se mide la cobertura de los esperados.
			rel := relevance(q.Expected, hits)
			cov := RecallAtK(rel, len(rel), len(q.Expected))
			res.Hit5, res.Recall5, res.RR, res.AP, res.NDCG3, res.NDCG10 = cov, cov, cov, cov, cov, cov
			res.Correct = cov == 1
			if cov > 0 {
				res.Rank = 1
			}
		default:
			rel := relevance(q.Expected, hits)
			n := len(q.Expected)
			for i, x := range rel {
				if x {
					res.Rank = i + 1
					break
				}
			}
			res.Hit5 = HitAtK(rel, k)
			res.Recall5 = RecallAtK(rel, k, n)
			res.RR = ReciprocalRank(rel)
			res.NDCG3 = NDCGAtK(rel, 3, n)
			res.NDCG10 = NDCGAtK(rel, 10, n)
			res.AP = AveragePrecision(rel, n)
			res.Correct = res.Hit5 == 1
		}
		rep.Results = append(rep.Results, res)
	}
	rep.Questions = len(rep.Results)
	rep.ByClass = map[string]Metrics{}
	byClass := map[string][]QuestionResult{}
	for _, res := range rep.Results {
		byClass[res.Class] = append(byClass[res.Class], res)
	}
	for c, rs := range byClass {
		rep.ByClass[c] = aggregate(rs)
	}
	rep.Total = aggregate(rep.Results)
	rep.RecallAtK, rep.MRR, rep.Errors = rep.Total.HitAt5, rep.Total.MRR, rep.Total.Errors
	for _, res := range rep.Results {
		if res.Class != ClassNoAnswer && res.Class != ClassChanges && res.Rank > 0 && res.Rank <= k {
			rep.Hits++
		}
	}
	rep.DurationMS = time.Since(start).Milliseconds()
	return rep
}

// aggregate promedia las métricas de ranking sobre las preguntas con
// respuesta; el acierto de "no hay respuesta" se calcula aparte.
func aggregate(rs []QuestionResult) Metrics {
	var m Metrics
	m.Questions = len(rs)
	ranked, noAns, noAnsOK := 0, 0, 0
	for _, r := range rs {
		if r.Error != "" {
			m.Errors++
		}
		if r.Correct {
			m.Correct++
		}
		if r.Class == ClassNoAnswer {
			noAns++
			if r.Correct {
				noAnsOK++
			}
			continue
		}
		if r.NoResults {
			m.FalseNoAnswer++
		}
		ranked++
		m.HitAt5 += r.Hit5
		m.RecallAt5 += r.Recall5
		m.MRR += r.RR
		m.NDCG3 += r.NDCG3
		m.NDCG10 += r.NDCG10
		m.MAP += r.AP
	}
	if ranked > 0 {
		f := float64(ranked)
		m.HitAt5, m.RecallAt5, m.MRR, m.NDCG3, m.NDCG10, m.MAP = m.HitAt5/f, m.RecallAt5/f, m.MRR/f, m.NDCG3/f, m.NDCG10/f, m.MAP/f
	}
	if noAns > 0 {
		v := float64(noAnsOK) / float64(noAns)
		m.NoAnswerAccuracy = &v
	}
	return m
}

// Summary es una línea legible del informe.
func (r Report) Summary() string {
	t := r.Total
	na := "-"
	if t.NoAnswerAccuracy != nil {
		na = fmt.Sprintf("%.1f %%", 100**t.NoAnswerAccuracy)
	}
	return fmt.Sprintf("motor %s: %d preguntas, Recall@%d (acierto) = %.1f %%, Recall@5 (fracción) = %.1f %%, MRR = %.3f, nDCG@10 = %.3f, MAP = %.3f, sin respuesta = %s, errores %d, %d ms",
		r.Retriever, r.Questions, r.K, 100*t.HitAt5, 100*t.RecallAt5, t.MRR, t.NDCG10, t.MAP, na, t.Errors, r.DurationMS)
}

// Table es la tabla por clase en Markdown.
func (r Report) Table() string {
	var b strings.Builder
	b.WriteString("| Clase | Preguntas | Hit@5 | Recall@5 | MRR | nDCG@3 | nDCG@10 | MAP | Sin respuesta | Falso \"no hay\" | Errores |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|\n")
	row := func(name string, m Metrics) {
		na := "-"
		if m.NoAnswerAccuracy != nil {
			na = fmt.Sprintf("%.0f %%", 100**m.NoAnswerAccuracy)
		}
		if name == ClassNoAnswer {
			fmt.Fprintf(&b, "| %s | %d | - | - | - | - | - | - | %s | - | %d |\n", name, m.Questions, na, m.Errors)
			return
		}
		fmt.Fprintf(&b, "| %s | %d | %.1f %% | %.1f %% | %.3f | %.3f | %.3f | %.3f | %s | %d | %d |\n",
			name, m.Questions, 100*m.HitAt5, 100*m.RecallAt5, m.MRR, m.NDCG3, m.NDCG10, m.MAP, na, m.FalseNoAnswer, m.Errors)
	}
	for _, c := range Classes {
		if m, ok := r.ByClass[c]; ok {
			row(c, m)
		}
	}
	row("total", r.Total)
	return b.String()
}

// Regressions compara una corrida con la de referencia (la última aceptada,
// RC-02): devuelve las métricas que cayeron más que tol (total y por clase).
func Regressions(base, cur map[string]Metrics, tol float64) []string {
	var out []string
	for class, b := range base {
		c, ok := cur[class]
		if !ok {
			continue
		}
		check := func(name string, bv, cv float64) {
			if cv < bv-tol {
				out = append(out, fmt.Sprintf("%s %s: %.3f -> %.3f", class, name, bv, cv))
			}
		}
		if class != ClassNoAnswer {
			check("hit_at_5", b.HitAt5, c.HitAt5)
			check("mrr", b.MRR, c.MRR)
		}
		if b.NoAnswerAccuracy != nil && c.NoAnswerAccuracy != nil {
			check("no_answer_accuracy", *b.NoAnswerAccuracy, *c.NoAnswerAccuracy)
		}
	}
	return out
}

// ClassMetrics devuelve las métricas por clase más el total ("total").
func (r Report) ClassMetrics() map[string]Metrics {
	out := map[string]Metrics{"total": r.Total}
	for k, v := range r.ByClass {
		out[k] = v
	}
	return out
}
