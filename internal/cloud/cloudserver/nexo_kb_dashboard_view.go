package cloudserver

// Nexo IA · NX-10 (panel) — piezas de presentación de las páginas de
// conocimiento: estados en español, buscador con respuesta citada, árbol de
// documentos por carpeta y comparación de versiones por sección. Diseño en
// docs/nexo/panel/design.md; estilos en dashboard/static/nexo_theme.css.
// Todo texto que viene de la base o del usuario pasa por esc().

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/search"
)

// kbNoStandard es el mensaje literal cuando no hay estándar (HU-06; el mismo
// que devuelven las herramientas MCP).
const kbNoStandard = "No existe un estándar oficial publicado sobre este tema."

// kbState devuelve el estado en español y la clase del chip.
func kbState(status string, from time.Time, until *time.Time, at time.Time) (string, string) {
	switch {
	case status == domain.DocRetired:
		return "Retirado", "nx-chip-ret"
	case from.After(at):
		return "Programado", "nx-chip-sched"
	case until != nil && !until.After(at):
		return "Vencido", "nx-chip-exp"
	}
	return "Vigente", "nx-chip-ok"
}

func kbChip(label, class string) string {
	return `<span class="nx-chip ` + class + `">` + esc(label) + `</span>`
}

func kbDocChip(d domain.Document, at time.Time) string {
	return kbChip(kbState(d.Status, d.ValidFrom, d.ValidUntil, at))
}

func kbDay(t time.Time) string { return t.Format("2006-01-02") }

func kbVigencia(from time.Time, until *time.Time) string {
	s := "desde " + kbDay(from)
	if until != nil {
		s += " hasta " + kbDay(*until)
	}
	return s
}

var kbRoleLabels = map[string]string{
	"superadmin": "Superadministrador", "admin": "Administrador", "auditor": "Auditor", "miembro": "Miembro",
	"lector": "Lector", "proponente": "Proponente", "publicador": "Publicador", "responsable": "Responsable",
}

func kbRole(r string) string {
	if l, ok := kbRoleLabels[r]; ok {
		return l
	}
	if r == "" {
		return "Sin rol"
	}
	return r
}

func kbVisibility(v string) string {
	switch v {
	case "open":
		return "Abierto a todo el equipo"
	case "restricted":
		return "Restringido a sus miembros"
	}
	return v
}

var kbResultLabels = map[string][2]string{
	domain.ResultCreated:   {"Creado", "nx-chip-ok"},
	domain.ResultUpdated:   {"Actualizado", "nx-chip-sched"},
	domain.ResultUnchanged: {"Sin cambios", "nx-chip-ret"},
	domain.ResultRejected:  {"Rechazado", "nx-chip-err"},
	domain.ResultRetired:   {"Retirado", "nx-chip-exp"},
	domain.ResultSkipped:   {"Omitido", "nx-chip-ret"},
}

func kbResultChip(status string) string {
	if l, ok := kbResultLabels[status]; ok {
		return kbChip(l[0], l[1])
	}
	return kbChip(status, "nx-chip-ret")
}

func kbResultWord(status string) string {
	if l, ok := kbResultLabels[status]; ok {
		return strings.ToLower(l[0])
	}
	return status
}

func kbPlural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// ─── Buscador ────────────────────────────────────────────────────────────────

// kbSearchForm dibuja el buscador. action es la página que lo recibe (GET:
// la URL se puede compartir y funciona sin JavaScript).
func kbSearchForm(action, q, at, scopeLabel string) string {
	var b strings.Builder
	b.WriteString(`<section class="nx-search" aria-labelledby="nx-search-title"><h2 class="nx-h2" id="nx-search-title">Buscar un estándar</h2>`)
	fmt.Fprintf(&b, `<form method="get" action="%s" role="search">`, esc(action))
	fmt.Fprintf(&b, `<label for="nx-q">Pregunta o tema <span class="nx-hint">%s</span><input id="nx-q" type="search" name="q" value="%s" maxlength="500" placeholder="Por ejemplo: nivel de log para errores de negocio" autocomplete="off" required/></label>`,
		esc(scopeLabel), esc(q))
	fmt.Fprintf(&b, `<label for="nx-at">En fecha <span class="nx-hint">opcional</span><input id="nx-at" type="date" name="at" value="%s"/></label>`, esc(at))
	b.WriteString(`<button type="submit">Buscar</button></form>`)
	return b.String()
}

