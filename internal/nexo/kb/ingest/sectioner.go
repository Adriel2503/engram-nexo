package ingest

import (
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
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
	// SplitTables (NX-10 2b, RC-04): una tabla mayor que TargetTokens se
	// parte por filas repitiendo la cabecera; nunca se parte una fila. Los
	// bloques de código siguen enteros.
	SplitTables bool `json:"split_tables"`
}

// DefaultSectionerOptions: unas 350 palabras por sección.
var DefaultSectionerOptions = SectionerOptions{TargetTokens: 350, SplitKeys: []string{"sheet", "slide"}}

// SplitTableRows parte un bloque de tabla en bloques de filas que repiten la
// cabecera (las dos primeras líneas en Markdown: cabecera y separador), cada
// uno de como máximo target palabras (o una sola fila si esa fila ya lo
// supera). La procedencia se ajusta: líneas del archivo (md) o filas (docx,
// xlsx: row_start/row_end, donde la fila 1 es la cabecera).
func SplitTableRows(b Block, target int) []Block {
	lines := strings.Split(b.Text, "\n")
	if b.Kind != BlockTable || len(lines) < 4 || CountTokens(b.Text) <= target {
		return []Block{b}
	}
	header := lines[:2]
	headTok := CountTokens(strings.Join(header, "\n"))
	rows := lines[2:]
	lineStart, hasLines := b.Location["line_start"]
	_, hasRows := b.Location["row_start"]
	var out []Block
	start := 0
	for start < len(rows) {
		end, tok := start, headTok
		for end < len(rows) {
			t := CountTokens(rows[end])
			if end > start && tok+t > target {
				break
			}
			tok += t
			end++
		}
		nb := b
		nb.Text = strings.Join(append(append([]string{}, header...), rows[start:end]...), "\n")
		loc := domain.SourceLocation{}
		for k, v := range b.Location {
			loc[k] = v
		}
		if hasLines {
			ls := toInt(lineStart)
			loc["line_start"], loc["line_end"] = ls+2+start, ls+2+end-1
		}
		if hasRows {
			// La fila 1 es la cabecera: las de datos empiezan en 2.
			base := toInt(b.Location["row_start"])
			loc["row_start"], loc["row_end"] = base+1+start, base+1+end-1
		}
		loc["table_part"] = len(out) + 1
		nb.Location = loc
		out = append(out, nb)
		start = end
	}
	return out
}

// Section corta los bloques en secciones: cada título abre una sección nueva;
// una sección que solo tiene su título (seguida de un subtítulo) no se emite,
// porque su título ya está en la ruta de las secciones hijas.
func Section(blocks []Block, opts SectionerOptions) []SectionDraft {
	if opts.TargetTokens <= 0 {
		opts.TargetTokens = DefaultSectionerOptions.TargetTokens
	}
	if opts.SplitTables {
		var expanded []Block
		for _, b := range blocks {
			if b.Kind == BlockTable {
				expanded = append(expanded, SplitTableRows(b, opts.TargetTokens)...)
				continue
			}
			expanded = append(expanded, b)
		}
		blocks = expanded
	}
	var out []SectionDraft
	used := map[string]int{}
	type acc struct {
		path    []string
		anchor  []string // ruta para el ancla (anchorPathOf)
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
		base := anchorFor(cur.anchor)
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
			cur = &acc{path: b.HeadingPath, anchor: anchorPathOf(b), heading: &h, part: 1}
			continue
		}
		if cur == nil {
			cur = &acc{path: b.HeadingPath, anchor: anchorPathOf(b), part: 1}
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
		out = append(out, SectionDraft{Ordinal: 1, Anchor: anchorFor(anchorPathOf(blocks[0])), HeadingPath: blocks[0].HeadingPath,
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

// anchorPathOf: la ruta de títulos con la que se calcula el ancla del bloque
// (AnchorPath si el conversor la dio; si no, HeadingPath).
func anchorPathOf(b Block) []string {
	if b.AnchorPath != nil {
		return b.AnchorPath
	}
	return b.HeadingPath
}

// anchorFor: slug de cada título de la ruta unidos por "." ("intro" si no
// hay títulos). Estable mientras no cambien los títulos.
func anchorFor(path []string) string {
	if len(path) == 0 {
		return "intro"
	}
	parts := make([]string, 0, len(path))
	for _, p := range path {
		parts = append(parts, Slugify(stripNumbering(p)))
	}
	return strings.Join(parts, ".")
}

// headingNumberRe: numeración al inicio de un título ("1.", "1.2.1.", "3)",
// "2 -"). NX-10 2b (IN-08): se quita del ancla (renumerar no cambia el
// ancla) y se conserva en el título.
var headingNumberRe = regexp.MustCompile(`^\s*(?:\d+(?:\.\d+)+\.?|\d+[.)])\s*[-–:]?\s+`)

func stripNumbering(s string) string {
	if t := headingNumberRe.ReplaceAllString(s, ""); strings.TrimSpace(t) != "" {
		return t
	}
	return s
}

// foldPool reparte transformadores de x/text: son con estado (buffers internos
// y Reset en cada transform.String), así que una sola cadena compartida entre
// goroutines produce condiciones de carrera y pánicos "slice bounds out of range".
// Cada llamada toma una cadena propia del conjunto y la devuelve al terminar.
var foldPool = sync.Pool{
	New: func() any {
		return transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	},
}

// Fold quita tildes y diéresis (á -> a, ñ -> n, ü -> u). Es seguro para uso concurrente.
func Fold(s string) string {
	t := foldPool.Get().(transform.Transformer)
	defer foldPool.Put(t)
	out, _, err := transform.String(t, s)
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
