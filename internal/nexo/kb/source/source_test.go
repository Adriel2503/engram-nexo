package source_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/ingest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/source"
)

func TestCleanPathRejectsTraversal(t *testing.T) {
	ok := map[string]string{
		"a/b.md":            "a/b.md",
		`docs\guia.md`:      "docs/guia.md",
		"./x/./y.md":        "x/y.md",
		"Guía de Estilo.md": "Guía de Estilo.md",
	}
	for in, want := range ok {
		if got := source.CleanPath(in); got != want {
			t.Errorf("CleanPath(%q) = %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"../etc/passwd", "a/../../b.md", "/abs/x.md", `C:\x.md`, "a\x00b.md", "", "a/\nb.md"} {
		if got := source.CleanPath(bad); got != "" {
			t.Errorf("CleanPath(%q) = %q, want rechazo", bad, got)
		}
	}
}

func TestDocKeyAndTitleFromWikiNames(t *testing.T) {
	if k := source.DocKeyFromPath("Lineamientos/≤-2022/Guía-de-Inicio.md"); k != "lineamientos/2022/guia-de-inicio" || !domain.ValidDocKey(k) {
		t.Fatalf("doc_key: %q", k)
	}
	if k := source.DocKeyFromPath("Lineamientos/[TO_REMOVE]/Librerías-%7C-Componentes.md"); k != "lineamientos/to-remove/librerias-componentes" {
		t.Fatalf("doc_key: %q", k)
	}
	if got := source.TitleFromName("AWS-Infrastructure-%2D-Next.js-+-ISR.md"); got != "AWS Infrastructure - Next.js + ISR" {
		t.Fatalf("título: %q", got)
	}
	if got := source.TitleFromName("Librerías-%7C-Componentes"); got != "Librerías | Componentes" {
		t.Fatalf("título: %q", got)
	}
	long := strings.Repeat("segmento-largo/", 30) + "x.md"
	if k := source.DocKeyFromPath(long); len(k) > 200 || !domain.ValidDocKey(k) {
		t.Fatalf("doc_key largo: %d %q", len(k), k)
	}
}

func TestSniff(t *testing.T) {
	if source.Sniff("a.md", []byte("# hola\n")) != "" || source.Sniff("a.docx", []byte("PK\x03\x04...")) != "" {
		t.Fatal("contenido válido rechazado")
	}
	if source.Sniff("a.md", []byte{0x89, 'P', 'N', 'G', 0, 0}) == "" || source.Sniff("a.docx", []byte("# no soy docx")) == "" {
		t.Fatal("tipo por contenido")
	}
}

func wiki() []source.File {
	return []source.File{
		{Path: ".order", Data: []byte("Lineamientos\n")},
		{Path: "Lineamientos.md", Data: []byte("[[_TOC_]]\n\n")},
		{Path: "Lineamientos/.order", Data: []byte("Guía-de-Inicio\nDesarrollo\n≤-2022\n[TO_REMOVE]\n")},
		{Path: "Lineamientos/Guía-de-Inicio.md", Data: []byte("[[_TOC_]]\n# 1. Objetivo\nTexto.\n")},
		{Path: "Lineamientos/Desarrollo.md", Data: []byte("# Desarrollo\nIndice de desarrollo.\n")},
		{Path: "Lineamientos/Desarrollo/Buenas-Prácticas.md", Data: []byte("# Nomenclatura\n\n<table><tr><th>Objeto</th><th>Regla</th></tr><tr><td>Tabla</td><td>singular</td></tr></table>\n\n::: mermaid\ngraph LR;\nA-->B\n:::\n")},
		{Path: "Lineamientos/≤-2022/Docs-Java.md", Data: []byte("# Java\nLegado.\n")},
		{Path: "Lineamientos/[TO_REMOVE]/Viejo.md", Data: []byte("# Viejo\nObsoleto.\n")},
		{Path: "Lineamientos/[TO_TIDY]/Ordenar.md", Data: []byte("# Ordenar\nPendiente.\n")},
		{Path: ".attachments/diagrama.png"},
		{Path: ".attachments/fuentes.zip"},
	}
}

func TestADOWikiSource(t *testing.T) {
	files := wiki()
	if source.Detect(files) != source.KindADO {
		t.Fatal("detección por .order")
	}
	res, err := source.Walk(context.Background(), files, source.Options{WikiURL: "https://dev.azure.com/rp/arq/_wiki/wikis/W"})
	if err != nil {
		t.Fatal(err)
	}
	docs := map[string]source.Doc{}
	for _, d := range res.Docs {
		docs[d.DocKey] = d
	}
	if _, ok := docs["lineamientos"]; ok {
		t.Fatal("la página vacía (solo [[_TOC_]]) se salta")
	}
	g := docs["lineamientos/guia-de-inicio"]
	if g.Title != "Guía de Inicio" || strings.Join(g.PathTitles, "/") != "Lineamientos" || g.Order != 1 || g.Format != ingest.FormatADOMarkdown {
		t.Fatalf("página: %+v", g)
	}
	if !strings.Contains(g.SourceURL, "pagePath=%2FLineamientos%2FGu%C3%ADa+de+Inicio") {
		t.Fatalf("enlace: %s", g.SourceURL)
	}
	if d := docs["lineamientos/to-remove/viejo"]; !d.Hint.Retire {
		t.Fatalf("[TO_REMOVE] retirado: %+v", d.Hint)
	}
	if d := docs["lineamientos/2022/docs-java"]; d.Hint.ValidUntil == nil || d.Hint.ValidUntil.Year() != 2023 || d.Hint.Retire {
		t.Fatalf("≤-2022 vencido: %+v", d.Hint)
	}
	if d := docs["lineamientos/to-tidy/ordenar"]; d.Hint.Retire || len(d.Hint.Tags) != 1 {
		t.Fatalf("[TO_TIDY] normal con etiqueta: %+v", d.Hint)
	}
	var notes []string
	for _, s := range res.Skipped {
		notes = append(notes, s.Path+"="+s.Reason)
	}
	all := strings.Join(notes, ";")
	if !strings.Contains(all, "Lineamientos.md=empty wiki page") || !strings.Contains(all, ".attachments/*.png=1 files") {
		t.Fatalf("avisos: %s", all)
	}
	// El contenido pasa por el conversor de la wiki: tabla HTML y mermaid.
	reg := ingest.DefaultRegistry()
	bp := docs["lineamientos/desarrollo/buenas-practicas"]
	conv, err := reg.Convert(context.Background(), bp.Format, ingest.Input{Name: bp.Path, Data: bp.Data}, ingest.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(conv.Markdown, "| Objeto | Regla |") || !strings.Contains(conv.Markdown, "```mermaid") || strings.Contains(conv.Markdown, "<table") {
		t.Fatalf("markdown: %s", conv.Markdown)
	}
}

func TestDirSource(t *testing.T) {
	files := []source.File{
		{Path: "guias/Guía de Estilo.md", Data: []byte("# Estilo\nx\n")},
		{Path: "guias/datos.bin", Data: []byte{0, 1}},
		{Path: "falso.md", Data: []byte{0xff, 0x00}},
	}
	res, err := source.Walk(context.Background(), files, source.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != source.KindDir || len(res.Docs) != 1 || res.Docs[0].DocKey != "guias/guia-de-estilo" || res.Docs[0].PathTitles[0] != "guias" {
		t.Fatalf("dir: %+v", res)
	}
	if len(res.Skipped) != 2 {
		t.Fatalf("avisos: %+v", res.Skipped)
	}
}

func makeZip(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	_ = zw.Close()
	return buf.Bytes()
}

func TestReadZipDefenses(t *testing.T) {
	idx := func(n string) bool { return strings.HasSuffix(n, ".md") }
	data := makeZip(t, map[string]string{"wiki/a.md": "# A\n", "wiki/../../evil.md": "# x\n", "wiki/img.png": "png", "wiki/.order": "a\n"})
	files, skipped, err := source.ReadZip(context.Background(), bytes.NewReader(data), int64(len(data)), source.ZipLimits{IndexableSuffix: idx})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, f := range files {
		got[f.Path] = f.Data != nil
	}
	if !got["a.md"] || got["img.png"] || !got[".order"] {
		t.Fatalf("archivos: %v (raíz común quitada, png sin descomprimir)", got)
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0].Reason, "invalid path") {
		t.Fatalf("zip-slip: %+v", skipped)
	}
	// Bomba: 5 MB de ceros comprimen a casi nada.
	bomb := makeZip(t, map[string]string{"b.md": strings.Repeat("0", 5<<20)})
	if _, _, err := source.ReadZip(context.Background(), bytes.NewReader(bomb), int64(len(bomb)), source.ZipLimits{IndexableSuffix: idx}); !errors.Is(err, source.ErrZipBomb) {
		t.Fatalf("zip bomb: %v", err)
	}
	// Tope total descomprimido.
	big := makeZip(t, map[string]string{"a.md": strings.Repeat("abcdefghij", 1000), "b.md": strings.Repeat("klmnopqrst", 1000)})
	if _, _, err := source.ReadZip(context.Background(), bytes.NewReader(big), int64(len(big)), source.ZipLimits{IndexableSuffix: idx, MaxTotalBytes: 15000, MaxRatio: 1000}); !domain.IsKind(err, domain.KindTooLarge) {
		t.Fatalf("tope total: %v", err)
	}
	// Demasiadas entradas.
	if _, _, err := source.ReadZip(context.Background(), bytes.NewReader(data), int64(len(data)), source.ZipLimits{MaxEntries: 2}); !domain.IsKind(err, domain.KindTooLarge) {
		t.Fatalf("entradas: %v", err)
	}
	if _, _, err := source.ReadZip(context.Background(), bytes.NewReader([]byte("no zip")), 6, source.ZipLimits{}); !domain.IsKind(err, domain.KindInvalid) {
		t.Fatalf("no es zip: %v", err)
	}
}