// kbExcerpt escapa el fragmento y convierte las marcas **término** del
// extracto en <mark>.
func kbExcerpt(s string) string {
	// El extracto marca cada término con **; el Markdown del documento también
	// usa ** para negrita: solo se resaltan términos sueltos (sin espacios).
	s = strings.ReplaceAll(s, "`", "")
	s = kbPipesRe.ReplaceAllString(s, "; ")
	s = strings.Trim(strings.TrimSpace(s), "; ")
	e := esc(s)
	e = kbMarkRe.ReplaceAllString(e, "<mark>$1</mark>")
	return strings.ReplaceAll(e, "**", "")
}

var (
	kbMarkRe  = regexp.MustCompile(`\*\*([^\s*]{1,60})\*\*`)
	kbPipesRe = regexp.MustCompile(`(\s*\|\s*)+`)
)

// kbSearchResults dibuja la respuesta: cada resultado con su cita (ruta de
// títulos), estado, vigencia, fragmento y enlace a la sección; sin
// resultados, el mensaje literal de HU-06.
func kbSearchResults(res search.Result, at *time.Time, showSpace bool) string {
	var b strings.Builder
	b.WriteString(`<div class="nx-answers" id="nx-results" aria-live="polite">`)
	ref := time.Now()
	if at != nil {
		ref = *at
	}
	if res.NoResults || len(res.Hits) == 0 {
		b.WriteString(`<div class="nx-none" role="status"><strong>` + esc(kbNoStandard) + `</strong><p>Si tu asistente propone algo sobre este tema, es una recomendación propia, no un estándar. Prueba con otras palabras o pide al responsable del espacio que lo publique.</p></div></div>`)
		return b.String()
	}
	when := "vigentes hoy"
	if at != nil {
		when = "vigentes el " + kbDay(*at)
	}
	fmt.Fprintf(&b, `<p class="nx-answers-head">%s, %s. La primera es la más relevante.</p>`, esc(kbPlural(len(res.Hits), "sección encontrada", "secciones encontradas")), esc(when))
	for _, h := range res.Hits {
		label, class := kbState(h.DocStatus, h.ValidFrom, h.ValidUntil, ref)
		cite := []string{}
		if showSpace {
			cite = append(cite, h.Space)
		}
		cite = append(cite, h.Title)
		cite = append(cite, h.HeadingPath...)
		link := h.Link
		if link == "" {
			link = search.SectionPath(h.Space, h.DocKey, h.Anchor, h.Version)
		}
		b.WriteString(`<article class="nx-answer">`)
		fmt.Fprintf(&b, `<p class="nx-answer-cite">%s</p>`, esc(strings.Join(cite, " > ")))
		title := h.Title
		if n := len(h.HeadingPath); n > 0 {
			title = h.HeadingPath[n-1]
		}
		fmt.Fprintf(&b, `<h3 class="nx-answer-title">%s</h3>`, esc(title))
		fmt.Fprintf(&b, `<blockquote>%s</blockquote>`, kbExcerpt(h.Excerpt))
		fmt.Fprintf(&b, `<div class="nx-answer-foot">%s <span>Versión %d, %s</span><span>%s</span><a href="%s">Abrir la sección</a>`,
			kbChip(label, class), h.Version, esc(kbVigencia(h.ValidFrom, h.ValidUntil)), esc(h.DocKey), esc(link))
		if h.SourceURL != "" && kbSafeHTTP(h.SourceURL) {
			fmt.Fprintf(&b, ` <a href="%s" rel="noopener noreferrer">Ver la fuente original</a>`, esc(h.SourceURL))
		}
		b.WriteString(`</div></article>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

func kbSafeHTTP(u string) bool {
	l := strings.ToLower(strings.TrimSpace(u))
	return strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "http://")
}

// kbParseAt lee el campo "en fecha" (AAAA-MM-DD, fin del día en UTC para
// incluir lo que rige ese día).
func kbParseAt(raw string) (*time.Time, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, ""
	}
	t, err := time.Parse("2006-01-02", raw)
	if err != nil {
		if t2, err2 := time.Parse(time.RFC3339, raw); err2 == nil {
			return &t2, ""
		}
		return nil, "La fecha debe tener el formato AAAA-MM-DD."
	}
	t = t.Add(24*time.Hour - time.Second)
	return &t, ""
}

// ─── Árbol de documentos ─────────────────────────────────────────────────────

// kbNode es una carpeta del árbol de documentos.
type kbNode struct {
	name     string
	docs     []domain.Document
	children map[string]*kbNode
	total    int
}

func kbTree(docs []domain.Document) *kbNode {
	root := &kbNode{children: map[string]*kbNode{}}
	for _, d := range docs {
		n := root
		n.total++
		dir := path.Dir(d.DocKey)
		if dir != "." {
			for _, seg := range strings.Split(dir, "/") {
				c, ok := n.children[seg]
				if !ok {
					c = &kbNode{name: seg, children: map[string]*kbNode{}}
					n.children[seg] = c
				}
				n = c
				n.total++
			}
		}
		n.docs = append(n.docs, d)
	}
	return root
}

// kbTreeHTML dibuja el árbol plegable (<details>): carpetas primero, luego
// los documentos de la carpeta. Se abren los niveles con pocos documentos.
func kbTreeHTML(n *kbNode, depth int, doc func(domain.Document) string) string {
	var b strings.Builder
	names := make([]string, 0, len(n.children))
	for k := range n.children {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		c := n.children[k]
		// Una cadena de carpetas con una sola subcarpeta y sin documentos se
		// muestra en una línea (a / b / c).
		label := c.name
		for len(c.docs) == 0 && len(c.children) == 1 {
			for _, only := range c.children {
				label += " / " + only.name
				c = only
			}
		}
		open := ""
		if depth == 0 || c.total <= 6 {
			open = " open"
		}
		fmt.Fprintf(&b, `<details class="nx-folder"%s><summary>%s <span class="nx-count">%s</span></summary><div class="nx-folder-body">%s</div></details>`,
			open, esc(label), esc(kbPlural(c.total, "documento", "documentos")), kbTreeHTML(c, depth+1, doc))
	}
	if len(n.docs) > 0 {
		sort.SliceStable(n.docs, func(i, j int) bool { return strings.ToLower(n.docs[i].Title) < strings.ToLower(n.docs[j].Title) })
		b.WriteString(`<ul class="nx-docs">`)
		for _, d := range n.docs {
			b.WriteString(doc(d))
		}
		b.WriteString(`</ul>`)
	}
	return b.String()
}

func kbFolderLabel(dir string) string {
	if dir == "" {
		return "Raíz del espacio"
	}
	return strings.ReplaceAll(dir, "/", " / ")
}

// ─── Comparación de versiones ────────────────────────────────────────────────

type kbDiffItem struct {
	kind     string // add | del | mod
	title    string
	old, new string
}

// kbDiff compara dos versiones por sección (ruta de títulos).
func kbDiff(oldSecs, newSecs []kbSec) []kbDiffItem {
	key := func(s kbSec) string {
		if len(s.path) > 0 {
			return strings.Join(s.path, " > ")
		}
		return s.anchor
	}
	oldBy := map[string]kbSec{}
	for _, s := range oldSecs {
		oldBy[key(s)] = s
	}
	seen := map[string]bool{}
	var out []kbDiffItem
	for _, s := range newSecs {
		k := key(s)
		seen[k] = true
		o, ok := oldBy[k]
		switch {
		case !ok:
			out = append(out, kbDiffItem{kind: "add", title: k, new: s.body})
		case strings.TrimSpace(o.body) != strings.TrimSpace(s.body):
			out = append(out, kbDiffItem{kind: "mod", title: k, old: o.body, new: s.body})
		}
	}
	for _, s := range oldSecs {
		if !seen[key(s)] {
			out = append(out, kbDiffItem{kind: "del", title: key(s), old: s.body})
		}
	}
	return out
}

func kbClip(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "\n..."
}

func kbDiffHTML(items []kbDiffItem) string {
	if len(items) == 0 {
		return `<p class="nx-empty">El texto de las secciones es igual; cambiaron solo los metadatos (vigencia, categoría u origen).</p>`
	}
	var b strings.Builder
	b.WriteString(`<div class="nx-diff">`)
	for _, it := range items {
		switch it.kind {
		case "add":
			fmt.Fprintf(&b, `<div class="nx-diff-item nx-diff-add"><h3>Sección nueva: %s</h3><pre class="nx-new">%s</pre></div>`, esc(it.title), esc(kbClip(it.new, 1500)))
		case "del":
			fmt.Fprintf(&b, `<div class="nx-diff-item nx-diff-del"><h3>Sección quitada: %s</h3><pre class="nx-old">%s</pre></div>`, esc(it.title), esc(kbClip(it.old, 1500)))
		default:
			fmt.Fprintf(&b, `<div class="nx-diff-item nx-diff-mod"><h3>Sección cambiada: %s</h3><p class="nx-hint">Antes</p><pre class="nx-old">%s</pre><p class="nx-hint">Ahora</p><pre class="nx-new">%s</pre></div>`,
				esc(it.title), esc(kbClip(it.old, 1500)), esc(kbClip(it.new, 1500)))
		}
	}
	b.WriteString(`</div>`)
	return b.String()
}

func kbDocURL(slug, key string, version int, extra string) string {
	v := url.Values{}
	v.Set("doc", key)
	if version > 0 {
		v.Set("v", fmt.Sprint(version))
	}
	if extra != "" {
		v.Set(extra, "1")
	}
	return "/dashboard/kb/spaces/" + url.PathEscape(slug) + "/doc?" + v.Encode()
}
