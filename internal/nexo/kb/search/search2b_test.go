package search

import (
	"strings"
	"testing"
)

func TestExpandSynonyms(t *testing.T) {
	syn := NewSynonyms(BaseSynonyms, [][]string{{"SSR", "Server Side Rendering"}})
	got := ExpandSynonyms("¿Cómo mido el coverage de las pruebas unitarias?", syn)
	for _, want := range []string{"cobertura", `"unit test"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("expansión %q sin %q", got, want)
		}
	}
	if ExpandSynonyms("nada que expandir aqui", syn) != "" {
		t.Fatal("sin sinónimos debe quedar vacío")
	}
	if !strings.Contains(ExpandSynonyms("qué es SSR", syn), `"server side rendering"`) {
		t.Fatal("sigla del glosario")
	}
}

func TestParseGlossary(t *testing.T) {
	groups := ParseGlossary("# Glosario\n\n- **BFF** (Backend For Frontend): capa intermedia.\nSSR: Server Side Rendering, renderizado en el servidor.\nmicrofrontend = MFE\nTexto normal sin definición.\n")
	if len(groups) != 3 {
		t.Fatalf("grupos: %v", groups)
	}
	syn := NewSynonyms(groups)
	if len(syn["bff"]) == 0 || len(syn["ssr"]) == 0 || len(syn["mfe"]) == 0 {
		t.Fatalf("diccionario: %v", syn)
	}
}

func TestParseExact(t *testing.T) {
	p, err := ParseExact("TryCommand")
	if err != nil || p.Re != nil || p.Literal != "TryCommand" {
		t.Fatalf("literal: %+v %v", p, err)
	}
	p, err = ParseExact("/Polly.*Retry/")
	if err != nil || p.Re == nil || p.Literal != "Polly" || !p.Re.MatchString("usar polly con retry") {
		t.Fatalf("regex: %+v %v", p, err)
	}
	for _, bad := range []string{"/a.*/", "/(x/", "a", "/" + strings.Repeat("a", 300) + "/"} {
		if _, err := ParseExact(bad); err == nil {
			t.Errorf("debía rechazar %q", bad)
		}
	}
	if s := snippet("uno dos tres cuatro", 4, 7); s != "uno **dos** tres cuatro" {
		t.Fatalf("snippet: %q", s)
	}
}

func TestQuote(t *testing.T) {
	q := Quote("## Titulo\n" + strings.Repeat("palabra ", 100))
	if len([]rune(q)) > QuoteMax || strings.HasPrefix(q, "##") || !strings.HasSuffix(q, "...") {
		t.Fatalf("cita: %d %q", len(q), q[:20])
	}
	if Quote("# T\ncorto") != "corto" {
		t.Fatal("cita corta")
	}
}

func TestSectionPath(t *testing.T) {
	if p := SectionPath("arq", "a/b c", "x.y", 2); p != "/dashboard/kb/spaces/arq/doc?doc=a%2Fb+c&v=2#x.y" {
		t.Fatalf("ruta: %s", p)
	}
}
