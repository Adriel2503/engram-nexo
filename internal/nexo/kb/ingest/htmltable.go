package ingest

// Nexo IA · NX-10 (entrega 2b, IN-08) — tablas HTML a Markdown. Solo se lee
// el texto de las celdas con el tokenizador de golang.org/x/net/html (ya es
// dependencia del módulo): nada se ejecuta ni se interpreta (scripts, estilos
// y atributos se ignoran). colspan repite la celda en las columnas que ocupa
// (así cada fila conserva su cabecera al partir la tabla por filas, RC-04).

import (
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// maxHTMLTableCells acota el trabajo ante una tabla HTML gigante.
const maxHTMLTableCells = 20000

// HTMLTablesToMarkdown convierte el fragmento HTML (una o más tablas) a tablas
// Markdown separadas por una línea vacía. El texto fuera de las tablas se
// conserva como párrafo.
func HTMLTablesToMarkdown(fragment string) string {
	z := html.NewTokenizer(strings.NewReader(fragment))
	type table struct {
		rows   [][]string
		header int // filas de cabecera (th o thead)
	}
	var (
		tables              []*table
		cur                 *table
		depth               int
		row                 []string
		cell                strings.Builder
		inCell, inHead      bool
		cellIsTH            bool
		colspan             int
		rowHasTH, rowIsHead bool
		cells               int
		outside             strings.Builder
		skip                int // dentro de <script> o <style>
	)
	flushCell := func() {
		if !inCell {
			return
		}
		txt := cleanCell(cell.String())
		n := colspan
		if n < 1 {
			n = 1
		}
		if n > 50 {
			n = 50
		}
		for k := 0; k < n; k++ {
			row = append(row, txt)
		}
		cells += n
		if cellIsTH {
			rowHasTH = true
		}
		cell.Reset()
		inCell = false
	}
	flushRow := func() {
		flushCell()
		if cur != nil && len(row) > 0 {
			cur.rows = append(cur.rows, row)
			if (rowHasTH || rowIsHead) && len(cur.rows) == cur.header+1 {
				cur.header++
			}
		}
		row, rowHasTH = nil, false
	}
	for cells < maxHTMLTableCells {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		name, hasAttr := z.TagName()
		tag := string(name)
		switch tt {
		case html.StartTagToken, html.SelfClosingTagToken:
			switch tag {
			case "script", "style":
				if tt == html.StartTagToken {
					skip++
				}
			case "table":
				depth++
				if depth == 1 {
					cur = &table{}
					tables = append(tables, cur)
				}
			case "thead":
				if depth == 1 {
					inHead = true
				}
			case "tr":
				if depth == 1 {
					flushRow()
					rowIsHead = inHead
				}
			case "td", "th":
				if depth == 1 {
					flushCell()
					inCell, cellIsTH, colspan = true, tag == "th", 1
					for hasAttr {
						var k, v []byte
						k, v, hasAttr = z.TagAttr()
						if string(k) == "colspan" {
							if n, err := strconv.Atoi(strings.TrimSpace(string(v))); err == nil {
								colspan = n
							}
						}
					}
				} else {
					cell.WriteByte(' ')
				}
			case "br", "p", "div", "li":
				if inCell {
					cell.WriteByte(' ')
				} else if depth == 0 {
					outside.WriteByte('\n')
				}
			}
		case html.EndTagToken:
			switch tag {
			case "script", "style":
				if skip > 0 {
					skip--
				}
			case "table":
				if depth == 1 {
					flushRow()
					cur = nil
				}
				if depth > 0 {
					depth--
				}
			case "thead":
				if depth == 1 {
					inHead = false
				}
			case "tr":
				if depth == 1 {
					flushRow()
				}
			case "td", "th":
				if depth == 1 {
					flushCell()
				}
			}
		case html.TextToken:
			if skip > 0 {
				continue
			}
			txt := string(z.Text())
			switch {
			case inCell:
				cell.WriteString(txt)
			case depth == 0:
				outside.WriteString(txt)
			}
		}
	}
	var parts []string
	if t := strings.TrimSpace(outside.String()); t != "" {
		parts = append(parts, t)
	}
	for _, t := range tables {
		if md := tableMarkdown(t.rows, t.header); md != "" {
			parts = append(parts, md)
		}
	}
	return strings.Join(parts, "\n\n")
}

// tableMarkdown arma la tabla GFM. Sin fila de cabecera, la primera fila
// hace de cabecera. Varias filas de cabecera se unen por columna.
func tableMarkdown(rows [][]string, header int) string {
	if len(rows) == 0 {
		return ""
	}
	cols := 0
	for _, r := range rows {
		cols = max(cols, len(r))
	}
	if header < 1 {
		header = 1
	}
	if header > len(rows) {
		header = len(rows)
	}
	head := make([]string, cols)
	for _, r := range rows[:header] {
		for c := 0; c < cols; c++ {
			v := ""
			if c < len(r) {
				v = r[c]
			}
			if v != "" && !strings.Contains(head[c], v) {
				head[c] = strings.TrimSpace(head[c] + " " + v)
			}
		}
	}
	line := func(cells []string) string {
		out := make([]string, cols)
		for c := 0; c < cols; c++ {
			if c < len(cells) {
				out[c] = cells[c]
			}
		}
		return "| " + strings.Join(out, " | ") + " |"
	}
	var b strings.Builder
	b.WriteString(line(head))
	b.WriteByte('\n')
	sep := make([]string, cols)
	for i := range sep {
		sep[i] = "---"
	}
	b.WriteString("| " + strings.Join(sep, " | ") + " |")
	for _, r := range rows[header:] {
		b.WriteByte('\n')
		b.WriteString(line(r))
	}
	return b.String()
}

func cleanCell(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return strings.ReplaceAll(s, "|", `\|`)
}
