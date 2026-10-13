// Package mdhtml convierte el Markdown de los documentos de conocimiento a
// HTML seguro para el panel de Nexo (NX-10, panel).
//
// Es un renderizador mínimo y propio (sin dependencias): títulos, párrafos,
// listas (con anidación por sangría), código en bloque y en línea, tablas con
// barras, citas, líneas horizontales, negrita, cursiva y enlaces.
//
// Seguridad: todo el texto se escapa; nunca se emite HTML del documento (una
// etiqueta escrita en el Markdown se ve como texto). Los enlaces solo llevan
// http, https, mailto o rutas relativas; cualquier otro esquema (javascript:,
// data:, vbscript:...) se muestra como texto sin enlace. Las imágenes no se
// cargan: se muestran como un enlace con su texto alternativo.
package mdhtml

import (
	"html"
	"regexp"
	"strings"
)

// Render convierte Markdown a HTML seguro.
func Render(src string) string {
	src = strings.ReplaceAll(src, "\r\n", "\n")
	src = strings.ReplaceAll(src, "\r", "\n")
	r := &renderer{}
	r.blocks(strings.Split(src, "\n"))
	return r.b.String()
}

type renderer struct {
	b strings.Builder
}

var (
	reHeading  = regexp.MustCompile(`^ {0,3}(#{1,6})[ \t]+(.*?)[ \t]*#*[ \t]*$`)
	reFence    = regexp.MustCompile("^ {0,3}(```+|~~~+)[ \t]*([^`]*)$")
	reHR       = regexp.MustCompile(`^ {0,3}([-*_])[ \t]*(?:[-*_][ \t]*){2,}$`)
	reUL       = regexp.MustCompile(`^([ \t]*)[-*+][ \t]+(.*)$`)
	reOL       = regexp.MustCompile(`^([ \t]*)(\d{1,9})[.)][ \t]+(.*)$`)
	reQuote    = regexp.MustCompile(`^ {0,3}>[ ]?(.*)$`)
	reTableSep = regexp.MustCompile(`^[ \t]*\|?[ \t]*:?-{1,}:?[ \t]*(\|[ \t]*:?-{1,}:?[ \t]*)*\|?[ \t]*$`)
	reTOC      = regexp.MustCompile(`^\s*\[\[_TOC_\]\]\s*$`)
	reBR       = regexp.MustCompile(`(?i)<br\s*/?>`)
	reLang     = regexp.MustCompile(`^[A-Za-z0-9_+.#-]{1,32}$`)
)

func (r *renderer) blocks(lines []string) {
	i := 0
	for i < len(lines) {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "" || reTOC.MatchString(line):
			i++
		case reFence.MatchString(line):
			i = r.fence(lines, i)
		case reHeading.MatchString(line):
			m := reHeading.FindStringSubmatch(line)
			level := string('0' + rune(len(m[1])))
			r.b.WriteString("<h" + level + ">" + inline(m[2]) + "</h" + level + ">\n")
			i++
		case reHR.MatchString(line):
			r.b.WriteString("<hr/>\n")
			i++
		case reQuote.MatchString(line):
			var inner []string
			for i < len(lines) && strings.TrimSpace(lines[i]) != "" {
				if m := reQuote.FindStringSubmatch(lines[i]); m != nil {
					inner = append(inner, m[1])
				} else {
					inner = append(inner, lines[i])
				}
				i++
			}
			r.b.WriteString("<blockquote>\n")
			sub := &renderer{}
			sub.blocks(inner)
			r.b.WriteString(sub.b.String())
			r.b.WriteString("</blockquote>\n")
		case reUL.MatchString(line) || reOL.MatchString(line):
			i = r.list(lines, i)
		case strings.Contains(line, "|") && i+1 < len(lines) && reTableSep.MatchString(lines[i+1]) && strings.Contains(lines[i+1], "-"):
			i = r.table(lines, i)
		default:
			var para []string
			for i < len(lines) {
				l := lines[i]
				if strings.TrimSpace(l) == "" || reFence.MatchString(l) || reHeading.MatchString(l) || reQuote.MatchString(l) ||
					(len(para) > 0 && (reUL.MatchString(l) || reOL.MatchString(l))) || reHR.MatchString(l) {
					break
				}
				para = append(para, strings.TrimSpace(l))
				i++
			}
			if len(para) == 0 { // línea que ninguna regla consumió
				para = append(para, trimmed)
				i++
			}
			r.b.WriteString("<p>" + inline(strings.Join(para, "\n")) + "</p>\n")
		}
	}
}

