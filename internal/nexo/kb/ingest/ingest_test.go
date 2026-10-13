package ingest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
)

func readTestdata(t *testing.T, rel string) Input {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "lineamientos-frontend", rel))
	if err != nil {
		t.Fatal(err)
	}
	return Input{Name: rel, Data: data}
}

func convert(t *testing.T, rel string) Result {
	t.Helper()
	res, err := DefaultRegistry().Convert(context.Background(), "", readTestdata(t, rel), Limits{})
	if err != nil {
		t.Fatalf("convert %s: %v", rel, err)
	}
	return res
}

func sectionByAnchor(t *testing.T, secs []SectionDraft, anchor string) SectionDraft {
	t.Helper()
	for _, s := range secs {
		if s.Anchor == anchor {
			return s
		}
	}
	var got []string
	for _, s := range secs {
		got = append(got, s.Anchor)
	}
	t.Fatalf("no hay sección %q; anclas: %v", anchor, got)
	return SectionDraft{}
}

func TestMarkdownFrontMatterAndBlocks(t *testing.T) {
	res := convert(t, "frontend/formularios.md")
	if res.Format != domain.FormatMarkdown || res.MIME != "text/markdown" {
		t.Fatalf("formato %s %s", res.Format, res.MIME)
	}
	m := res.Meta
	if m.ID != "frontend/formularios" || m.Title != "Formularios en aplicaciones web" || m.Category != "lineamiento" || m.Owner != "ana" {
		t.Fatalf("front matter: %+v", m)
	}
	if strings.Join(m.Tags, ",") != "frontend,formularios,accesibilidad" {
		t.Fatalf("tags %v", m.Tags)
	}
	if m.ValidFrom == nil || m.ValidFrom.Format("2006-01-02") != "2026-09-01" {
		t.Fatalf("valid_from %v", m.ValidFrom)
	}
	if strings.HasPrefix(res.Markdown, "---") || !strings.HasPrefix(res.Markdown, "# Formularios") {
		t.Fatalf("el Markdown normalizado no debe llevar front matter: %q", res.Markdown[:40])
	}
	if res.SourceSHA256 == "" || res.SourceSize == 0 {
		t.Fatal("falta hash o tamaño del original")
	}
	for _, b := range res.Blocks {
		if b.Kind == BlockHeading && strings.Contains(b.Text, "Esto no es un título") {
			t.Fatal("un # dentro de un bloque de código no es un título")
		}
	}
}

func TestSectionerHeadingsTablesCodeAndAnchors(t *testing.T) {
	res := convert(t, "frontend/formularios.md")
	secs := Section(res.Blocks, DefaultSectionerOptions)

	// La sección de mensajes tiene la ruta completa de títulos.
	msg := sectionByAnchor(t, secs, "formularios-en-aplicaciones-web.validacion.mensajes-de-error")
	if strings.Join(msg.HeadingPath, " > ") != "Formularios en aplicaciones web > Validación > Mensajes de error" {
		t.Fatalf("ruta %v", msg.HeadingPath)
	}
	if !strings.HasPrefix(msg.Body, "### Mensajes de error") {
		t.Fatalf("el cuerpo empieza con su título: %q", msg.Body)
	}

	// El bloque de código queda entero y la sección se marca como mixta.
	lib := sectionByAnchor(t, secs, "formularios-en-aplicaciones-web.validacion.libreria")
	if !strings.Contains(lib.Body, "```tsx") || !strings.Contains(lib.Body, "useForm({ resolver: zodResolver(schema) });\n}\n```") {
		t.Fatalf("bloque de código partido: %q", lib.Body)
	}
	if lib.Extraction != domain.ExtractionMixed {
		t.Fatalf("extracción %s", lib.Extraction)
	}

	// La tabla queda entera en una sección, aun con un tamaño objetivo mínimo.
	tiny := Section(res.Blocks, SectionerOptions{TargetTokens: 5})
	found := false
	for _, s := range tiny {
		if strings.Contains(s.Body, "| Correo |") {
			found = true
			if !strings.Contains(s.Body, "| Fecha de nacimiento |") {
				t.Fatalf("tabla partida: %q", s.Body)
			}
			if s.Extraction != domain.ExtractionTable {
				t.Fatalf("una sección solo con la tabla es 'table', got %s", s.Extraction)
			}
		}
	}
	if !found {
		t.Fatal("no aparece la tabla")
	}

	// Títulos repetidos: anclas únicas y estables.
	sectionByAnchor(t, secs, "formularios-en-aplicaciones-web.ejemplo")
	sectionByAnchor(t, secs, "formularios-en-aplicaciones-web.ejemplo-2")
	seen := map[string]bool{}
	for _, s := range tiny {
		if seen[s.Anchor] {
			t.Fatalf("ancla duplicada %s", s.Anchor)
		}
		seen[s.Anchor] = true
	}

	// "Validación" solo tiene un párrafo propio: se emite; el H1 tiene
	// párrafo introductorio: se emite.
	sectionByAnchor(t, secs, "formularios-en-aplicaciones-web")
	sectionByAnchor(t, secs, "formularios-en-aplicaciones-web.validacion")

	// Reconvertir da las mismas anclas (estables).
	again := Section(convert(t, "frontend/formularios.md").Blocks, DefaultSectionerOptions)
	if len(again) != len(secs) {
		t.Fatal("el corte no es determinista")
	}
	for i := range secs {
		if secs[i].Anchor != again[i].Anchor || secs[i].Body != again[i].Body {
			t.Fatalf("sección %d distinta al reconvertir", i)
		}
	}
}

