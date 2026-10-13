package search

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
)

// rogueRetriever ignora el filtro y devuelve de todo: otros espacios,
// documentos vencidos, futuros, reemplazados en el registro y aún no
// publicados. El Searcher debe descartarlos (design §14.3.4).
type rogueRetriever struct {
	hits []Hit
	got  Filter
}

func (r *rogueRetriever) Name() string { return "rogue" }
func (r *rogueRetriever) Search(_ context.Context, _ Query, f Filter) ([]Hit, error) {
	r.got = f
	return r.hits, nil
}

func tp(t time.Time) *time.Time { return &t }

func TestRetrieverCannotBypassTimeAndPermissionFilters(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	ok := Hit{SectionID: "ok", SpaceID: "visible", DocKey: "a", ValidFrom: now.Add(-30 * day), RecordedFrom: now.Add(-30 * day)}
	otherSpace := ok
	otherSpace.SectionID, otherSpace.SpaceID = "other-space", "restringido"
	expired := ok
	expired.SectionID, expired.ValidUntil = "expired", tp(now.Add(-day))
	future := ok
	future.SectionID, future.ValidFrom = "future", now.Add(10*day)
	replaced := ok
	replaced.SectionID, replaced.RecordedUntil = "replaced", tp(now.Add(-day))
	unpublished := ok
	unpublished.SectionID, unpublished.RecordedFrom = "unpublished", now.Add(day)

	r := &rogueRetriever{hits: []Hit{otherSpace, expired, future, replaced, unpublished, ok}}
	s := NewSearcher(r)
	s.now = func() time.Time { return now }

	res, err := s.Search(context.Background(), Query{Text: "x"}, []string{"visible"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 || res.Hits[0].SectionID != "ok" || res.Dropped != 5 {
		t.Fatalf("solo pasa lo permitido y vigente: %+v (descartados %d)", res.Hits, res.Dropped)
	}
	if !r.got.Current() || r.got.At() != now || len(r.got.SpaceIDs()) != 1 {
		t.Fatalf("el motor recibe el filtro común: %+v", r.got)
	}
	// El motor no puede ampliar el filtro: SpaceIDs devuelve una copia.
	ids := r.got.SpaceIDs()
	ids[0] = "restringido"
	if r.got.SpaceIDs()[0] != "visible" {
		t.Fatal("el filtro es inmutable para el motor")
	}

	// at= en el futuro: el documento futuro pasa; el vencido no.
	at := now.Add(11 * day)
	res, _ = s.Search(context.Background(), Query{Text: "x"}, []string{"visible"}, &at, nil)
	got := map[string]bool{}
	for _, h := range res.Hits {
		got[h.SectionID] = true
	}
	if !got["future"] || got["expired"] || got["other-space"] || got["replaced"] {
		t.Fatalf("at futuro: %v", got)
	}

	// known_at en el pasado: lo reemplazado ayer sí estaba publicado entonces.
	known := now.Add(-2 * day)
	res, _ = s.Search(context.Background(), Query{Text: "x"}, []string{"visible"}, nil, &known)
	got = map[string]bool{}
	for _, h := range res.Hits {
		got[h.SectionID] = true
	}
	if !got["replaced"] || got["unpublished"] {
		t.Fatalf("known_at pasado: %v", got)
	}

	// known_at no puede ser futuro (el registro no conoce el futuro).
	kf := now.Add(5 * day)
	res, _ = s.Search(context.Background(), Query{Text: "x"}, []string{"visible"}, nil, &kf)
	if !res.KnownAt.Equal(now) {
		t.Fatalf("known_at futuro se recorta a ahora: %v", res.KnownAt)
	}

	// Sin espacios visibles el motor ni se llama.
	r.got = Filter{}
	res, _ = s.Search(context.Background(), Query{Text: "x"}, nil, nil, nil)
	if !res.NoResults || res.Message != NoResultsMessage || r.got.At() != (time.Time{}) {
		t.Fatalf("sin espacios: %+v", res)
	}
}

func TestSearchValidatesQueryAndLimits(t *testing.T) {
	r := &rogueRetriever{}
	s := NewSearcher(r)
	if _, err := s.Search(context.Background(), Query{Text: "  "}, []string{"a"}, nil, nil); !domain.IsKind(err, domain.KindInvalid) {
		t.Fatalf("q vacío: %v", err)
	}
	if _, err := s.Search(context.Background(), Query{Text: strings.Repeat("a", 501)}, []string{"a"}, nil, nil); !domain.IsKind(err, domain.KindInvalid) {
		t.Fatalf("q largo: %v", err)
	}
	many := make([]Hit, 80)
	for i := range many {
		many[i] = Hit{SpaceID: "a", ValidFrom: time.Now().Add(-time.Hour), RecordedFrom: time.Now().Add(-time.Hour)}
	}
	r.hits = many
	res, _ := s.Search(context.Background(), Query{Text: "x", Limit: 500}, []string{"a"}, nil, nil)
	if len(res.Hits) != MaxLimit {
		t.Fatalf("límite máximo %d, got %d", MaxLimit, len(res.Hits))
	}
}

func TestCitationAndLocationLabels(t *testing.T) {
	h := Hit{Space: "lineamientos-frontend", Title: "Formularios", DocKey: "frontend/formularios", Anchor: "formularios.validacion",
		HeadingPath: []string{"Formularios", "Validación"}, Version: 3, ValidFrom: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		SourceLocation: domain.SourceLocation{"line_start": 10, "line_end": 24}}
	c := Citation(h)
	for _, want := range []string{"lineamientos-frontend / Formularios > Formularios > Validación", "frontend/formularios#formularios.validacion", "v3", "vigente desde 2026-09-01", "líneas 10-24"} {
		if !strings.Contains(c, want) {
			t.Fatalf("cita %q sin %q", c, want)
		}
	}
	cases := map[string]domain.SourceLocation{
		"página 12":                     {"page_start": 12, "page_end": 12},
		"páginas 3-5":                   {"page_start": 3, "page_end": 5},
		"hoja Componentes, filas 40-60": {"sheet": "Componentes", "row_start": 40, "row_end": 60},
		"diapositiva 7":                 {"slide_start": float64(7)},
		"":                              {},
	}
	for want, loc := range cases {
		if got := LocationLabel(loc); got != want {
			t.Errorf("LocationLabel(%v) = %q, want %q", loc, got, want)
		}
	}
}
