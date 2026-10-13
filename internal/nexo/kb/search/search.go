// Package search define la consulta de conocimiento (NX-10, design §6.2 y
// §14): la interfaz Retriever, los filtros comunes obligatorios (tiempo y
// permisos) y la implementación pgfts (FTS nativo de Postgres).
//
// Regla de diseño §14.3.4: el filtro temporal y el de permisos viven en la
// capa de consulta, no en cada índice. Un Retriever recibe un Filter que no
// puede construir ni modificar (campos privados) y el Searcher vuelve a
// comprobar cada resultado: lo que un Retriever devuelva fuera del filtro se
// descarta y se cuenta (Result.Dropped), así un motor nuevo o defectuoso no
// puede saltarse la vigencia ni los espacios visibles.
package search

import (
	"context"
	"log"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
)

// Coincidencia de un resultado con la consulta.
const (
	MatchAllTerms  = "all_terms"
	MatchSomeTerms = "some_terms"
)

// MaxLimit es el máximo de resultados por búsqueda (design §8).
const MaxLimit = 50

// DefaultLimit es el número de resultados si no se pide otro.
const DefaultLimit = 10

// ClockSkew tolera la diferencia de reloj entre la aplicación y Postgres
// cuando la consulta usa "ahora" (los relojes de la fuente de verdad son los
// de la base).
const ClockSkew = 30 * time.Second

// Query es lo que pide quien busca.
type Query struct {
	Text     string
	Category string
	Tag      string
	Limit    int
	// NX-10 2b: modo (fts o exact, RC-22) y secciones vecinas (RC-10).
	Mode      string
	Neighbors bool
}

// Filter son los filtros comunes. Solo este paquete los construye.
type Filter struct {
	spaceIDs []string
	at       time.Time
	knownAt  time.Time
	current  bool // at y known_at son "ahora": el motor puede usar su atajo de vigentes
}

// SpaceIDs devuelve los espacios permitidos (copia).
func (f Filter) SpaceIDs() []string { return slices.Clone(f.spaceIDs) }

// At es el reloj del mundo: qué regía en ese momento.
func (f Filter) At() time.Time { return f.at }

// KnownAt es el reloj del registro: qué estaba publicado en ese momento.
func (f Filter) KnownAt() time.Time { return f.knownAt }

// Current informa que at y known_at son "ahora" (sin parámetros).
func (f Filter) Current() bool { return f.current }

// Hit es un resultado citable (RF-21, RN-03, design §14.3.5).
type Hit struct {
	SectionID      string                `json:"section_id"`
	SpaceID        string                `json:"space_id"`
	Space          string                `json:"space"`
	DocumentID     string                `json:"document_id"`
	DocKey         string                `json:"doc_key"`
	Title          string                `json:"title"`
	Category       string                `json:"category"`
	Tags           []string              `json:"tags"`
	Owner          string                `json:"owner"`
	Version        int                   `json:"version"`
	Anchor         string                `json:"anchor"`
	HeadingPath    []string              `json:"heading_path"`
	Excerpt        string                `json:"excerpt"`
	SourceLocation domain.SourceLocation `json:"source_location"`
	Extraction     string                `json:"extraction"`
	ValidFrom      time.Time             `json:"valid_from"`
	ValidUntil     *time.Time            `json:"valid_until,omitempty"`
	RecordedFrom   time.Time             `json:"recorded_from"`
	RecordedUntil  *time.Time            `json:"recorded_until,omitempty"`
	DocStatus      string                `json:"document_status"`
	SupersededBy   string                `json:"superseded_by,omitempty"`
	Rank           float64               `json:"rank"`
	Match          string                `json:"match"` // all_terms | some_terms
	Retriever      string                `json:"retriever"`
	Citation       string                `json:"citation"`
	// NX-10 2b (RC-08): enlace permanente a la sección en Nexo (la API lo
	// completa con la URL pública) y enlace a la fuente original si se
	// declaró (front matter source_url o la wiki de origen).
	Link      string `json:"link,omitempty"`
	SourceURL string `json:"source_url,omitempty"`
	// Coverage (RC-07): fracción ponderada por rareza de los términos de la
	// consulta que contiene la sección (1 = todos; -1 = el motor no la
	// calcula).
	Coverage float64 `json:"coverage"`
	// NX-10 R2: similitud de coseno con la pregunta (solo si hay índice
	// vectorial activo) y qué búsquedas encontraron la sección (text, vector).
	Similarity *float64 `json:"similarity,omitempty"`
	MatchedBy  []string `json:"matched_by,omitempty"`
	// Neighbors (RC-10): secciones anterior y siguiente, si se pidieron.
	Neighbors []Neighbor `json:"neighbors,omitempty"`
}