func TestSectionerSourceLocation(t *testing.T) {
	res := convert(t, "frontend/formularios.md")
	secs := Section(res.Blocks, DefaultSectionerOptions)
	tbl := sectionByAnchor(t, secs, "formularios-en-aplicaciones-web.campos-obligatorios")
	loc := tbl.Location
	// Las líneas son del archivo original (incluido el front matter).
	src := strings.Split(string(readTestdata(t, "frontend/formularios.md").Data), "\n")
	start, end := loc["line_start"].(int), loc["line_end"].(int)
	if !strings.HasPrefix(src[start-1], "## Campos obligatorios") {
		t.Fatalf("line_start=%d apunta a %q", start, src[start-1])
	}
	if !strings.HasPrefix(src[end-1], "Los campos obligatorios se marcan") {
		t.Fatalf("line_end=%d apunta a %q", end, src[end-1])
	}
	hp, _ := loc["heading_path"].([]string)
	if len(hp) != 2 || hp[1] != "Campos obligatorios" {
		t.Fatalf("heading_path en la procedencia: %v", loc["heading_path"])
	}
}

func TestSetextHeadingsAndCommaTags(t *testing.T) {
	res := convert(t, "frontend/componentes.md")
	if strings.Join(res.Meta.Tags, "|") != "frontend|componentes" || res.Meta.Category != "catalogo" {
		t.Fatalf("meta %+v", res.Meta)
	}
	secs := Section(res.Blocks, DefaultSectionerOptions)
	sectionByAnchor(t, secs, "componentes-del-sistema-de-diseno.botones")
	sectionByAnchor(t, secs, "componentes-del-sistema-de-diseno.tablas-de-datos")
}

func TestTextConverter(t *testing.T) {
	res := convert(t, "glosario.txt")
	if res.Format != domain.FormatText || res.Meta.Title != "Glosario de frontend" {
		t.Fatalf("txt %+v", res.Meta)
	}
	secs := Section(res.Blocks, DefaultSectionerOptions)
	if len(secs) != 1 || secs[0].Anchor != "intro" || !strings.Contains(secs[0].Body, "Design token") {
		t.Fatalf("secciones de texto: %+v", secs)
	}
	if secs[0].Location["line_start"] != 1 || secs[0].Location["line_end"] != 7 {
		t.Fatalf("procedencia %v", secs[0].Location)
	}
	// Con tamaño objetivo chico, el texto se parte por párrafos con sufijo -pN.
	parts := Section(res.Blocks, SectionerOptions{TargetTokens: 10})
	if len(parts) < 3 || parts[1].Anchor != "intro-p2" {
		t.Fatalf("partes %d %v", len(parts), parts)
	}
}

func TestMergeLocationsRecordsRanges(t *testing.T) {
	loc := MergeLocations([]domain.SourceLocation{
		{"page_start": 3, "page_end": 3},
		{"page_start": 4, "page_end": 5},
		{"sheet": "Componentes", "row_start": 40, "row_end": 50},
		{"sheet": "Tokens", "row_start": 1, "row_end": 10},
	})
	if loc["page_start"] != 3 || loc["page_end"] != 5 || loc["row_start"] != 1 || loc["row_end"] != 50 {
		t.Fatalf("rangos %v", loc)
	}
	if sheets, ok := loc["sheets"].([]any); !ok || len(sheets) != 2 {
		t.Fatalf("una mezcla de hojas debe registrarse: %v", loc)
	}
	// El Sectioner no mezcla hojas: cambia de sección.
	blocks := []Block{
		{Kind: BlockTable, Text: "| a |\n|---|\n| 1 |", Location: domain.SourceLocation{"sheet": "A", "row_start": 1, "row_end": 2}, Extraction: domain.ExtractionTable},
		{Kind: BlockTable, Text: "| b |\n|---|\n| 2 |", Location: domain.SourceLocation{"sheet": "B", "row_start": 1, "row_end": 2}, Extraction: domain.ExtractionTable},
	}
	secs := Section(blocks, DefaultSectionerOptions)
	if len(secs) != 2 || secs[0].Location["sheet"] != "A" || secs[1].Location["sheet"] != "B" {
		t.Fatalf("hojas mezcladas: %+v", secs)
	}
}

type slowConverter struct{}