func (r *renderer) fence(lines []string, i int) int {
	m := reFence.FindStringSubmatch(lines[i])
	marker := m[1]
	lang := strings.Fields(m[2])
	i++
	var body []string
	for i < len(lines) {
		t := strings.TrimSpace(lines[i])
		if strings.HasPrefix(t, marker[:3]) && strings.Trim(t, string(marker[0])) == "" && len(t) >= len(marker) {
			i++
			break
		}
		body = append(body, lines[i])
		i++
	}
	// Quita la sangría de la valla (código dentro de una lista) y las líneas
	// en blanco del inicio y del final.
	for len(body) > 0 && strings.TrimSpace(body[0]) == "" {
		body = body[1:]
	}
	for len(body) > 0 && strings.TrimSpace(body[len(body)-1]) == "" {
		body = body[:len(body)-1]
	}
	minIndent := -1
	for _, l := range body {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if n := len(l) - len(strings.TrimLeft(l, " \t")); minIndent < 0 || n < minIndent {
			minIndent = n
		}
	}
	for k, l := range body {
		if len(l) >= minIndent && minIndent > 0 {
			body[k] = l[minIndent:]
		}
	}
	r.b.WriteString("<pre><code")
	if len(lang) > 0 && reLang.MatchString(lang[0]) {
		r.b.WriteString(` class="language-` + html.EscapeString(lang[0]) + `"`)
	}
	r.b.WriteString(">" + html.EscapeString(strings.Join(body, "\n")) + "</code></pre>\n")
	return i
}

type listItem struct {
	indent  int
	ordered bool
	text    []string
}

func indentOf(s string) int {
	n := 0
	for _, c := range s {
		switch c {
		case ' ':
			n++
		case '\t':
			n += 4
		default:
			return n
		}
	}
	return n
}

// list lee una lista (con sus continuaciones) y la emite anidada por sangría.
func (r *renderer) list(lines []string, i int) int {
	var items []listItem
	for i < len(lines) {
		l := lines[i]
		// Un ítem de otro tipo en el nivel superior empieza otra lista.
		if len(items) > 0 {
			ul, ol := reUL.FindStringSubmatch(l), reOL.FindStringSubmatch(l)
			if (ul != nil && items[0].ordered && indentOf(ul[1]) <= items[0].indent) ||
				(ol != nil && !items[0].ordered && indentOf(ol[1]) <= items[0].indent) {
				break
			}
		}
		if m := reUL.FindStringSubmatch(l); m != nil && !reHR.MatchString(l) {
			items = append(items, listItem{indent: indentOf(m[1]), text: []string{m[2]}})
		} else if m := reOL.FindStringSubmatch(l); m != nil {
			items = append(items, listItem{indent: indentOf(m[1]), ordered: true, text: []string{m[3]}})
		} else if strings.TrimSpace(l) != "" && len(items) > 0 && indentOf(l) > items[len(items)-1].indent && !reFence.MatchString(l) {
			last := &items[len(items)-1]
			last.text = append(last.text, strings.TrimSpace(l))
		} else if strings.TrimSpace(l) == "" && i+1 < len(lines) && (reUL.MatchString(lines[i+1]) || reOL.MatchString(lines[i+1])) {
			// línea en blanco entre ítems de la misma lista
		} else {
			break
		}
		i++
	}
	type open struct {
		indent  int
		ordered bool
	}
	var stack []open
	tag := func(o bool) string {
		if o {
			return "ol"
		}
		return "ul"
	}
	for k, it := range items {
		switch {
		case len(stack) == 0:
			stack = append(stack, open{it.indent, it.ordered})
			r.b.WriteString("<" + tag(it.ordered) + ">\n<li>")
		case it.indent > stack[len(stack)-1].indent:
			stack = append(stack, open{it.indent, it.ordered})
			r.b.WriteString("\n<" + tag(it.ordered) + ">\n<li>")
		default:
			for len(stack) > 1 && it.indent < stack[len(stack)-1].indent {
				r.b.WriteString("</li>\n</" + tag(stack[len(stack)-1].ordered) + ">\n")
				stack = stack[:len(stack)-1]
			}
			r.b.WriteString("</li>\n<li>")
		}
		r.b.WriteString(inline(strings.Join(it.text, "\n")))
		_ = k
	}
	for len(stack) > 0 {
		r.b.WriteString("</li>\n</" + tag(stack[len(stack)-1].ordered) + ">\n")
		stack = stack[:len(stack)-1]
	}
	return i
}

