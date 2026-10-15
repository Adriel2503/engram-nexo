package dashboard

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"golang.org/x/net/html"
)

func nexoRendered(t *testing.T, ctx context.Context, component templ.Component) string {
	t.Helper()
	var b bytes.Buffer
	if err := component.Render(ctx, &b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// Collect visible text while respecting native, closed details. This checks
// that simplification never hides an actionable warning with the diagnostics.
func nexoVisibleText(t *testing.T, source string) string {
	t.Helper()
	n, err := html.Parse(strings.NewReader(source))
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		if n.Type == html.ElementNode && n.Data == "details" {
			open := false
			for _, a := range n.Attr {
				open = open || a.Key == "open"
			}
			if !open {
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					if c.Data == "summary" {
						walk(c)
						break
					}
				}
				return
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

func TestNexoSimplifiedSearchKeepsWarningsVisible(t *testing.T) {
	p := NexoSearchStatusPage{
		Verdict: NexoSearchVerdict{Class: "info", Title: "La búsqueda usa solo texto."},
		Config: []NexoSearchRow{
			{Label: "Índice de texto", Value: "Necesita reindexar", Chip: "nx-chip-exp", Note: "Ejecuta engram nexo kb reindex."},
			{Label: "Proveedor", Value: "Sin proveedor", Env: "NEXO_KB_EMBEDDER"},
		},
		Dead: NexoSearchDead{Total: 1, Summary: "Una sección falló.", Errors: []NexoSearchDeadError{{Message: "Proveedor no disponible"}}},
	}
	source := nexoRendered(t, context.Background(), NexoSearchStatusPageView(p))
	visible := nexoVisibleText(t, source)
	for _, want := range []string{p.Verdict.Title, "Necesita reindexar", "engram nexo kb reindex", "Proveedor no disponible"} {
		if !strings.Contains(visible, want) {
			t.Errorf("aviso oculto: %s", want)
		}
	}
	if strings.Contains(visible, "NEXO_KB_EMBEDDER") || !strings.Contains(source, "NEXO_KB_EMBEDDER") {
		t.Error("la configuración debe conservarse dentro de un desplegable cerrado")
	}
}

func TestNexoSimplifiedNavigationPreservesAccessAndActivePage(t *testing.T) {
	was := NexoBrand()
	nexoBrandOn.Store(true)
	t.Cleanup(func() { nexoBrandOn.Store(was) })
	for _, c := range []struct {
		name         string
		admin, audit bool
		active       string
	}{
		{"miembro", false, false, "kb"},
		{"auditor", false, true, "audit"},
		{"administrador", true, true, NexoSearchStatusTab},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			if c.audit {
				ctx = WithNexoAuditNav(ctx)
			}
			source := nexoRendered(t, ctx, nexoNavTabs(c.active, c.admin))
			if strings.Count(source, `aria-current="page"`) != 1 {
				t.Error("debe haber un solo destino activo")
			}
			if strings.Contains(source, NexoSearchStatusPath) != c.admin || strings.Contains(source, `href="/dashboard/admin/audit"`) != c.audit {
				t.Error("cambiaron los destinos disponibles para el rol")
			}
			if c.admin && !strings.Contains(nexoVisibleText(t, source), "Estado de la búsqueda") {
				t.Error("el grupo de la página activa debe estar abierto")
			}
		})
	}
}
