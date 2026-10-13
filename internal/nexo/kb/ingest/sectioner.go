package ingest

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
)

// SectionDraft es una sección calculada antes de guardarse.
type SectionDraft struct {
	Ordinal     int
	Anchor      string
	HeadingPath []string
	Body        string
	TokenCount  int
	Location    domain.SourceLocation
	Extraction  string
}

// SectionerOptions configura el corte (RF-17).
type SectionerOptions struct {
	// TargetTokens: al superarlo, la sección se parte en el siguiente borde
	// de bloque. Las tablas y los bloques de código nunca se parten.
	TargetTokens int
	// SplitKeys: claves de procedencia que, al cambiar de valor entre dos
	// bloques, fuerzan una sección nueva (una sección no mezcla hojas ni
	// diapositivas; las páginas sí se mezclan y quedan como rango).
	SplitKeys []string
}

// DefaultSectionerOptions: unas 350 palabras por sección.
var DefaultSectionerOptions = SectionerOptions{TargetTokens: 350, SplitKeys: []string{"sheet", "slide"}}

// Section corta los bloques en secciones: cada título abre una sección nueva;
// una sección que solo tiene su título (seguida de un subtítulo) no se emite,
// porque su título ya está en la ruta de las secciones hijas.
func Section(blocks []Block, opts SectionerOptions) []SectionDraft {
	if opts.TargetTokens <= 0 {
		opts.TargetTokens = DefaultSectionerOptions.TargetTokens
	}
	var out []SectionDraft
	used := map[string]int{}
	type acc struct {
		path    []string
		heading *Block
		blocks  []Block
		tokens  int
		part    int
	}
	var cur *acc
	flush := func() {
		if cur == nil || len(cur.blocks) == 0 {
			return
		}
		var parts []string
		var locs []domain.SourceLocation
		kinds := map[string]bool{}
		if cur.heading != nil {
			parts = append(parts, cur.heading.Text)
			locs = append(locs, cur.heading.Location)
		}
		for _, b := range cur.blocks {
			parts = append(parts, b.Text)
			locs = append(locs, b.Location)
			kinds[b.Extraction] = true
		}
		body := strings.Join(parts, "\n\n")
		base := anchorFor(cur.path)
		if cur.part > 1 {
			base = fmt.Sprintf("%s-p%d", base, cur.part)
		}
		anchor := base
		if n := used[base]; n > 0 {
			anchor = fmt.Sprintf("%s-%d", base, n+1)
		}
		used[base]++
		out = append(out, SectionDraft{
			Ordinal: len(out) + 1, Anchor: anchor, HeadingPath: append([]string{}, cur.path...),
			Body: body, TokenCount: CountTokens(body), Location: MergeLocations(locs), Extraction: extractionOf(kinds),
		})
		cur.blocks, cur.tokens = nil, 0
	}
	for i := range blocks {
		b := blocks[i]
		if b.Kind == BlockHeading {
			flush()
			h := b
			cur = &acc{path: b.HeadingPath, heading: &h, part: 1}
			continue
		}
		if cur == nil {
			cur = &acc{path: b.HeadingPath, part: 1}
		}
		if len(cur.blocks) > 0 && splitKeyChanged(cur.blocks[len(cur.blocks)-1].Location, b.Location, opts.SplitKeys) {
			flush()
			cur.part++
		}
		t := CountTokens(b.Text)
		if len(cur.blocks) > 0 && cur.tokens+t > opts.TargetTokens {
			flush()
			cur.part++
		}
		cur.blocks = append(cur.blocks, b)
		cur.tokens += t
	}
	flush()
	// Un documento que solo tiene títulos: una sección con todos ellos.
	if len(out) == 0 && len(blocks) > 0 {
		var parts []string
		var locs []domain.SourceLocation
		for _, b := range blocks {
			parts = append(parts, b.Text)
			locs = append(locs, b.Location)
		}
		body := strings.Join(parts, "\n\n")
		out = append(out, SectionDraft{Ordinal: 1, Anchor: anchorFor(blocks[0].HeadingPath), HeadingPath: blocks[0].HeadingPath,
			Body: body, TokenCount: CountTokens(body), Location: MergeLocations(locs), Extraction: domain.ExtractionText})
	}
	return out
}

func splitKeyChanged(a, b domain.SourceLocation, keys []string) bool {
	for _, k := range keys {
		va, oka := a[k]
		vb, okb := b[k]
		if (oka || okb) && !reflect.DeepEqual(va, vb) {
			return true
		}
	}
	return false
}

func extractionOf(kinds map[string]bool) string {
	switch len(kinds) {
	case 0:
		return domain.ExtractionText
	case 1:
		for k := range kinds {
			return k
		}
	}
	return domain.ExtractionMixed
}

// MergeLocations combina la procedencia de varios bloques: las claves
// *_start toman el mínimo, las *_end el máximo; las demás quedan iguales si
// coinciden o como lista de valores distintos si no (así una sección que
// cruza hojas o rutas lo registra).
func MergeLocations(locs []domain.SourceLocation) domain.SourceLocation {
	out := domain.SourceLocation{}
	multi := map[string][]any{}
	for _, l := range locs {
		for k, v := range l {
			switch {
			case strings.HasSuffix(k, "_start"):
				if cur, ok := out[k]; !ok || toInt(v) < toInt(cur) {
					out[k] = toInt(v)
				}
			case strings.HasSuffix(k, "_end"):
				if cur, ok := out[k]; !ok || toInt(v) > toInt(cur) {
					out[k] = toInt(v)
				}
			default:
				seen := false
				for _, x := range multi[k] {
					if reflect.DeepEqual(x, v) {
						seen = true
						break
					}
				}
				if !seen {
					multi[k] = append(multi[k], v)
				}
			}
		}
	}
	keys := make([]string, 0, len(multi))
	for k := range multi {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		vals := multi[k]
		if k == "heading_path" {
			// La ruta de la sección es la del primer bloque (su título).
			out[k] = vals[0]
			continue
		}
		if len(vals) == 1 {
			out[k] = vals[0]
		} else {
			out[k+"s"] = vals
		}
	}
	return out
}

func toInt(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	}
	return 0
}

// CountTokens es una aproximación (palabras); token_count es orientativo.
func CountTokens(s string) int { return len(strings.Fields(s)) }

// ─── Anclas y normalización ──────────────────────────────────────────────────

// anchorFor: slug de cada título de la ruta unidos por "." ("intro" si no
// hay títulos). Estable mientras no cambien los títulos.
func anchorFor(path []string) string {
	if len(path) == 0 {
		return "intro"
	}
	parts := make([]string, 0, len(path))
	for _, p := range path {
		parts = append(parts, Slugify(p))
	}
	return strings.Join(parts, ".")
}

var foldChain = transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)

// Fold quita tildes y diéresis (á -> a, ñ -> n, ü -> u).
func Fold(s string) string {
	out, _, err := transform.String(foldChain, s)
	if err != nil {
		return s
	}
	return out
}

// Slugify: minúsculas sin tildes, letras y números, guiones; máximo 60.
func Slugify(s string) string {
	s = strings.ToLower(Fold(s))
	var b strings.Builder
	dash := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if len(out) > 60 {
		out = strings.TrimRight(out[:60], "-")
	}
	if out == "" {
		return "seccion"
	}
	return out
}