func splitRow(line string) []string {
	t := strings.TrimSpace(line)
	t = strings.TrimPrefix(t, "|")
	t = strings.TrimSuffix(t, "|")
	// Respeta \| como barra literal.
	t = strings.ReplaceAll(t, `\|`, "\x00")
	parts := strings.Split(t, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(strings.ReplaceAll(parts[i], "\x00", "|"))
	}
	return parts
}

func (r *renderer) table(lines []string, i int) int {
	head := splitRow(lines[i])
	aligns := splitRow(lines[i+1])
	i += 2
	align := func(k int) string {
		if k >= len(aligns) {
			return ""
		}
		a := aligns[k]
		switch {
		case strings.HasPrefix(a, ":") && strings.HasSuffix(a, ":"):
			return ` style="text-align:center"`
		case strings.HasSuffix(a, ":"):
			return ` style="text-align:right"`
		}
		return ""
	}
	r.b.WriteString(`<div class="nx-scroll"><table><thead><tr>`)
	for k, h := range head {
		r.b.WriteString("<th" + align(k) + ">" + inline(h) + "</th>")
	}
	r.b.WriteString("</tr></thead><tbody>\n")
	for i < len(lines) && strings.TrimSpace(lines[i]) != "" && strings.Contains(lines[i], "|") {
		r.b.WriteString("<tr>")
		for k, c := range splitRow(lines[i]) {
			r.b.WriteString("<td" + align(k) + ">" + inline(c) + "</td>")
		}
		r.b.WriteString("</tr>\n")
		i++
	}
	r.b.WriteString("</tbody></table></div>\n")
	return i
}

// ─── En línea ────────────────────────────────────────────────────────────────

var (
	reImage    = regexp.MustCompile(`!\[([^\]]*)\]\(([^()\s]*(?:\([^()\s]*\)[^()\s]*)*)(?:\s+"[^"]*")?\)`)
	reLink     = regexp.MustCompile(`\[([^\]]+)\]\(([^()\s]*(?:\([^()\s]*\)[^()\s]*)*)(?:\s+"[^"]*")?\)`)
	reAutoLink = regexp.MustCompile(`&lt;((?:https?|mailto):[^\s<>&]+(?:&amp;[^\s<>&]+)*)&gt;`)
	reBold     = regexp.MustCompile(`\*\*([^*\n]+?)\*\*|__([^_\n]+?)__`)
	reItalic   = regexp.MustCompile(`(^|[^\w*])\*([^*\s][^*\n]*?)\*|(^|[^\w])_([^_\s][^_\n]*?)_([^\w]|$)`)
	reStrike   = regexp.MustCompile(`~~([^~\n]+?)~~`)
)

