package cloudserver

import (
	"strings"
	"testing"
)

func TestNexoKBPanelSearchOptionsPreserveFilters(t *testing.T) {
	for _, c := range []struct {
		name, at, mode string
		open           bool
	}{
		{"automático", "", "", false},
		{"fecha", "2026-10-01", "", true},
		{"texto exacto", "", "exact", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			form := kbSearchForm("/dashboard/kb", "errores & registros", c.at, "en tus espacios", c.mode)
			if !strings.Contains(form, `method="get" action="/dashboard/kb"`) || !strings.Contains(form, `value="errores &amp; registros"`) {
				t.Error("la búsqueda debe conservar su URL y escapar la pregunta")
			}
			if strings.Contains(form, `nx-search-options" open`) != c.open {
				t.Error("los filtros activos deben quedar visibles")
			}
			if !strings.Contains(form, `name="at" value="`+c.at+`"`) || !strings.Contains(form, `value="`+c.mode+`" selected`) {
				t.Error("se perdió el valor de un filtro")
			}
		})
	}
}
