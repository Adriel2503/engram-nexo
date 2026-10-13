package source

// Adaptador de la wiki de Azure DevOps (IN-08, design §17.8): se activa al
// encontrar archivos .order.
//
//   - Cada página es un .md; su título sale del nombre decodificado (el guion
//     es un espacio; %2D es un guion literal; %7C, etc.).
//   - Una carpeta con una página homónima al lado (X.md y X/) es la página
//     padre de las de la carpeta: la ruta de carpetas es la ruta de títulos
//     del documento (PathTitles).
//   - .order fija el orden de las páginas de cada carpeta (Order).
//   - Las páginas vacías (contenedoras) se saltan con aviso.
//   - Carpetas [TO_REMOVE]: se publican retiradas. Carpetas "≤-AAAA": se
//     publican vencidas (vigentes desde LegacyFrom hasta el 1 de enero de
//     AAAA+1). [TO_TIDY]: normal, con la etiqueta to-tidy (KP-11).
//   - /.attachments (imágenes, zip, pdf) y otros archivos no se indexan; se
//     informa un resumen.
//   - El contenido pasa por el conversor "ado-md" (ingest/ado.go).

import (
	"context"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/ingest"
)

// DefaultLegacyFrom es el inicio de la wiki de arquitectura (design §17.8).
var DefaultLegacyFrom = time.Date(2021, 11, 1, 0, 0, 0, 0, time.UTC)

var legacyDirRe = regexp.MustCompile(`^≤-?\s*(\d{4})$`)

func walkADO(ctx context.Context, files []File, opts Options) (Result, error) {
	res := Result{Kind: KindADO}
	legacyFrom := opts.LegacyFrom
	if legacyFrom.IsZero() {
		legacyFrom = DefaultLegacyFrom
	}
	// Orden de cada carpeta según .order.
	orders := map[string]map[string]int{}
	for _, f := range files {
		if path.Base(f.Path) != ".order" {
			continue
		}
		if Sniff(".order", f.Data) != "" {
			continue
		}
		m := map[string]int{}
		for i, line := range strings.Split(strings.ReplaceAll(string(f.Data), "\r\n", "\n"), "\n") {
			if l := strings.TrimSpace(line); l != "" {
				if _, ok := m[l]; !ok {
					m[l] = i + 1
				}
			}
		}
		orders[path.Dir(f.Path)] = m
	}
	ignored := map[string]int{}
	seen := map[string]string{}
	for _, f := range sortedFiles(files) {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		base := path.Base(f.Path)
		segs := strings.Split(f.Path, "/")
		if hasHiddenSegment(segs[:len(segs)-1]) || base == ".order" || base == ".gitignore" {
			if base != ".order" {
				ignored[ignoredKind(f.Path)]++
			}
			continue
		}
		ext := strings.ToLower(path.Ext(base))
		if ext != ".md" {
			if opts.Registry.Supported(base) && ext != ".markdown" && ext != ".mdx" {
				// Un .docx o .txt suelto dentro de la wiki se publica como en dir.
				if why := Sniff(base, f.Data); why != "" {
					res.Skipped = append(res.Skipped, Skipped{Path: f.Path, Reason: why})
					continue
				}
				doc := adoDoc(f, segs, orders, legacyFrom, opts.WikiURL)
				doc.Format = ""
				if prev, dup := seen[doc.DocKey]; dup {
					res.Skipped = append(res.Skipped, Skipped{Path: f.Path, Reason: "same doc_key as " + prev})
					continue
				}
				seen[doc.DocKey] = f.Path
				res.Docs = append(res.Docs, doc)
				continue
			}
			ignored[ignoredKind(f.Path)]++
			continue
		}
		if why := Sniff(base, f.Data); why != "" {
			res.Skipped = append(res.Skipped, Skipped{Path: f.Path, Reason: why})
			continue
		}
		if ingest.IsBlankADOPage(f.Data) {
			res.Skipped = append(res.Skipped, Skipped{Path: f.Path, Reason: "empty wiki page (container only)"})
			continue
		}
		doc := adoDoc(f, segs, orders, legacyFrom, opts.WikiURL)
		if prev, dup := seen[doc.DocKey]; dup {
			res.Skipped = append(res.Skipped, Skipped{Path: f.Path, Reason: "same doc_key as " + prev})
			continue
		}
		seen[doc.DocKey] = f.Path
		res.Docs = append(res.Docs, doc)
	}
	res.Skipped = append(res.Skipped, summarizeIgnored(ignored)...)
	sort.SliceStable(res.Docs, func(i, j int) bool {
		a, b := res.Docs[i], res.Docs[j]
		da, db := path.Dir(a.Path), path.Dir(b.Path)
		if da != db {
			return a.Path < b.Path
		}
		oa, ob := a.Order, b.Order
		if oa == 0 {
			oa = 1 << 30
		}
		if ob == 0 {
			ob = 1 << 30
		}
		if oa != ob {
			return oa < ob
		}
		return a.Path < b.Path
	})
	return res, nil
}

