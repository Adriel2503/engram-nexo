package client

// Pruebas de la copia local sin servidor (D-01): aplicar el feed, vigencia
// al consultar con el reloj local, borrado de espacios no visibles,
// umbral de "no hay estándar", modo exact y ventana de solape.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func ptr(t time.Time) *time.Time { return &t }

func openTestLocal(t *testing.T) *Local {
	t.Helper()
	l, err := OpenLocal(filepath.Join(t.TempDir(), "kb.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	l.now = func() time.Time { return t0 }
	return l
}

func doc(id, space, key, title string, secs ...SyncSection) *SyncDocument {
	return &SyncDocument{ID: id, Space: space, DocKey: key, Title: title, Category: "lineamiento", Status: "active", CurrentVersion: 1,
		ValidFrom: t0.AddDate(0, -1, 0), Sections: secs}
}

func sec(v int, anchor, body string, from time.Time, until *time.Time) SyncSection {
	return SyncSection{Version: v, Anchor: anchor, HeadingPath: []string{anchor}, Body: body, ValidFrom: from, ValidUntil: until,
		SourceLocation: map[string]any{"line_start": 1, "line_end": 3}}
}

var spArq = SyncSpace{ID: "11111111-1111-1111-1111-111111111111", Slug: "arquitectura", Name: "Arquitectura", Role: "lector"}
var spSeg = SyncSpace{ID: "22222222-2222-2222-2222-222222222222", Slug: "seguridad", Name: "Seguridad", Role: "lector"}

func basePage() SyncPage {
	return SyncPage{Seq: 10, VisibleSpaces: []SyncSpace{spArq, spSeg}, PublicURL: "https://nexo.test", Changes: []SyncChange{
		{Seq: 1, Kind: "upsert", Space: "arquitectura", Document: doc("d1", "arquitectura", "datos/tablas", "Nomenclatura de tablas",
			sec(1, "tablas", "## Tablas\nLas tablas de la base de datos se nombran en singular con prefijo del módulo.", t0.AddDate(0, -1, 0), nil))},
		// Programada: entra en vigor dentro de 10 días (KP-07).
		{Seq: 2, Kind: "upsert", Space: "arquitectura", Document: doc("d2", "arquitectura", "logging", "Logging",
			sec(1, "niveles", "## Niveles\nUsar Serilog con nivel Information para eventos de negocio.", t0.AddDate(0, -1, 0), ptr(t0.AddDate(0, 0, 10))),
			sec(2, "niveles", "## Niveles\nUsar OpenTelemetry con nivel Information para eventos de negocio.", t0.AddDate(0, 0, 10), nil))},
		// Vence en 5 días.
		{Seq: 3, Kind: "upsert", Space: "seguridad", Document: doc("d3", "seguridad", "tokens", "Tokens temporales",
			sec(1, "caducidad", "## Caducidad\nLos tokens de integración caducan a los 90 días.", t0.AddDate(0, -1, 0), ptr(t0.AddDate(0, 0, 5))))},
	}}
}

func TestLocalApplyAndValidityAtQueryTime(t *testing.T) {
	l := openTestLocal(t)
	ctx := context.Background()
	st, err := l.Apply(ctx, basePage())
	if err != nil || st.Upserts != 3 {
		t.Fatalf("apply: %+v %v", st, err)
	}
	// Hoy rige Serilog, no OpenTelemetry.
	r, err := l.Search(ctx, LocalQuery{Text: "nivel de logging para eventos de negocio"})
	if err != nil || r.NoResults || !strings.Contains(r.Hits[0].Body, "Serilog") || r.Hits[0].Version != 1 {
		t.Fatalf("hoy: %+v %v", r.Hits, err)
	}
	for _, h := range r.Hits {
		if strings.Contains(h.Body, "OpenTelemetry") {
			t.Fatal("la versión programada apareció antes de su fecha")
		}
	}
	// Con at en el futuro (o el reloj local avanzado), entra sola.
	r, _ = l.Search(ctx, LocalQuery{Text: "nivel de logging para eventos de negocio", At: ptr(t0.AddDate(0, 0, 11))})
	if r.NoResults || !strings.Contains(r.Hits[0].Body, "OpenTelemetry") {
		t.Fatalf("programada en su fecha: %+v", r.Hits)
	}
	l.now = func() time.Time { return t0.AddDate(0, 0, 11) }
	r, _ = l.Search(ctx, LocalQuery{Text: "caducidad de tokens de integración"})
	if !r.NoResults {
		t.Fatalf("el documento vencido sigue apareciendo: %+v", r.Hits)
	}
	d, found, err := l.Get(ctx, "arquitectura", "logging", "", nil)
	if err != nil || !found || d.Version != 2 {
		t.Fatalf("get con el reloj avanzado: %+v %v %v", d, found, err)
	}
}

func TestLocalRemovesSpacesNoLongerVisible(t *testing.T) {
	l := openTestLocal(t)
	ctx := context.Background()
	if _, err := l.Apply(ctx, basePage()); err != nil {
		t.Fatal(err)
	}
	st, err := l.Apply(ctx, SyncPage{Seq: 11, VisibleSpaces: []SyncSpace{spArq}})
	if err != nil || len(st.SpacesRemoved) != 1 || st.SpacesRemoved[0] != "seguridad" {
		t.Fatalf("espacios quitados: %+v %v", st, err)
	}
	s := l.Status(ctx)
	if s.Spaces != 1 || s.Documents != 2 {
		t.Fatalf("estado: %+v", s)
	}
	if r, _ := l.Search(ctx, LocalQuery{Text: "tokens de integración"}); !r.NoResults {
		t.Fatalf("contenido de un espacio no visible: %+v", r.Hits)
	}
	// remove de un documento.
	if _, err := l.Apply(ctx, SyncPage{Seq: 12, VisibleSpaces: []SyncSpace{spArq}, Changes: []SyncChange{{Seq: 12, Kind: "remove", Space: "arquitectura", DocID: "d1"}}}); err != nil {
		t.Fatal(err)
	}
	if r, _ := l.Search(ctx, LocalQuery{Text: "nombres de tablas"}); !r.NoResults {
		t.Fatalf("documento quitado aparece: %+v", r.Hits)
	}
}

func TestLocalNoStandardAndExact(t *testing.T) {
	l := openTestLocal(t)
	ctx := context.Background()
	if _, err := l.Apply(ctx, basePage()); err != nil {
		t.Fatal(err)
	}
	r, err := l.Search(ctx, LocalQuery{Text: "cómo se diseña el esquema GraphQL de un servicio"})
	if err != nil || !r.NoResults {
		t.Fatalf("sin estándar debía ser no_results: %+v %v", r.Hits, err)
	}
	r, err = l.Search(ctx, LocalQuery{Text: "Serilog", Mode: "exact"})
	if err != nil || r.NoResults || r.Hits[0].DocKey != "logging" {
		t.Fatalf("exact: %+v %v", r.Hits, err)
	}
	r, err = l.Search(ctx, LocalQuery{Text: "/Open[a-z]+metry/", Mode: "exact", At: ptr(t0.AddDate(0, 1, 0))})
	if err != nil || r.NoResults {
		t.Fatalf("regex: %+v %v", r.Hits, err)
	}
	// Plural y tildes: "tabla" y "nombran" encuentran "tablas" y "nombran".
	r, _ = l.Search(ctx, LocalQuery{Text: "nombre de la tabla en la base de datos"})
	if r.NoResults || r.Hits[0].DocKey != "datos/tablas" {
		t.Fatalf("raíces: %+v", r.Hits)
	}
}

func TestSinceFromOverlap(t *testing.T) {
	w := 2 * time.Minute
	cps := []checkpoint{{Seq: 100, At: t0}, {Seq: 150, At: t0.Add(10 * time.Minute)}, {Seq: 151, At: t0.Add(10*time.Minute + 30*time.Second)}}
	// La última es de t0+10m30s: la más nueva anterior a t0+8m30s es la de t0.
	if got := sinceFrom(cps, w, 1000); got != 100 {
		t.Fatalf("since = %d, quería 100 (relee el intervalo anterior)", got)
	}
	if got := sinceFrom([]checkpoint{{Seq: 1500, At: t0}}, w, 1000); got != 500 {
		t.Fatalf("un solo punto: %d", got)
	}
	if got := sinceFrom(nil, w, 1000); got != 0 {
		t.Fatalf("sin puntos: %d", got)
	}
	if fingerprint("https://a", "x") == fingerprint("https://a", "y") || strings.Contains(fingerprint("https://a", "secreto"), "secreto") {
		t.Fatal("huella")
	}
}

func TestEffectiveAllowlist(t *testing.T) {
	agent := map[string]bool{"mem_search": true, "mem_save": true}
	cases := []struct {
		name      string
		allow     map[string]bool
		connected bool
		ok        bool
	}{
		{"todas sin conexión", nil, false, false},
		{"todas con conexión", nil, true, true},
		{"perfil agent sin conexión", agent, false, false},
		{"perfil agent con conexión", agent, true, true},
		{"explícitas sin conexión", map[string]bool{"nexo_search": true}, false, true},
		{"token nexo", map[string]bool{"mem_search": true, "nexo": true}, false, true},
		{"perfil admin con conexión", map[string]bool{"mem_stats": true}, true, false},
	}
	for _, c := range cases {
		if _, ok := effectiveAllowlist(c.allow, c.connected); ok != c.ok {
			t.Errorf("%s: %v", c.name, ok)
		}
	}
}