func (slowConverter) Format() string       { return "slow" }
func (slowConverter) Extensions() []string { return []string{".slow"} }
func (slowConverter) MIMETypes() []string  { return []string{"application/x-slow"} }
func (slowConverter) Convert(ctx context.Context, in Input) (Result, error) {
	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	case <-time.After(5 * time.Second):
		return Result{Markdown: strings.Repeat("x", 100)}, nil
	}
}

func TestRegistryLimitsAndLookup(t *testing.T) {
	r := DefaultRegistry()
	r.Register(slowConverter{})
	ctx := context.Background()

	if _, err := r.Convert(ctx, "", Input{Name: "a.md", Data: make([]byte, 101)}, Limits{MaxInputBytes: 100}); !domain.IsKind(err, domain.KindTooLarge) {
		t.Fatalf("entrada grande: %v", err)
	}
	if _, err := r.Convert(ctx, "", Input{Name: "a.slow", Data: []byte("x")}, Limits{Timeout: 50 * time.Millisecond}); err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("tiempo máximo: %v", err)
	}
	if _, err := r.Convert(ctx, "", Input{Name: "a.md", Data: []byte(strings.Repeat("palabra ", 100))}, Limits{MaxOutputBytes: 50}); !domain.IsKind(err, domain.KindTooLarge) {
		t.Fatalf("salida grande: %v", err)
	}
	if _, err := r.Convert(ctx, "", Input{Name: "informe.pdf", Data: []byte("%PDF")}, Limits{}); !domain.IsKind(err, domain.KindInvalid) {
		t.Fatalf("formato no soportado: %v", err)
	}
	// MIME tiene prioridad sobre la extensión.
	res, err := r.Convert(ctx, "", Input{Name: "nota.bin", MIME: "text/markdown; charset=utf-8", Data: []byte("# Hola\n\ntexto")}, Limits{})
	if err != nil || res.Format != domain.FormatMarkdown {
		t.Fatalf("por MIME: %v %s", err, res.Format)
	}
	if c, ok := r.Lookup("", "", `docs\guia.MD`); !ok || c.Format() != "md" {
		t.Fatal("extensión en mayúsculas y ruta de Windows")
	}
}

func TestDetectSecrets(t *testing.T) {
	bad := []string{
		"-----BEGIN RSA PRIVATE KEY-----",
		"aws: AKIAIOSFODNN7EXAMPLE",
		"token ghp_" + strings.Repeat("a", 36),
		"DATABASE_URL=postgres://app:S3cr3t0!@db.interno:5432/ventas",
		"clave sk-ant-" + strings.Repeat("b", 40),
	}
	for _, s := range bad {
		if len(DetectSecrets(s)) == 0 {
			t.Errorf("no detectó secreto en %q", s)
		}
	}
	good := []string{
		"La contraseña debe tener 12 caracteres y no se guarda en el repositorio.",
		"DATABASE_URL=postgres://usuario:<password>@host:5432/db",
		"Use postgres://app:${DB_PASSWORD}@db:5432/app",
		"Clave de ejemplo: sk-xxxx",
	}
	for _, s := range good {
		if f := DetectSecrets(s); len(f) != 0 {
			t.Errorf("falso positivo en %q: %v", s, f)
		}
	}
	res := convert(t, "frontend/formularios.md")
	if f := DetectSecrets(res.Markdown); len(f) != 0 {
		t.Fatalf("el lineamiento real no tiene secretos: %v", f)
	}
}

func TestSlugAndFold(t *testing.T) {
	cases := map[string]string{
		"Validación":                 "validacion",
		"Diseño y Niñez — Ñandú":     "diseno-y-ninez-nandu",
		"  ¿Cómo  usar `useForm`?  ": "como-usar-useform",
		"!!!":                        "seccion",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
	if Fold("acción pingüino") != "accion pinguino" {
		t.Fatal(Fold("acción pingüino"))
	}
}

func TestFrontMatterInvalidAndDates(t *testing.T) {
	_, err := DefaultRegistry().Convert(context.Background(), "", Input{Name: "x.md", Data: []byte("---\ntitle: [a\n---\n# X\n")}, Limits{})
	if !domain.IsKind(err, domain.KindInvalid) {
		t.Fatalf("YAML inválido: %v", err)
	}
	_, err = DefaultRegistry().Convert(context.Background(), "", Input{Name: "x.md", Data: []byte("---\nvalid_until: mañana\n---\n# X\n")}, Limits{})
	if !domain.IsKind(err, domain.KindInvalid) {
		t.Fatalf("fecha inválida: %v", err)
	}
	// '---' sin cierre no es front matter.
	res, err := DefaultRegistry().Convert(context.Background(), "", Input{Name: "x.md", Data: []byte("---\n# Título\n\nTexto")}, Limits{})
	if err != nil || res.Meta.Title != "Título" {
		t.Fatalf("sin cierre: %v %+v", err, res.Meta)
	}
}
