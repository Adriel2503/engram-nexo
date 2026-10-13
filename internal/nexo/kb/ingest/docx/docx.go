// Nexo IA · NX-10 (entrega 2b, tanda 1) — conversor de Word (.docx) a
// Markdown normalizado con bloques y procedencia (design §15.1 y §17.7).
//
// Go puro: archive/zip + encoding/xml, sin CGO ni dependencias nuevas. Lee
// word/document.xml, word/styles.xml (nombre, nivel de esquema y basedOn de
// cada estilo), word/numbering.xml (viñeta o numeración por numId e ilvl) y
// docProps/core.xml (título). Nunca ejecuta ni descarga nada: las relaciones
// externas, imágenes y campos se ignoran.
//
// Procedencia por bloque (domain.SourceLocation):
//
//	párrafos, títulos y listas: heading_path, paragraph_start, paragraph_end
//	                            (ordinal 1..n de los párrafos del cuerpo)
//	tablas:                     heading_path, table (ordinal 1..n de las
//	                            tablas del cuerpo), row_start, row_end
//
// Límites de seguridad (además de los de ingest.Registry): como máximo
// MaxEntries entradas en el zip, MaxUncompressed bytes leídos en total entre
// las partes XML, ninguna parte declarada mayor que MaxUncompressed y ninguna
// mayor que 1 MB con una relación de compresión superior a MaxRatio (zip
// bomb).
package docx

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/ingest"
)

// MIME es el tipo MIME de un documento de Word.
const MIME = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"

// Format es el formato registrado (source_format de la versión).
const Format = "docx"

// Límites de seguridad.
const (
	MaxEntries            = 2000
	MaxUncompressed int64 = 50 << 20
	MaxRatio              = 200
	ratioMinSize          = 1 << 20
	maxNodes              = 4_000_000
)

// Converter convierte .docx a Markdown (implementa ingest.Converter).
type Converter struct{}

// Format implementa ingest.Converter.
func (Converter) Format() string { return Format }

// Extensions implementa ingest.Converter.
func (Converter) Extensions() []string { return []string{".docx"} }

// MIMETypes implementa ingest.Converter.
func (Converter) MIMETypes() []string { return []string{MIME} }

var _ ingest.Converter = Converter{}

// Convert implementa ingest.Converter.
func (Converter) Convert(ctx context.Context, in ingest.Input) (ingest.Result, error) {
	if err := ctx.Err(); err != nil {
		return ingest.Result{}, err
	}
	zr, err := zip.NewReader(bytes.NewReader(in.Data), int64(len(in.Data)))
	if err != nil {
		return ingest.Result{}, domain.Errorf(domain.KindInvalid, domain.CodeUnsupported, "%q is not a valid .docx file (not a zip archive)", in.Name)
	}
	if len(zr.File) > MaxEntries {
		return ingest.Result{}, domain.Errorf(domain.KindTooLarge, domain.CodeTooLarge, "%q has %d zip entries; the limit is %d", in.Name, len(zr.File), MaxEntries)
	}
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[strings.TrimPrefix(f.Name, "/")] = f
	}
	rd := &reader{ctx: ctx, budget: MaxUncompressed, name: in.Name}
	docF := files["word/document.xml"]
	if docF == nil {
		return ingest.Result{}, domain.Errorf(domain.KindInvalid, domain.CodeUnsupported, "%q is not a Word document (word/document.xml is missing)", in.Name)
	}
	doc, err := rd.parse(docF)
	if err != nil {
		return ingest.Result{}, err
	}
	c := &conv{ctx: ctx, styles: map[string]*styleInfo{}, nums: map[string]string{}, fmts: map[string]map[string]string{}}
	if f := files["word/styles.xml"]; f != nil {
		n, err := rd.parse(f)
		if err != nil {
			return ingest.Result{}, err
		}
		c.loadStyles(n)
	}
	if f := files["word/numbering.xml"]; f != nil {
		n, err := rd.parse(f)
		if err != nil {
			return ingest.Result{}, err
		}
		c.loadNumbering(n)
	}
	title := ""
	if f := files["docProps/core.xml"]; f != nil {
		n, err := rd.parse(f)
		if err != nil {
			return ingest.Result{}, err
		}
		if t := n.find("title"); t != nil {
			title = strings.Join(strings.Fields(t.text.String()), " ")
		}
	}
	body := doc.find("body")
	if body == nil {
		return ingest.Result{}, domain.Errorf(domain.KindInvalid, domain.CodeUnsupported, "%q has no document body", in.Name)
	}
	if err := c.walk(body); err != nil {
		return ingest.Result{}, err
	}
	if title == "" {
		for _, b := range c.blocks {
			if b.Kind == ingest.BlockHeading && b.Level == 1 {
				title = b.HeadingPath[len(b.HeadingPath)-1]
				break
			}
		}
	}
	parts := make([]string, len(c.blocks))
	for i, b := range c.blocks {
		parts[i] = b.Text
	}
	md := strings.Join(parts, "\n\n")
	if md != "" {
		md += "\n"
	}
	return ingest.Result{Format: Format, MIME: MIME, Markdown: md, Meta: ingest.FrontMatter{Title: title}, Blocks: c.blocks}, nil
}

