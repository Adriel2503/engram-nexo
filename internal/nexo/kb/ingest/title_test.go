package ingest

import (
	"context"
	"reflect"
	"testing"
)

// Regresión: un título como "trace_id" se mostraba "traceid" porque la
// limpieza del título quitaba todo '_', '*' y '`'. El título visible conserva
// el '_' entre letras y el texto de los spans de código; el ancla no cambia.
func TestCleanInlineKeepsIdentifiers(t *testing.T) {
	cases := map[string]string{
		"trace_id":                   "trace_id",
		"`trace_id`":                 "trace_id",
		"Campo `span_id` del log":    "Campo span_id del log",
		"*Énfasis* y **negrita**":    "Énfasis y negrita",
		"_cursiva_ y __negrita__":    "cursiva y negrita",
		"_trace_id_":                 "trace_id",
		"Uso de `*.md` y snake_case": "Uso de *.md y snake_case",
		"Sin cierre ` aquí":          "Sin cierre  aquí",
		"MAX__VALUE":                 "MAX__VALUE",
	}
	for in, want := range cases {
		if got := cleanInline(in); got != want {
			t.Errorf("cleanInline(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMarkdownTitleKeepsUnderscoreAnchorStable(t *testing.T) {
	src := "# trace_id\n\nIdentificador de la traza.\n\n## Campo `span_id`\n\nTexto.\n\nuser_name\n---------\n\nOtro.\n"
	res, err := MarkdownConverter{}.Convert(context.Background(), Input{Name: "t.md", Data: []byte(src)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Meta.Title != "trace_id" {
		t.Fatalf("título del documento = %q, want %q", res.Meta.Title, "trace_id")
	}
	secs := Section(res.Blocks, DefaultSectionerOptions)
	type sec struct {
		anchor string
		path   []string
	}
	var got []sec
	for _, s := range secs {
		got = append(got, sec{s.Anchor, s.HeadingPath})
	}
	// Anclas iguales a las de antes del arreglo (sin '_'): no cambian las
	// anclas ya publicadas ni los enlaces que las usan.
	want := []sec{
		{"traceid", []string{"trace_id"}},
		{"traceid.campo-spanid", []string{"trace_id", "Campo span_id"}},
		{"traceid.username", []string{"trace_id", "user_name"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("secciones = %+v, want %+v", got, want)
	}
}