func adoDoc(f File, segs []string, orders map[string]map[string]int, legacyFrom time.Time, wikiURL string) Doc {
	base := segs[len(segs)-1]
	name := strings.TrimSuffix(base, path.Ext(base))
	var titles []string
	var hint Hint
	for _, s := range segs[:len(segs)-1] {
		titles = append(titles, TitleFromName(s))
		applyFolderHint(&hint, s, legacyFrom)
	}
	// La página homónima de una carpeta especial también recibe la marca.
	applyFolderHint(&hint, name, legacyFrom)
	doc := Doc{DocKey: DocKeyFromPath(f.Path), Path: f.Path, Title: TitleFromName(base), PathTitles: titles,
		Format: ingest.FormatADOMarkdown, Data: f.Data, Hint: hint}
	if m, ok := orders[path.Dir(f.Path)]; ok {
		doc.Order = m[name]
	}
	if wikiURL = strings.TrimRight(strings.TrimSpace(wikiURL), "/"); wikiURL != "" {
		pagePath := "/" + strings.Join(append(append([]string{}, titles...), TitleFromName(base)), "/")
		doc.SourceURL = wikiURL + "?pagePath=" + url.QueryEscape(pagePath)
	}
	return doc
}

func applyFolderHint(h *Hint, seg string, legacyFrom time.Time) {
	s := strings.TrimSpace(seg)
	switch {
	case strings.EqualFold(s, "[TO_REMOVE]"):
		h.Retire = true
		h.Note = "wiki folder [TO_REMOVE]: published as retired (KP-11)"
	case strings.EqualFold(s, "[TO_TIDY]"):
		if !containsStr(h.Tags, "to-tidy") {
			h.Tags = append(h.Tags, "to-tidy")
		}
	default:
		if m := legacyDirRe.FindStringSubmatch(decodeName(s)); m != nil && h.ValidUntil == nil {
			year, _ := strconv.Atoi(m[1])
			until := time.Date(year+1, 1, 1, 0, 0, 0, 0, time.UTC)
			from := legacyFrom
			if !from.Before(until) {
				from = until.AddDate(-1, 0, 0)
			}
			h.ValidFrom, h.ValidUntil = &from, &until
			if h.Note == "" {
				h.Note = "wiki folder " + s + ": published as expired (valid until " + until.Format("2006-01-02") + ", KP-11)"
			}
			if !containsStr(h.Tags, "legado") {
				h.Tags = append(h.Tags, "legado")
			}
		}
	}
}

func hasHiddenSegment(segs []string) bool {
	for _, s := range segs {
		if s != "." && strings.HasPrefix(s, ".") {
			return true
		}
	}
	return false
}

func ignoredKind(p string) string {
	if strings.HasPrefix(p, ".attachments/") || strings.Contains(p, "/.attachments/") {
		ext := strings.ToLower(path.Ext(p))
		if ext == "" {
			ext = "(sin extensión)"
		}
		return ".attachments/*" + ext
	}
	ext := strings.ToLower(path.Ext(p))
	if ext == "" {
		ext = "(sin extensión)"
	}
	return "*" + ext
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