// ─── Lectura del zip y árbol XML ─────────────────────────────────────────────

type reader struct {
	ctx    context.Context
	budget int64
	name   string
}

func (r *reader) tooLarge(format string, args ...any) error {
	return domain.Errorf(domain.KindTooLarge, domain.CodeTooLarge, "%q: "+format, append([]any{r.name}, args...)...)
}

func (r *reader) parse(f *zip.File) (*node, error) {
	if f.UncompressedSize64 > uint64(MaxUncompressed) {
		return nil, r.tooLarge("part %s declares %d bytes; the limit is %d", f.Name, f.UncompressedSize64, MaxUncompressed)
	}
	if f.UncompressedSize64 > ratioMinSize && (f.CompressedSize64 == 0 || f.UncompressedSize64/f.CompressedSize64 > MaxRatio) {
		return nil, r.tooLarge("part %s has a suspicious compression ratio (possible zip bomb)", f.Name)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, domain.Errorf(domain.KindInvalid, domain.CodeUnsupported, "%q: cannot open %s: %v", r.name, f.Name, err)
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, r.budget+1))
	if err != nil {
		return nil, domain.Errorf(domain.KindInvalid, domain.CodeUnsupported, "%q: cannot read %s: %v", r.name, f.Name, err)
	}
	if int64(len(data)) > r.budget {
		return nil, r.tooLarge("uncompressed XML exceeds %d bytes", MaxUncompressed)
	}
	r.budget -= int64(len(data))
	return buildTree(r.ctx, r.name, f.Name, data)
}

type node struct {
	name  string
	attrs map[string]string
	kids  []*node
	text  strings.Builder
}

func (n *node) attr(name string) string { return n.attrs[name] }

func (n *node) child(name string) *node {
	for _, k := range n.kids {
		if k.name == name {
			return k
		}
	}
	return nil
}

// find busca el primer descendiente con ese nombre (en profundidad).
func (n *node) find(name string) *node {
	for _, k := range n.kids {
		if k.name == name {
			return k
		}
		if f := k.find(name); f != nil {
			return f
		}
	}
	return nil
}

func buildTree(ctx context.Context, docName, part string, data []byte) (*node, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	root := &node{}
	stack := []*node{root}
	count := 0
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, domain.Errorf(domain.KindInvalid, domain.CodeUnsupported, "%q: %s is not valid XML: %v", docName, part, err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			count++
			if count%4096 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			if count > maxNodes {
				return nil, domain.Errorf(domain.KindTooLarge, domain.CodeTooLarge, "%q: %s has more than %d XML elements", docName, part, maxNodes)
			}
			n := &node{name: t.Name.Local}
			if len(t.Attr) > 0 {
				n.attrs = make(map[string]string, len(t.Attr))
				for _, a := range t.Attr {
					n.attrs[a.Name.Local] = a.Value
				}
			}
			parent := stack[len(stack)-1]
			parent.kids = append(parent.kids, n)
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			stack[len(stack)-1].text.Write(t)
		}
	}
	return root, nil
}

