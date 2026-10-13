package ingest

// Pruebas de la entrega 2b: conversor de la wiki de Azure DevOps, tablas HTML,
// tablas partidas por filas (RC-04) y anclas sin numeración (IN-08).

import (
	"context"
	"strings"
	"testing"
)

func TestPreprocessADOMarkdownKeepsLines(t *testing.T) {
	in := "[[_TOC_]]\n# Titulo\n::: mermaid\ngraph LR;\nA-->B\n:::\nfin\n```\n[[_TOC_]] dentro de código\n```\n"
	out := PreprocessADOMarkdown(in)
	if strings.Contains(out, "[[_TOC_]]\n# Titulo") || !strings.Contains(out, "```mermaid\ngraph LR;") || !strings.Contains(out, "[[_TOC_]] dentro de código") {
		t.Fatalf("preproceso:\n%s", out)
	}
	if strings.Count(out, "\n") != strings.Count(in, "\n") {
		t.Fatalf("las líneas deben conservarse: %d vs %d", strings.Count(out, "\n"), strings.Count(in, "\n"))
	}
}

func TestHTMLTablesToMarkdown(t *testing.T) {
	md := HTMLTablesToMarkdown(`<table><thead><tr><th colspan="2">Grupo</th></tr><tr><th>A</th><th>B|C</th></tr></thead>
<tbody><tr><td>uno<br>dos</td><td><script>ignorar()</script>tres</td></tr></tbody></table>`)
	want := "| Grupo A | Grupo B\\|C |\n| --- | --- |\n| uno dos | tres |"
	if md != want {
		t.Fatalf("tabla:\n%s\nwant\n%s", md, want)
	}
}

func TestADOConverterEndToEnd(t *testing.T) {
	src := "[[_TOC_]]\n# 1. Nomenclatura\n\n<table>\n<tr>\n<th>Objeto</th>\n<th>Regla</th>\n</tr>\n<tr>\n<td>Tabla</td>\n<td>singular</td>\n</tr>\n</table>\n\n## 1.2. Columnas\nsnake_case.\n"
	res, err := DefaultRegistry().Convert(context.Background(), FormatADOMarkdown, Input{Name: "x.md", Data: []byte(src)}, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Format != FormatADOMarkdown {
		t.Fatalf("formato %s", res.Format)
	}
	secs := Section(res.Blocks, DefaultSectionerOptions)
	if len(secs) != 2 || secs[0].Anchor != "nomenclatura" || secs[1].Anchor != "nomenclatura.columnas" {
		t.Fatalf("anclas sin numeración: %+v", secs)
	}
	if secs[0].HeadingPath[0] != "1. Nomenclatura" || secs[0].Extraction != "mixed" && secs[0].Extraction != "table" {
		t.Fatalf("título con numeración y tabla: %+v", secs[0])
	}
	// La línea de "## 1.2. Columnas" sigue siendo la 15 del archivo original.
	if ls := secs[1].Location["line_start"]; ls != 15 {
		t.Fatalf("procedencia de líneas: %v", secs[1].Location)
	}
}

func TestSplitTableRowsRepeatsHeader(t *testing.T) {
	var b strings.Builder
	b.WriteString("| Nivel | Uso |\n|---|---|\n")
	for i := 0; i < 40; i++ {
		b.WriteString("| fila" + strings.Repeat(" palabra", 10) + " | texto de uso de la fila |\n")
	}
	text := strings.TrimSuffix(b.String(), "\n")
	blk := Block{Kind: BlockTable, Text: text, HeadingPath: []string{"Niveles"}, Extraction: "table",
		Location: map[string]any{"heading_path": []string{"Niveles"}, "line_start": 10, "line_end": 51}}
	parts := SplitTableRows(blk, 120)
	if len(parts) < 3 {
		t.Fatalf("partes: %d", len(parts))
	}
	for i, p := range parts {
		if !strings.HasPrefix(p.Text, "| Nivel | Uso |\n|---|---|\n| fila") {
			t.Fatalf("parte %d sin cabecera: %q", i, p.Text[:40])
		}
		if CountTokens(p.Text) > 120 {
			t.Fatalf("parte %d supera el objetivo", i)
		}
	}
	if parts[0].Location["line_start"] != 12 || parts[len(parts)-1].Location["line_end"] != 51 {
		t.Fatalf("líneas: %v .. %v", parts[0].Location, parts[len(parts)-1].Location)
	}
	secs := Section([]Block{{Kind: BlockHeading, Level: 1, Text: "# Niveles", HeadingPath: []string{"Niveles"}, Extraction: "text",
		Location: map[string]any{"line_start": 9, "line_end": 9}}, blk}, SectionerOptions{TargetTokens: 120, SplitTables: true})
	if len(secs) < 3 || secs[0].Anchor != "niveles" || secs[1].Anchor != "niveles-p2" {
		t.Fatalf("secciones: %d %s %s", len(secs), secs[0].Anchor, secs[1].Anchor)
	}
	// Sin la opción, la tabla queda entera (comportamiento de 2a).
	if secs := Section([]Block{blk}, SectionerOptions{TargetTokens: 120}); len(secs) != 1 {
		t.Fatalf("sin split_tables: %d", len(secs))
	}
}

func TestStripNumbering(t *testing.T) {
	for in, want := range map[string]string{"1. Intro": "Intro", "1.2.1. Tablas y columnas": "Tablas y columnas", "3) Uso": "Uso", "2023 reglas": "2023 reglas", "10": "10"} {
		if got := stripNumbering(in); got != want {
			t.Errorf("stripNumbering(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDetectSecretsSG09(t *testing.T) {
	cases := map[string]string{
		"Server=db;Database=x;User Id=app;Password=Sup3rS3creto;":    "connection_string_password",
		"Data Source=x;Pwd=Abc12345":                                 "connection_string_password",
		"Authorization: Bearer abcdefghijklmnopqrstuvwxyz0123456789": "bearer_token",
		"api-key: 0123456789abcdef0123":                              "api_key_header",
	}
	for text, kind := range cases {
		got := DetectSecrets(text)
		if len(got) == 0 || got[0].Kind != kind {
			t.Errorf("%q: %+v", text, got)
		}
	}
	for _, ok := range []string{"Password=<tu-clave>", "Pwd=${DB_PASSWORD}", "Bearer <token>", "api-key: <clave-del-servicio>", "la contraseña se guarda en Secret Manager"} {
		if got := DetectSecrets(ok); len(got) != 0 {
			t.Errorf("marcador o texto normal %q detectado: %+v", ok, got)
		}
	}
}