// Neighbor es una sección vecina (contexto).
type Neighbor struct {
	Anchor      string   `json:"anchor"`
	HeadingPath []string `json:"heading_path"`
	Position    string   `json:"position"` // previous | next
	Body        string   `json:"body"`
}

// SearcherOptions son las opciones comunes a todo motor (R1).
type SearcherOptions struct {
	// MinCoverage (RC-07): umbral de cobertura; un resultado por debajo se
	// descarta y, si no queda ninguno, la respuesta es "no hay estándar
	// oficial". 0 = sin umbral. Los motores que no calculan cobertura (-1)
	// no se filtran.
	MinCoverage float64 `json:"min_coverage"`
	// MaxPerDoc (RC-09): máximo de secciones de un mismo documento en la
	// respuesta (diversidad). 0 = sin tope.
	MaxPerDoc int `json:"max_per_doc"`
	// NX-10 R2 (umbral del modo hybrid, design §20.5): un resultado con
	// similitud conocida pasa si la similitud llega a MinSimilarity (el
	// significado alcanza solo) o si la cobertura llega a MinCoverage y la
	// similitud a MinTextSimilarity (el texto coincide y el significado no lo
	// contradice). Sin similitud (modo fts o vectores no disponibles) rige solo
	// la cobertura. 0 = sin esa condición.
	MinSimilarity     float64 `json:"min_similarity,omitempty"`
	MinTextSimilarity float64 `json:"min_text_similarity,omitempty"`
}

// Relevance registra la decisión del umbral (RC-21: umbral aplicado, mejor
// puntaje y cuántos resultados lo superaron).
type Relevance struct {
	MinCoverage    float64 `json:"min_coverage"`
	BestCoverage   float64 `json:"best_coverage"`
	MinSimilarity  float64 `json:"min_similarity,omitempty"`
	BestSimilarity float64 `json:"best_similarity,omitempty"`
	Above          int     `json:"above"`
	Below          int     `json:"below"`
}

// SectionPath es la ruta del panel de Nexo que muestra una sección (RC-08):
// la versión fija el contenido citado (enlace permanente).
func SectionPath(space, docKey, anchor string, version int) string {
	v := url.Values{}
	v.Set("doc", docKey)
	if version > 0 {
		v.Set("v", itoa(version))
	}
	return "/dashboard/kb/spaces/" + url.PathEscape(space) + "/doc?" + v.Encode() + "#" + url.PathEscape(anchor)
}

// Retriever es un motor de búsqueda (pgfts hoy; BM25 local, híbrido o grafo
// después). Debe devolver como máximo q.Limit resultados ordenados.
type Retriever interface {
	Name() string
	Search(ctx context.Context, q Query, f Filter) ([]Hit, error)
}

// Result es la respuesta de una búsqueda.
type Result struct {
	Hits      []Hit     `json:"results"`
	NoResults bool      `json:"no_results"`
	Message   string    `json:"message,omitempty"`
	At        time.Time `json:"at"`
	KnownAt   time.Time `json:"known_at"`
	Retriever string    `json:"retriever"`
	Mode      string    `json:"mode"`
	// Relevance: decisión del umbral (solo con umbral activo).
	Relevance *Relevance `json:"relevance,omitempty"`
	// Vector (NX-10 R2): estado de la búsqueda por significado en esta
	// consulta (usada, no disponible o fallida: en ese caso la respuesta es solo
	// por texto y Notice lo explica).
	Vector  *VectorInfo `json:"vector,omitempty"`
	Notice  string      `json:"notice,omitempty"`
	Dropped int         `json:"-"`
}

// NoResultsMessage es el aviso explícito de HU-06.
const NoResultsMessage = "No existe un estándar oficial publicado sobre este tema en los espacios visibles."

// Searcher aplica los filtros comunes alrededor de un Retriever.
type Searcher struct {
	r    Retriever
	now  func() time.Time
	opts SearcherOptions
}

// SetOptions fija las opciones comunes (R1).
func (s *Searcher) SetOptions(o SearcherOptions) { s.opts = o }

// Options devuelve las opciones comunes.
func (s *Searcher) Options() SearcherOptions { return s.opts }

// NewSearcher crea el buscador sobre un Retriever.
func NewSearcher(r Retriever) *Searcher { return &Searcher{r: r, now: time.Now} }

// Retriever devuelve el motor en uso.
func (s *Searcher) Retriever() Retriever { return s.r }