// ─── Estilos y numeración ────────────────────────────────────────────────────

type styleInfo struct {
	name, basedOn string
	outline       int // -1 sin nivel de esquema
	numID, ilvl   string
}

type conv struct {
	ctx    context.Context
	styles map[string]*styleInfo
	nums   map[string]string            // numId -> abstractNumId
	fmts   map[string]map[string]string // abstractNumId -> ilvl -> numFmt

	blocks   []ingest.Block
	stack    []headingEntry
	paraIdx  int
	tableIdx int

	list *listAcc
}

type headingEntry struct {
	level int
	title string
}

type listAcc struct {
	lines      []string
	start, end int
	counters   map[string]int
}

func (c *conv) loadStyles(root *node) {
	for _, s := range collect(root, "style") {
		id := s.attr("styleId")
		if id == "" {
			continue
		}
		si := &styleInfo{outline: -1}
		if n := s.child("name"); n != nil {
			si.name = n.attr("val")
		}
		if b := s.child("basedOn"); b != nil {
			si.basedOn = b.attr("val")
		}
		if ppr := s.child("pPr"); ppr != nil {
			if o := ppr.child("outlineLvl"); o != nil {
				if v, err := strconv.Atoi(o.attr("val")); err == nil {
					si.outline = v
				}
			}
			if np := ppr.child("numPr"); np != nil {
				if n := np.child("numId"); n != nil {
					si.numID = n.attr("val")
				}
				if l := np.child("ilvl"); l != nil {
					si.ilvl = l.attr("val")
				}
			}
		}
		c.styles[id] = si
	}
}

func (c *conv) loadNumbering(root *node) {
	for _, a := range collect(root, "abstractNum") {
		id := a.attr("abstractNumId")
		lv := map[string]string{}
		for _, l := range a.kids {
			if l.name != "lvl" {
				continue
			}
			if f := l.child("numFmt"); f != nil {
				lv[l.attr("ilvl")] = f.attr("val")
			}
		}
		c.fmts[id] = lv
	}
	for _, n := range collect(root, "num") {
		if a := n.child("abstractNumId"); a != nil {
			c.nums[n.attr("numId")] = a.attr("val")
		}
	}
}

func collect(n *node, name string) []*node {
	var out []*node
	for _, k := range n.kids {
		if k.name == name {
			out = append(out, k)
		}
		out = append(out, collect(k, name)...)
	}
	return out
}

// headingFromName reconoce estilos de título en inglés y español por nombre o
// id: Title/Título (1), Heading N, Título N, TítuloN, Ttulo N (ids de Word en
// español) y Encabezado N.
func headingFromName(s string) int {
	n := strings.ToLower(ingest.Fold(s))
	n = strings.NewReplacer(" ", "", "-", "", "_", "").Replace(n)
	switch n {
	case "title", "titulo", "ttulo":
		return 1
	}
	for _, p := range []string{"heading", "titulo", "ttulo", "encabezado"} {
		if rest, ok := strings.CutPrefix(n, p); ok && len(rest) == 1 && rest[0] >= '1' && rest[0] <= '6' {
			return int(rest[0] - '0')
		}
	}
	return 0
}

// styleHeading: nivel de título de un estilo (por nombre o id, o por nivel de
// esquema), siguiendo basedOn.
func (c *conv) styleHeading(id string) int {
	for i := 0; i < 16 && id != ""; i++ {
		if l := headingFromName(id); l > 0 {
			return l
		}
		si := c.styles[id]
		if si == nil {
			return 0
		}
		if l := headingFromName(si.name); l > 0 {
			return l
		}
		if si.outline >= 0 && si.outline <= 5 {
			return si.outline + 1
		}
		id = si.basedOn
	}
	return 0
}

