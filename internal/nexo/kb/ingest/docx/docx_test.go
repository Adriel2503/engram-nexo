package docx

// Nexo IA · NX-10 — pruebas del conversor .docx con documentos armados en la
// prueba (archive/zip), sin binarios externos.

import (
	"archive/zip"
	"bytes"
	"context"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/ingest"
)

const wns = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"`

func docXML(body string) string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:document ` + wns + `><w:body>` + body + `<w:sectPr/></w:body></w:document>`
}

func para(style, text string) string {
	ppr := ""
	if style != "" {
		ppr = `<w:pPr><w:pStyle w:val="` + style + `"/></w:pPr>`
	}
	return `<w:p>` + ppr + `<w:r><w:t xml:space="preserve">` + text + `</w:t></w:r></w:p>`
}

func item(numID, ilvl, text string) string {
	return `<w:p><w:pPr><w:pStyle w:val="ListParagraph"/><w:numPr><w:ilvl w:val="` + ilvl + `"/><w:numId w:val="` + numID + `"/></w:numPr></w:pPr><w:r><w:t>` + text + `</w:t></w:r></w:p>`
}

func table(rows [][]string) string {
	var b strings.Builder
	b.WriteString(`<w:tbl><w:tblPr/><w:tblGrid/>`)
	for _, r := range rows {
		b.WriteString(`<w:tr>`)
		for _, c := range r {
			b.WriteString(`<w:tc><w:tcPr/>` + para("", c) + `</w:tc>`)
		}
		b.WriteString(`</w:tr>`)
	}
	b.WriteString(`</w:tbl>`)
	return b.String()
}

const stylesXML = `<?xml version="1.0" encoding="UTF-8"?><w:styles ` + wns + `>
<w:style w:type="paragraph" w:styleId="Normal"><w:name w:val="Normal"/></w:style>
<w:style w:type="paragraph" w:styleId="Title"><w:name w:val="Title"/><w:basedOn w:val="Normal"/></w:style>
<w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/><w:basedOn w:val="Normal"/><w:pPr><w:outlineLvl w:val="0"/></w:pPr></w:style>
<w:style w:type="paragraph" w:styleId="Heading2"><w:name w:val="heading 2"/><w:basedOn w:val="Normal"/></w:style>
<w:style w:type="paragraph" w:styleId="Ttulo1"><w:name w:val="Título 1"/><w:basedOn w:val="Normal"/></w:style>
<w:style w:type="paragraph" w:styleId="Ttulo2"><w:name w:val="Título 2"/><w:basedOn w:val="Normal"/></w:style>
<w:style w:type="paragraph" w:styleId="MiTitulo"><w:name w:val="Mi titulo propio"/><w:basedOn w:val="Ttulo1"/></w:style>
<w:style w:type="paragraph" w:styleId="Resumen"><w:name w:val="Resumen"/><w:basedOn w:val="Normal"/><w:pPr><w:outlineLvl w:val="2"/></w:pPr></w:style>
<w:style w:type="paragraph" w:styleId="ListParagraph"><w:name w:val="List Paragraph"/><w:basedOn w:val="Normal"/></w:style>
</w:styles>`

const numberingXML = `<?xml version="1.0" encoding="UTF-8"?><w:numbering ` + wns + `>
<w:abstractNum w:abstractNumId="10"><w:lvl w:ilvl="0"><w:numFmt w:val="bullet"/></w:lvl><w:lvl w:ilvl="1"><w:numFmt w:val="bullet"/></w:lvl></w:abstractNum>
<w:abstractNum w:abstractNumId="20"><w:lvl w:ilvl="0"><w:numFmt w:val="decimal"/></w:lvl></w:abstractNum>
<w:num w:numId="1"><w:abstractNumId w:val="10"/></w:num>
<w:num w:numId="2"><w:abstractNumId w:val="20"/></w:num>
</w:numbering>`

func coreXML(title string) string {
	return `<?xml version="1.0" encoding="UTF-8"?><cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>` + title + `</dc:title></cp:coreProperties>`
}

const contentTypes = `<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`

func buildDocx(t testing.TB, parts map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	names := []string{"[Content_Types].xml", "word/document.xml", "word/styles.xml", "word/numbering.xml", "docProps/core.xml"}
	for _, n := range names {
		content, ok := parts[n]
		if n == "[Content_Types].xml" && !ok {
			content, ok = contentTypes, true
		}
		if !ok {
			continue
		}
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func convert(t *testing.T, data []byte) ingest.Result {
	t.Helper()
	res, err := Converter{}.Convert(context.Background(), ingest.Input{Name: "x.docx", Data: data})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	return res
}

func sampleBody() string {
	return para("Title", "Guía de Logging") +
		para("Heading1", "Objetivo") +
		`<w:p><w:r><w:t>Definir cómo</w:t></w:r><w:r><w:tab/><w:t>registrar.</w:t></w:r>` +
		`<w:del><w:r><w:delText>texto borrado</w:delText></w:r></w:del>` +
		`<w:ins><w:r><w:t xml:space="preserve"> Con trazas.</w:t></w:r></w:ins>` +
		`<w:r><w:instrText>HYPERLINK "http://x"</w:instrText></w:r>` +
		`<w:hyperlink><w:r><w:t xml:space="preserve"> Ver guía</w:t></w:r></w:hyperlink></w:p>` +
		para("", "") +
		para("Heading2", "Alcance") +
		item("1", "0", "Servicios backend") +
		item("1", "1", "Incluye workers") +
		para("", "Aplica a todos los equipos.") +
		para("Ttulo1", "Niveles") +
		table([][]string{{"Nivel", "Uso"}, {"INFO", "Eventos normales"}, {"WARN", "Algo raro | revisar"}, {"ERROR", "Fallo"}}) +
		item("2", "0", "Primero") +
		item("2", "0", "Segundo") +
		para("MiTitulo", "Anexo") +
		para("Resumen", "Resumen") +
		para("", "Texto del resumen.")
}

func TestConvertStructure(t *testing.T) {
	res := convert(t, buildDocx(t, map[string]string{
		"word/document.xml": docXML(sampleBody()), "word/styles.xml": stylesXML, "word/numbering.xml": numberingXML,
	}))
	if res.Format != "docx" || res.MIME != MIME {
		t.Fatalf("formato: %s %s", res.Format, res.MIME)
	}
	if res.Meta.Title != "Guía de Logging" {
		t.Fatalf("título desde el primer título de nivel 1: %q", res.Meta.Title)
	}
	md := res.Markdown
	for _, want := range []string{
		"# Guía de Logging\n\n# Objetivo\n\nDefinir cómo registrar. Con trazas. Ver guía\n\n## Alcance\n\n- Servicios backend\n  - Incluye workers\n\nAplica a todos los equipos.",
		"# Niveles\n\n| Nivel | Uso |\n| --- | --- |\n| INFO | Eventos normales |\n| WARN | Algo raro \\| revisar |\n| ERROR | Fallo |",
		"1. Primero\n2. Segundo",
		"# Anexo\n\n### Resumen\n\nTexto del resumen.\n",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("falta %q en:\n%s", want, md)
		}
	}
	for _, bad := range []string{"borrado", "HYPERLINK"} {
		if strings.Contains(md, bad) {
			t.Fatalf("no debe incluir %q:\n%s", bad, md)
		}
	}
	var table *ingest.Block
	for i, b := range res.Blocks {
		if b.Kind == ingest.BlockTable {
			table = &res.Blocks[i]
		}
	}
	if table == nil || table.Extraction != domain.ExtractionTable {
		t.Fatalf("bloque de tabla: %+v", res.Blocks)
	}
	if table.Location["table"] != 1 || table.Location["row_start"] != 1 || table.Location["row_end"] != 4 {
		t.Fatalf("procedencia de la tabla: %v", table.Location)
	}
	if strings.Join(table.HeadingPath, ">") != "Niveles" {
		t.Fatalf("ruta de la tabla: %v", table.HeadingPath)
	}
	// Lista de viñetas: un bloque con sus párrafos 6 y 7 (el vacío cuenta).
	for _, b := range res.Blocks {
		if strings.HasPrefix(b.Text, "- Servicios") {
			if b.Location["paragraph_start"] != 6 || b.Location["paragraph_end"] != 7 || strings.Join(b.HeadingPath, ">") != "Objetivo>Alcance" {
				t.Fatalf("lista: %v %v", b.Location, b.HeadingPath)
			}
		}
		if b.Text == "### Resumen" && (b.Level != 3 || strings.Join(b.HeadingPath, ">") != "Anexo>Resumen") {
			t.Fatalf("nivel por outlineLvl del estilo: %+v", b)
		}
	}
	secs := ingest.Section(res.Blocks, ingest.DefaultSectionerOptions)
	var anchors []string
	for _, s := range secs {
		anchors = append(anchors, s.Anchor)
	}
	if got := strings.Join(anchors, ","); got != "objetivo,objetivo.alcance,niveles,anexo.resumen" {
		t.Fatalf("anclas: %s", got)
	}
	for _, s := range secs {
		if s.Anchor == "niveles" {
			if s.Extraction != domain.ExtractionMixed || s.Location["table"] != 1 || s.Location["paragraph_start"] != 9 {
				t.Fatalf("sección niveles: %+v", s)
			}
		}
	}
}

func TestCoreTitleAndSpanishHeadings(t *testing.T) {
	body := para("Ttulo1", "Introducción") + para("", "Hola.") + para("Ttulo2", "Detalle") + para("", "Más.")
	res := convert(t, buildDocx(t, map[string]string{"word/document.xml": docXML(body), "word/styles.xml": stylesXML,
		"docProps/core.xml": coreXML("Título del documento")}))
	if res.Meta.Title != "Título del documento" {
		t.Fatalf("título de core.xml: %q", res.Meta.Title)
	}
	if !strings.Contains(res.Markdown, "# Introducción\n\nHola.\n\n## Detalle\n\nMás.") {
		t.Fatalf("markdown: %s", res.Markdown)
	}
	// Sin styles.xml: los ids de estilo bastan.
	res = convert(t, buildDocx(t, map[string]string{"word/document.xml": docXML(para("Heading3", "Solo id") + para("", "x"))}))
	if !strings.HasPrefix(res.Markdown, "### Solo id") {
		t.Fatalf("título por id sin estilos: %s", res.Markdown)
	}
}

func wantErr(t *testing.T, err error, kind domain.Kind) {
	t.Helper()
	if err == nil || !domain.IsKind(err, kind) {
		t.Fatalf("esperaba error de clase %d, got %v", kind, err)
	}
}

func TestRejectsInvalid(t *testing.T) {
	ctx := context.Background()
	_, err := Converter{}.Convert(ctx, ingest.Input{Name: "a.docx", Data: []byte("no soy un zip")})
	wantErr(t, err, domain.KindInvalid)
	_, err = Converter{}.Convert(ctx, ingest.Input{Name: "a.docx", Data: buildDocx(t, map[string]string{"word/styles.xml": stylesXML})})
	wantErr(t, err, domain.KindInvalid)
	_, err = Converter{}.Convert(ctx, ingest.Input{Name: "a.docx", Data: buildDocx(t, map[string]string{"word/document.xml": "<w:document><w:body>"})})
	if err == nil {
		t.Fatal("XML truncado debe fallar")
	}
}

func TestRejectsZipBombs(t *testing.T) {
	// 1. Tamaño declarado enorme (cabecera mentirosa).
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	raw := []byte("<x/>")
	w, err := zw.CreateRaw(&zip.FileHeader{Name: "word/document.xml", Method: zip.Store, CRC32: crc32.ChecksumIEEE(raw),
		CompressedSize64: uint64(len(raw)), UncompressedSize64: 60 << 20})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write(raw)
	_ = zw.Close()
	_, err = Converter{}.Convert(context.Background(), ingest.Input{Name: "bomba.docx", Data: buf.Bytes()})
	wantErr(t, err, domain.KindTooLarge)

	// 2. Relación de compresión excesiva (2 MB de espacios en pocos KB).
	big := docXML(para("", "a")) + strings.Repeat(" ", 2<<20)
	_, err = Converter{}.Convert(context.Background(), ingest.Input{Name: "bomba2.docx", Data: buildDocx(t, map[string]string{"word/document.xml": big})})
	wantErr(t, err, domain.KindTooLarge)

	// 3. Demasiadas entradas.
	buf.Reset()
	zw = zip.NewWriter(&buf)
	for i := 0; i <= MaxEntries; i++ {
		_, _ = zw.Create("x/" + strings.Repeat("a", 3) + string(rune('a'+i%26)) + "/" + itoa(i))
	}
	_ = zw.Close()
	_, err = Converter{}.Convert(context.Background(), ingest.Input{Name: "muchas.docx", Data: buf.Bytes()})
	wantErr(t, err, domain.KindTooLarge)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func TestContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	data := buildDocx(t, map[string]string{"word/document.xml": docXML(sampleBody())})
	if _, err := (Converter{}).Convert(ctx, ingest.Input{Name: "x.docx", Data: data}); err == nil {
		t.Fatal("contexto cancelado debe fallar")
	}
}

func TestRegistryIntegration(t *testing.T) {
	reg := ingest.DefaultRegistry()
	reg.Register(Converter{})
	data := buildDocx(t, map[string]string{"word/document.xml": docXML(para("Heading1", "Uno") + para("", "Texto."))})
	res, err := reg.Convert(context.Background(), "", ingest.Input{Name: "carpeta/doc.docx", Data: data}, ingest.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Format != "docx" || res.SourceSize != int64(len(data)) || res.SourceSHA256 == "" {
		t.Fatalf("registro: %+v", res)
	}
}

// sampleDocx es el lineamiento de ejemplo usado por las pruebas E2E.
func sampleDocx(t testing.TB) []byte {
	body := para("Title", "Lineamiento de Logging") +
		para("Heading1", "Objetivo") +
		para("", "Definir cómo registran eventos los microservicios y microfrontends de la empresa para facilitar el soporte y la auditoría.") +
		para("Heading1", "Niveles de log") +
		para("", "Cada mensaje usa uno de los niveles de la tabla.") +
		table([][]string{
			{"Nivel", "Uso", "Ejemplo"},
			{"DEBUG", "Detalle para desarrollo; desactivado en producción", "Valores intermedios de un cálculo"},
			{"INFO", "Eventos normales del negocio", "Pedido 123 creado"},
			{"WARN", "Situación anómala recuperable", "Reintento de llamada a un servicio externo"},
			{"ERROR", "Fallo que requiere atención", "No se pudo guardar el pedido"},
		}) +
		para("Heading1", "Buenas prácticas") +
		item("1", "0", "Usar logging estructurado en JSON con un identificador de correlación.") +
		item("1", "0", "Registrar el inicio y el fin de cada operación relevante.") +
		item("1", "0", "No registrar dentro de bucles de alto volumen.") +
		para("Heading1", "Datos sensibles") +
		para("", "Nunca se registran tokens de acceso, contraseñas ni datos personales de clientes. Los identificadores de cliente se enmascaran antes de escribirse en el log.")
	return buildDocx(t, map[string]string{"word/document.xml": docXML(body), "word/styles.xml": stylesXML, "word/numbering.xml": numberingXML,
		"docProps/core.xml": coreXML("Lineamiento de Logging")})
}

func TestSampleDocx(t *testing.T) {
	res := convert(t, sampleDocx(t))
	if res.Meta.Title != "Lineamiento de Logging" || !strings.Contains(res.Markdown, "| WARN | Situación anómala recuperable |") {
		t.Fatalf("ejemplo: %s", res.Markdown)
	}
	if found := ingest.DetectSecrets(res.Markdown); len(found) > 0 {
		t.Fatalf("el ejemplo no debe tener secretos: %v", found)
	}
}

// TestWriteSampleDocx escribe testdata/lineamiento-ejemplo.docx (solo con
// NEXO_KB_WRITE_SAMPLE=1).
func TestWriteSampleDocx(t *testing.T) {
	if os.Getenv("NEXO_KB_WRITE_SAMPLE") != "1" {
		t.Skip("NEXO_KB_WRITE_SAMPLE=1 para escribir testdata/lineamiento-ejemplo.docx")
	}
	if err := os.MkdirAll("testdata", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("testdata", "lineamiento-ejemplo.docx"), sampleDocx(t), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSampleFileConverts comprueba el archivo de ejemplo versionado.
func TestSampleFileConverts(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "lineamiento-ejemplo.docx"))
	if err != nil {
		t.Skip("sin testdata/lineamiento-ejemplo.docx")
	}
	res := convert(t, data)
	if len(ingest.Section(res.Blocks, ingest.DefaultSectionerOptions)) < 4 {
		t.Fatalf("secciones del ejemplo: %s", res.Markdown)
	}
}