// Search valida la consulta, arma el filtro con los espacios visibles (ya
// calculados por el llamador con authz.VisibleScopes) y los tiempos (por
// defecto, vigente hoy), llama al Retriever y descarta lo que no cumpla el
// filtro.
func (s *Searcher) Search(ctx context.Context, q Query, visibleSpaceIDs []string, at, knownAt *time.Time) (Result, error) {
	q.Text = strings.TrimSpace(q.Text)
	if q.Text == "" {
		return Result{}, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "q is required")
	}
	if len([]rune(q.Text)) > 500 {
		return Result{}, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "q is longer than 500 characters")
	}
	if !utf8.ValidString(q.Text) { // NX-10 R2: antes llegaba a Postgres y daba 500
		return Result{}, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "q must be valid UTF-8")
	}
	if q.Limit <= 0 {
		q.Limit = DefaultLimit
	}
	if q.Limit > MaxLimit {
		q.Limit = MaxLimit
	}
	now := s.now().UTC()
	f := Filter{spaceIDs: dedupe(visibleSpaceIDs), at: now, knownAt: now, current: at == nil && knownAt == nil}
	if at != nil {
		f.at = at.UTC()
	}
	if knownAt != nil {
		f.knownAt = knownAt.UTC()
		if f.knownAt.After(now) {
			f.knownAt = now // el registro no conoce el futuro
		}
	}
	q.Mode = strings.ToLower(strings.TrimSpace(q.Mode))
	var vinfo *VectorInfo
	switch q.Mode {
	case "":
		q.Mode = ModeFTS
		if d, ok := s.r.(DefaultModer); ok {
			q.Mode = d.DefaultMode(ctx)
		}
	case ModeFTS, ModeExact:
	case ModeHybrid:
		if _, ok := s.r.(InfoRetriever); !ok {
			// R2 no configurado: se responde por texto y se avisa (sin error).
			q.Mode = ModeFTS
			vinfo = &VectorInfo{Status: VectorUnavailable, Reason: "la búsqueda por significado no está configurada en el servidor (NEXO_KB_EMBEDDER)"}
		}
	default:
		return Result{}, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "mode must be fts, exact or hybrid")
	}
	res := Result{At: f.at, KnownAt: f.knownAt, Retriever: s.r.Name(), Mode: q.Mode, Hits: []Hit{}, Vector: vinfo}
	if len(f.spaceIDs) > 0 {
		// Con umbral o diversidad se piden más candidatos para completar el
		// límite después de filtrar.
		want := q.Limit
		rq := q
		if s.opts.MinCoverage > 0 || s.opts.MaxPerDoc > 0 || s.opts.MinSimilarity > 0 {
			rq.Limit = min(q.Limit*3, MaxLimit)
		}
		var hits []Hit
		var err error
		if ir, ok := s.r.(InfoRetriever); ok && q.Mode == ModeHybrid {
			hits, res.Vector, err = ir.SearchInfo(ctx, rq, f)
		} else {
			hits, err = s.r.Search(ctx, rq, f)
		}
		if err != nil {
			return Result{}, err
		}
		if res.Vector != nil && res.Vector.Status != VectorUsed && q.Mode == ModeHybrid {
			res.Mode = ModeFTS // respuesta solo por texto
		}
		perDoc := map[string]int{}
		var rel *Relevance
		if s.opts.MinCoverage > 0 && (q.Mode == ModeFTS || q.Mode == ModeHybrid) {
			rel = &Relevance{MinCoverage: s.opts.MinCoverage}
			if res.Mode == ModeHybrid {
				rel.MinSimilarity = s.opts.MinSimilarity
			}
		}
		for _, h := range hits {
			if !f.Allows(h) {
				res.Dropped++
				continue
			}
			if rel != nil {
				if h.Coverage >= 0 {
					rel.BestCoverage = max(rel.BestCoverage, h.Coverage)
				}
				if h.Similarity != nil {
					rel.BestSimilarity = max(rel.BestSimilarity, *h.Similarity)
				}
				if !s.relevant(h) {
					rel.Below++
					continue
				}
				rel.Above++
			}
			if len(res.Hits) == want {
				continue // se sigue contando el umbral
			}
			if s.opts.MaxPerDoc > 0 && perDoc[h.DocumentID] >= s.opts.MaxPerDoc {
				continue
			}
			perDoc[h.DocumentID]++
			if h.Retriever == "" {
				h.Retriever = s.r.Name()
			}
			h.Citation = Citation(h)
			h.Link = SectionPath(h.Space, h.DocKey, h.Anchor, h.Version)
			res.Hits = append(res.Hits, h)
		}
		res.Relevance = rel
		if res.Dropped > 0 {
			log.Printf("[nexo] kb: el motor %s devolvió %d resultados fuera de los filtros de tiempo o permisos; se descartaron", s.r.Name(), res.Dropped)
		}
	}
	if res.Vector != nil {
		if res.Vector.Status != VectorUsed {
			res.Retriever = "pgfts"
			res.Notice = "Respuesta solo por texto: " + res.Vector.Reason + "."
		} else if res.Vector.IndexLag > 0 {
			res.Notice = "Hay secciones recién publicadas que todavía no tienen vector; esas se encuentran solo por texto."
		}
	}
	if len(res.Hits) == 0 {
		res.NoResults = true
		res.Message = NoResultsMessage
	}
	return res, nil
}