func (c *conv) styleNum(id string) (string, string) {
	for i := 0; i < 16 && id != ""; i++ {
		si := c.styles[id]
		if si == nil {
			return "", ""
		}
		if si.numID != "" {
			return si.numID, si.ilvl
		}
		id = si.basedOn
	}
	return "", ""
}

func (c *conv) isBullet(numID, ilvl string) bool {
	f := c.fmts[c.nums[numID]][ilvl]
	return f == "" || f == "bullet" || f == "none"
}

// ─── Recorrido del cuerpo ────────────────────────────────────────────────────

// bodyElems aplana los contenedores (controles de contenido y customXml) del
// cuerpo.
func bodyElems(n *node) []*node {
	var out []*node
	for _, k := range n.kids {
		switch k.name {
		case "sdt":
			if sc := k.child("sdtContent"); sc != nil {
				out = append(out, bodyElems(sc)...)
			}
		case "customXml", "sdtContent":
			out = append(out, bodyElems(k)...)
		default:
			out = append(out, k)
		}
	}
	return out
}

func (c *conv) path() []string {
	out := make([]string, len(c.stack))
	for i, h := range c.stack {
		out[i] = h.title
	}
	return out
}

func (c *conv) walk(body *node) error {
	for i, el := range bodyElems(body) {
		if i%256 == 0 {
			if err := c.ctx.Err(); err != nil {
				return err
			}
		}
		switch el.name {
		case "p":
			c.paraIdx++
			c.paragraph(el)
		case "tbl":
			c.tableIdx++
			c.flushList()
			c.table(el)
		}
	}
	c.flushList()
	return nil
}

func (c *conv) paragraph(p *node) {
	text := paraText(p)
	if text == "" {
		c.flushList()
		return
	}
	style, numID, ilvl, outline := "", "", "", -1
	if ppr := p.child("pPr"); ppr != nil {
		if s := ppr.child("pStyle"); s != nil {
			style = s.attr("val")
		}
		if o := ppr.child("outlineLvl"); o != nil {
			if v, err := strconv.Atoi(o.attr("val")); err == nil {
				outline = v
			}
		}
		if np := ppr.child("numPr"); np != nil {
			if n := np.child("numId"); n != nil {
				numID = n.attr("val")
			}
			if l := np.child("ilvl"); l != nil {
				ilvl = l.attr("val")
			}
		}
	}
	level := c.styleHeading(style)
	if level == 0 && outline >= 0 && outline <= 5 {
		level = outline + 1
	}
	if level > 0 {
		c.flushList()
		for len(c.stack) > 0 && c.stack[len(c.stack)-1].level >= level {
			c.stack = c.stack[:len(c.stack)-1]
		}
		c.stack = append(c.stack, headingEntry{level: level, title: text})
		hp := c.path()
		c.blocks = append(c.blocks, ingest.Block{Kind: ingest.BlockHeading, Level: level, Text: strings.Repeat("#", level) + " " + text,
			HeadingPath: hp, Extraction: domain.ExtractionText,
			Location: domain.SourceLocation{"heading_path": hp, "paragraph_start": c.paraIdx, "paragraph_end": c.paraIdx}})
		return
	}
	if numID == "" {
		numID, ilvl = c.styleNum(style)
	}
	if numID != "" && numID != "0" {
		if ilvl == "" {
			ilvl = "0"
		}
		depth, _ := strconv.Atoi(ilvl)
		if depth < 0 || depth > 8 {
			depth = 0
		}
		if c.list == nil {
			c.list = &listAcc{start: c.paraIdx, counters: map[string]int{}}
		}
		marker := "- "
		if !c.isBullet(numID, ilvl) {
			key := numID + "|" + ilvl
			c.list.counters[key]++
			for k := range c.list.counters { // reiniciar los niveles más profundos
				if kn, kl, ok := strings.Cut(k, "|"); ok && kn == numID {
					if d, err := strconv.Atoi(kl); err == nil && d > depth {
						delete(c.list.counters, k)
					}
				}
			}
			marker = strconv.Itoa(c.list.counters[key]) + ". "
		}
		c.list.lines = append(c.list.lines, strings.Repeat("  ", depth)+marker+text)
		c.list.end = c.paraIdx
		return
	}
	c.flushList()
	hp := c.path()
	c.blocks = append(c.blocks, ingest.Block{Kind: ingest.BlockParagraph, Text: text, HeadingPath: hp, Extraction: domain.ExtractionText,
		Location: domain.SourceLocation{"heading_path": hp, "paragraph_start": c.paraIdx, "paragraph_end": c.paraIdx}})
}