// SafeURL informa si un destino de enlace es seguro: http, https, mailto o
// una ruta relativa (sin esquema).
func SafeURL(raw string) bool {
	u := strings.Map(func(r rune) rune {
		if r <= 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, html.UnescapeString(raw))
	if u == "" {
		return false
	}
	lu := strings.ToLower(u)
	for _, p := range []string{"http://", "https://", "mailto:"} {
		if strings.HasPrefix(lu, p) {
			return true
		}
	}
	// Sin esquema: no hay ":" antes del primer "/", "?" o "#".
	end := len(lu)
	if k := strings.IndexAny(lu, "/?#"); k >= 0 {
		end = k
	}
	return !strings.Contains(lu[:end], ":") && !strings.HasPrefix(lu, "//")
}

func anchor(text, href string) string {
	if !SafeURL(href) {
		return text
	}
	return `<a href="` + html.EscapeString(href) + `" rel="noopener noreferrer">` + text + `</a>`
}

// inline aplica el formato en línea; el código en línea se separa antes de
// escapar para que su contenido no se interprete.
func inline(s string) string {
	s = reBR.ReplaceAllString(s, "\n")
	var out strings.Builder
	for {
		k := strings.Index(s, "`")
		if k < 0 {
			out.WriteString(inlineText(s))
			break
		}
		// Código en línea con el mismo número de comillas.
		n := 0
		for k+n < len(s) && s[k+n] == '`' {
			n++
		}
		ticks := s[k : k+n]
		rest := s[k+n:]
		end := strings.Index(rest, ticks)
		if end < 0 {
			out.WriteString(inlineText(s[:k+n]))
			s = rest
			continue
		}
		out.WriteString(inlineText(s[:k]))
		out.WriteString("<code>" + html.EscapeString(strings.TrimSpace(rest[:end])) + "</code>")
		s = rest[end+n:]
	}
	return strings.ReplaceAll(out.String(), "\n", "<br/>\n")
}

// inlineText: escapa y luego aplica enlaces, negrita, cursiva y tachado sobre
// el texto ya escapado (los destinos de enlace se validan sin escapar).
func inlineText(s string) string {
	// Quita los caracteres que se usan como marcadores internos.
	s = strings.ReplaceAll(s, "\x01", "")
	e := html.EscapeString(s)
	// Los enlaces se reemplazan por marcadores para que el énfasis no toque
	// sus destinos; se restituyen al final.
	var links []string
	hold := func(markup string) string {
		links = append(links, markup)
		return "\x01L" + itoa(len(links)-1) + "\x01"
	}
	// html.EscapeString escapa < y >: los autoenlaces quedan como &lt;...&gt;.
	e = reAutoLink.ReplaceAllStringFunc(e, func(m string) string {
		u := reAutoLink.FindStringSubmatch(m)[1]
		return hold(anchor(u, html.UnescapeString(u)))
	})
	e = reImage.ReplaceAllStringFunc(e, func(m string) string {
		sm := reImage.FindStringSubmatch(m)
		alt := sm[1]
		if alt == "" {
			alt = "imagen"
		}
		return hold(anchor("[imagen: "+alt+"]", html.UnescapeString(sm[2])))
	})
	e = reLink.ReplaceAllStringFunc(e, func(m string) string {
		sm := reLink.FindStringSubmatch(m)
		return hold(anchor(emphasis(sm[1]), html.UnescapeString(sm[2])))
	})
	e = emphasis(e)
	if len(links) > 0 {
		e = reHold.ReplaceAllStringFunc(e, func(m string) string {
			n := 0
			for _, c := range m[2 : len(m)-1] {
				n = n*10 + int(c-'0')
			}
			if n < len(links) {
				return links[n]
			}
			return ""
		})
	}
	return e
}

var reHold = regexp.MustCompile("\x01[A-Z][0-9]+\x01")

func emphasis(e string) string {
	e = reBold.ReplaceAllString(e, "<strong>$1$2</strong>")
	e = reStrike.ReplaceAllString(e, "<del>$1</del>")
	return reItalic.ReplaceAllStringFunc(e, func(m string) string {
		sm := reItalic.FindStringSubmatch(m)
		if sm[2] != "" {
			return sm[1] + "<em>" + sm[2] + "</em>"
		}
		return sm[3] + "<em>" + sm[4] + "</em>" + sm[5]
	})
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	return string(d)
}