// relevant aplica el umbral de "no hay estándar" (RC-07 y, con vectores,
// design §20.5). Nunca se conserva un resultado por debajo del umbral
// (RC-21).
func (s *Searcher) relevant(h Hit) bool {
	o := s.opts
	covOK := h.Coverage < 0 || h.Coverage >= o.MinCoverage
	if h.Similarity == nil {
		return covOK
	}
	sim := *h.Similarity
	if o.MinSimilarity > 0 && sim >= o.MinSimilarity {
		return true
	}
	if h.Coverage < 0 {
		return o.MinSimilarity <= 0
	}
	return covOK && sim >= o.MinTextSimilarity
}

// Allows comprueba un resultado contra el filtro (permisos y ambos relojes).
func (f Filter) Allows(h Hit) bool {
	if !slices.Contains(f.spaceIDs, h.SpaceID) {
		return false
	}
	at, known := f.at, f.knownAt
	skew := time.Duration(0)
	if f.current {
		skew = ClockSkew
	}
	if h.ValidFrom.After(at.Add(skew)) || (h.ValidUntil != nil && !h.ValidUntil.After(at.Add(-skew))) {
		return false
	}
	if h.RecordedFrom.After(known.Add(skew)) || (h.RecordedUntil != nil && !h.RecordedUntil.After(known.Add(-skew))) {
		return false
	}
	return true
}

// Citation arma el texto de cita (RN-03): espacio, documento, sección,
// versión, vigencia y procedencia.
func Citation(h Hit) string {
	var b strings.Builder
	b.WriteString(h.Space + " / " + h.Title)
	if len(h.HeadingPath) > 0 {
		b.WriteString(" > " + strings.Join(h.HeadingPath, " > "))
	}
	b.WriteString(" (" + h.DocKey + "#" + h.Anchor + ", v" + itoa(h.Version) + ", vigente desde " + h.ValidFrom.Format("2006-01-02"))
	if h.ValidUntil != nil {
		b.WriteString(" hasta " + h.ValidUntil.Format("2006-01-02"))
	}
	if loc := LocationLabel(h.SourceLocation); loc != "" {
		b.WriteString(", " + loc)
	}
	b.WriteString(")")
	return b.String()
}

// LocationLabel describe la procedencia para citar ("página 12", "hoja
// Componentes, filas 40-60", "líneas 10-24").
func LocationLabel(l domain.SourceLocation) string {
	rng := func(name, plural, a, b string) string {
		s, okS := l[a]
		e, okE := l[b]
		if !okS {
			return ""
		}
		if okE && num(e) != num(s) {
			return plural + " " + itoa(num(s)) + "-" + itoa(num(e))
		}
		return name + " " + itoa(num(s))
	}
	var parts []string
	if v := rng("página", "páginas", "page_start", "page_end"); v != "" {
		parts = append(parts, v)
	}
	if sh, ok := l["sheet"].(string); ok && sh != "" {
		parts = append(parts, "hoja "+sh)
	}
	if v := rng("fila", "filas", "row_start", "row_end"); v != "" {
		parts = append(parts, v)
	}
	if v := rng("diapositiva", "diapositivas", "slide_start", "slide_end"); v != "" {
		parts = append(parts, v)
	}
	// NX-10 2b: Word (párrafos y tablas).
	if v := rng("párrafo", "párrafos", "paragraph_start", "paragraph_end"); v != "" {
		parts = append([]string{v}, parts...)
	}
	if t, ok := l["table"]; ok && num(t) > 0 {
		parts = append([]string{"tabla " + itoa(num(t))}, parts...)
	}
	if len(parts) == 0 {
		if v := rng("línea", "líneas", "line_start", "line_end"); v != "" {
			parts = append(parts, v)
		}
	}
	return strings.Join(parts, ", ")
}

func num(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	}
	return 0
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

func dedupe(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}