func (c *conv) flushList() {
	if c.list == nil {
		return
	}
	hp := c.path()
	c.blocks = append(c.blocks, ingest.Block{Kind: ingest.BlockParagraph, Text: strings.Join(c.list.lines, "\n"), HeadingPath: hp,
		Extraction: domain.ExtractionText,
		Location:   domain.SourceLocation{"heading_path": hp, "paragraph_start": c.list.start, "paragraph_end": c.list.end}})
	c.list = nil
}

func (c *conv) table(tbl *node) {
	var rows [][]string
	for _, tr := range rowsOf(tbl) {
		var cells []string
		for _, tc := range cellsOf(tr) {
			var b strings.Builder
			collectText(tc, &b)
			cells = append(cells, strings.ReplaceAll(normSpace(b.String()), "|", `\|`))
		}
		rows = append(rows, cells)
	}
	cols := 0
	empty := true
	for _, r := range rows {
		cols = max(cols, len(r))
		for _, x := range r {
			if x != "" {
				empty = false
			}
		}
	}
	if len(rows) == 0 || cols == 0 || empty {
		return
	}
	line := func(r []string) string {
		cells := make([]string, cols)
		copy(cells, r)
		return "| " + strings.Join(cells, " | ") + " |"
	}
	lines := []string{line(rows[0]), "|" + strings.Repeat(" --- |", cols)}
	for _, r := range rows[1:] {
		lines = append(lines, line(r))
	}
	hp := c.path()
	c.blocks = append(c.blocks, ingest.Block{Kind: ingest.BlockTable, Text: strings.Join(lines, "\n"), HeadingPath: hp, Extraction: domain.ExtractionTable,
		Location: domain.SourceLocation{"heading_path": hp, "table": c.tableIdx, "row_start": 1, "row_end": len(rows)}})
}

func rowsOf(tbl *node) []*node {
	var out []*node
	for _, k := range bodyElems(tbl) {
		if k.name == "tr" {
			out = append(out, k)
		}
	}
	return out
}

func cellsOf(tr *node) []*node {
	var out []*node
	for _, k := range bodyElems(tr) {
		if k.name == "tc" {
			out = append(out, k)
		}
	}
	return out
}

// ─── Texto ───────────────────────────────────────────────────────────────────

func paraText(p *node) string {
	var b strings.Builder
	collectText(p, &b)
	return normSpace(b.String())
}

func normSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

// collectText junta el texto visible: w:t de las corridas (también dentro de
// hipervínculos, campos simples e inserciones); tabulaciones y saltos como
// espacio; sin texto borrado, códigos de campo, dibujos ni propiedades. Los
// párrafos y tablas anidados (celdas) se separan con espacios.
func collectText(n *node, b *strings.Builder) {
	switch n.name {
	case "t":
		b.WriteString(n.text.String())
		return
	case "tab", "br", "cr":
		b.WriteByte(' ')
		return
	case "del", "delText", "moveFrom", "instrText", "drawing", "pict", "object", "pPr", "rPr", "tblPr", "tcPr", "trPr",
		"sym", "fldChar", "footnoteReference", "endnoteReference", "commentReference", "sectPr", "tblGrid":
		return
	}
	for _, k := range n.kids {
		collectText(k, b)
	}
	if n.name == "p" || n.name == "tc" {
		b.WriteByte(' ')
	}
}

// String describe el conversor (registro y logs).
func (Converter) String() string { return fmt.Sprintf("docx (%s)", MIME) }
