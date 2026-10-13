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
	"slices"
	"strings"
	"time"

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
	Dropped   int       `json:"-"`
}

// NoResultsMessage es el aviso explícito de HU-06.
const NoResultsMessage = "No existe un estándar oficial publicado sobre este tema en los espacios visibles."

// Searcher aplica los filtros comunes alrededor de un Retriever.
type Searcher struct {
	r   Retriever
	now func() time.Time
}

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
	res := Result{At: f.at, KnownAt: f.knownAt, Retriever: s.r.Name(), Hits: []Hit{}}
	if len(f.spaceIDs) > 0 {
		hits, err := s.r.Search(ctx, q, f)
		if err != nil {
			return Result{}, err
		}
		for _, h := range hits {
			if !f.Allows(h) {
				res.Dropped++
				continue
			}
			if len(res.Hits) == q.Limit {
				break
			}
			h.Retriever = s.r.Name()
			h.Citation = Citation(h)
			res.Hits = append(res.Hits, h)
		}
		if res.Dropped > 0 {
			log.Printf("[nexo] kb: el motor %s devolvió %d resultados fuera de los filtros de tiempo o permisos; se descartaron", s.r.Name(), res.Dropped)
		}
	}
	if len(res.Hits) == 0 {
		res.NoResults = true
		res.Message = NoResultsMessage
	}
	return res, nil
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
