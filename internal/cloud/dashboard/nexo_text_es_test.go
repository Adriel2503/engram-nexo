package dashboard

// Nexo IA · K6 (OP-06) — todo texto que components.templ pasa por nx() tiene
// su traducción, y las traducciones no dejan marcadores ni texto vacío.

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestNexoPanelTextsAreTranslated(t *testing.T) {
	src, err := os.ReadFile("components.templ")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`nx\("([^"]*)"\)`)
	found := 0
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		key := m[1]
		found++
		if _, ok := nexoText[key]; ok {
			continue
		}
		if es, ok := nexoTextPanel[key]; !ok || strings.TrimSpace(es) == "" {
			t.Errorf("components.templ: sin traducción para nx(%q)", key)
		}
	}
	if found < 150 {
		t.Fatalf("components.templ solo pasa %d textos por nx(): ¿se perdió el envoltorio?", found)
	}
}

func TestNexoPanelTextsOnlyWithBrand(t *testing.T) {
	was := NexoBrand()
	t.Cleanup(func() { nexoBrandOn.Store(was) })
	nexoBrandOn.Store(false)
	if got := nx("Create User"); got != "Create User" {
		t.Fatalf("sin la marca, el texto de Engram queda igual: %q", got)
	}
	nexoBrandOn.Store(true)
	for en, es := range map[string]string{"Create User": "Crear usuario", "Create Token": "Crear token", "Expires in days (default 90)": "Días de vida (por defecto 90)"} {
		if got := nx(en); got != es {
			t.Fatalf("nx(%q) = %q, quería %q", en, got, es)
		}
	}
}

// OP-06: las cadenas que las funciones de Go de Engram (helpers.go y
// dashboard.go) envuelven con nx() también tienen traducción.
func TestNexoGoFilesWrappedTextsAreTranslated(t *testing.T) {
	re := regexp.MustCompile(`nx\("([^"]*)"\)`)
	found := 0
	for _, f := range []string{"helpers.go", "dashboard.go"} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			found++
			if es, ok := nexoLookup(m[1]); !ok || strings.TrimSpace(es) == "" {
				t.Errorf("%s: sin traducción para nx(%q)", f, m[1])
			}
		}
	}
	if found < 3 {
		t.Fatalf("helpers.go y dashboard.go solo pasan %d textos por nx(): ¿se perdió el envoltorio?", found)
	}
}
