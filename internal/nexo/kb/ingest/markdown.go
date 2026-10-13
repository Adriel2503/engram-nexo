package ingest

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
)

// MarkdownConverter normaliza Markdown (subconjunto de CommonMark suficiente
// para lineamientos: títulos ATX y setext, párrafos y listas, tablas GFM,
// bloques de código con ``` o ~~~) y lee el front matter YAML.
type MarkdownConverter struct{}

func (MarkdownConverter) Format() string       { return domain.FormatMarkdown }
func (MarkdownConverter) Extensions() []string { return []string{".md", ".markdown", ".mdx"} }
func (MarkdownConverter) MIMETypes() []string {
	return []string{"text/markdown", "text/x-markdown"}
}

var (
	atxRe       = regexp.MustCompile(`^ {0,3}(#{1,6})(?:[ \t]+(.*?))?[ \t]*$`)
	atxCloseRe  = regexp.MustCompile(`[ \t]+#+$`)
	fenceRe     = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})")
	setext1Re   = regexp.MustCompile(`^ {0,3}=+[ \t]*$`)
	setext2Re   = regexp.MustCompile(`^ {0,3}-+[ \t]*$`)
	tableSepRe  = regexp.MustCompile(`^\s*\|?\s*:?-{1,}:?\s*(\|\s*:?-{1,}:?\s*)*\|?\s*$`)
	frontOpenRe = regexp.MustCompile(`^---[ \t]*$`)
	frontEndRe  = regexp.MustCompile(`^(---|\.\.\.)[ \t]*$`)
)

// Convert implementa Converter.
func (MarkdownConverter) Convert(ctx context.Context, in Input) (Result, error) {
	text := normalizeText(in.Data)
	lines := strings.Split(text, "\n")
	meta, bodyStart, err := parseFrontMatter(lines)
	if err != nil {
		return Result{}, err
	}
	body := lines[bodyStart:]
	// Quitar líneas en blanco iniciales y finales del cuerpo.
	for len(body) > 0 && strings.TrimSpace(body[0]) == "" {
		body, bodyStart = body[1:], bodyStart+1
	}
	for len(body) > 0 && strings.TrimSpace(body[len(body)-1]) == "" {
		body = body[:len(body)-1]
	}
	blocks, err := parseMarkdownBlocks(ctx, body, bodyStart)
	if err != nil {
		return Result{}, err
	}
	md := strings.Join(body, "\n")
	if md != "" {
		md += "\n"
	}
	if meta.Title == "" {
		for _, b := range blocks {
			if b.Kind == BlockHeading && b.Level == 1 {
				meta.Title = b.HeadingPath[len(b.HeadingPath)-1]
				break
			}
		}
	}
	return Result{Format: domain.FormatMarkdown, MIME: "text/markdown", Markdown: md, Meta: meta, Blocks: blocks}, nil
}

// parseFrontMatter lee el bloque YAML inicial (--- ... --- o ...).
func parseFrontMatter(lines []string) (FrontMatter, int, error) {
	if len(lines) == 0 || !frontOpenRe.MatchString(lines[0]) {
		return FrontMatter{}, 0, nil
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if frontEndRe.MatchString(lines[i]) {
			end = i
			break
		}
	}
	if end < 0 {
		return FrontMatter{}, 0, nil // no es front matter: '---' es una regla horizontal
	}
	raw := map[string]any{}
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &raw); err != nil {
		return FrontMatter{}, 0, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "front matter is not valid YAML: %v", err)
	}
	fm, err := FrontMatterFromMap(raw)
	return fm, end + 1, err
}

// FrontMatterFromMap interpreta las claves conocidas: id, title, category,
// tags, owner, valid_from, valid_until. El resto queda en Extra.
func FrontMatterFromMap(raw map[string]any) (FrontMatter, error) {
	fm := FrontMatter{Extra: map[string]any{}}
	for k, v := range raw {
		key := strings.ToLower(strings.TrimSpace(k))
		switch key {
		case "id", "doc_key":
			fm.ID = scalarString(v)
		case "title", "titulo", "título":
			fm.Title = scalarString(v)
		case "category", "categoria", "categoría":
			fm.Category = strings.ToLower(scalarString(v))
		case "owner", "responsable":
			fm.Owner = scalarString(v)
		case "tags", "etiquetas":
			fm.Tags = stringList(v)
		case "valid_from", "vigente_desde":
			t, err := parseDate(v)
			if err != nil {
				return fm, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "valid_from: %v", err)
			}
			fm.ValidFrom = t
		case "valid_until", "vigente_hasta":
			t, err := parseDate(v)
			if err != nil {
				return fm, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "valid_until: %v", err)
			}
			fm.ValidUntil = t
		default:
			fm.Extra[key] = v
		}
	}
	return fm, nil
}

func scalarString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(t)
	case time.Time:
		return t.Format("2006-01-02")
	default:
		return strings.TrimSpace(fmt.Sprint(t))
	}
}

func stringList(v any) []string {
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	switch t := v.(type) {
	case []any:
		for _, x := range t {
			add(scalarString(x))
		}
	case string:
		for _, x := range strings.Split(t, ",") {
			add(x)
		}
	case nil:
	default:
		add(scalarString(t))
	}
	return out
}

// ParseDate acepta 2026-11-01, 2026-11-01T10:00:00Z (RFC 3339) o un time.Time.
func ParseDate(s string) (*time.Time, error) { return parseDate(s) }

func parseDate(v any) (*time.Time, error) {
	switch t := v.(type) {
	case nil:
		return nil, nil
	case time.Time:
		u := t.UTC()
		return &u, nil
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return nil, nil
		}
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"} {
			if p, err := time.Parse(layout, s); err == nil {
				u := p.UTC()
				return &u, nil
			}
		}
		return nil, fmt.Errorf("unrecognized date %q (use YYYY-MM-DD or RFC 3339)", s)
	}
	return nil, fmt.Errorf("unrecognized date %v", v)
}

// parseMarkdownBlocks recorre las líneas del cuerpo. offset es el índice (base
// 0) de la primera línea del cuerpo en el archivo original.
func parseMarkdownBlocks(ctx context.Context, lines []string, offset int) ([]Block, error) {
	var blocks []Block
	var stack []headingEntry
	path := func() []string {
		out := make([]string, len(stack))
		for i, h := range stack {
			out[i] = h.title
		}
		return out
	}
	emit := func(kind string, start, end int, extraction string) {
		text := strings.Join(lines[start:end+1], "\n")
		hp := path()
		blocks = append(blocks, Block{Kind: kind, Text: text, HeadingPath: hp, Extraction: extraction,
			Location: domain.SourceLocation{"heading_path": hp, "line_start": start + offset + 1, "line_end": end + offset + 1}})
	}
	pushHeading := func(level int, title string, start, end int) {
		for len(stack) > 0 && stack[len(stack)-1].level >= level {
			stack = stack[:len(stack)-1]
		}
		stack = append(stack, headingEntry{level: level, title: title})
		hp := path()
		blocks = append(blocks, Block{Kind: BlockHeading, Level: level, Text: strings.Join(lines[start:end+1], "\n"), HeadingPath: hp, Extraction: domain.ExtractionText,
			Location: domain.SourceLocation{"heading_path": hp, "line_start": start + offset + 1, "line_end": end + offset + 1}})
	}

	i := 0
	for i < len(lines) {
		if i%512 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			i++
			continue
		}
		// Bloque de código cercado: entero hasta el cierre (o el final).
		if m := fenceRe.FindStringSubmatch(line); m != nil {
			fence := m[1]
			j := i + 1
			for j < len(lines) && !isFenceClose(lines[j], fence) {
				j++
			}
			end := j
			if end >= len(lines) {
				end = len(lines) - 1
			}
			emit(BlockCode, i, end, domain.ExtractionCode)
			i = end + 1
			continue
		}
		// Título ATX.
		if m := atxRe.FindStringSubmatch(line); m != nil {
			title := strings.TrimSpace(atxCloseRe.ReplaceAllString(m[2], ""))
			if title == "" {
				title = strings.TrimSpace(m[2])
			}
			pushHeading(len(m[1]), cleanInline(title), i, i)
			i++
			continue
		}
		// Tabla GFM: cabecera con '|' y fila separadora.
		if strings.Contains(line, "|") && i+1 < len(lines) && tableSepRe.MatchString(lines[i+1]) && strings.Contains(lines[i+1], "-") {
			j := i + 2
			for j < len(lines) && strings.TrimSpace(lines[j]) != "" && strings.Contains(lines[j], "|") {
				j++
			}
			emit(BlockTable, i, j-1, domain.ExtractionTable)
			i = j
			continue
		}
		// Párrafo o lista: líneas seguidas hasta una en blanco, un título, un
		// cerco o una tabla. Un párrafo de una línea seguido de === o --- es un
		// título setext.
		if i+1 < len(lines) && (setext1Re.MatchString(lines[i+1]) || (setext2Re.MatchString(lines[i+1]) && !isListItem(line))) {
			level := 1
			if setext2Re.MatchString(lines[i+1]) {
				level = 2
			}
			pushHeading(level, cleanInline(strings.TrimSpace(line)), i, i+1)
			i += 2
			continue
		}
		j := i + 1
		for j < len(lines) {
			l := lines[j]
			if strings.TrimSpace(l) == "" || atxRe.MatchString(l) || fenceRe.MatchString(l) {
				break
			}
			if strings.Contains(l, "|") && j+1 < len(lines) && tableSepRe.MatchString(lines[j+1]) && strings.Contains(lines[j+1], "-") {
				break
			}
			j++
		}
		emit(BlockParagraph, i, j-1, domain.ExtractionText)
		i = j
	}
	return blocks, nil
}

type headingEntry struct {
	level int
	title string
}

// isFenceClose: la misma marca (``` o ~~~), al menos tan larga como la de
// apertura, sin texto después.
func isFenceClose(line, fence string) bool {
	t := strings.TrimSpace(line)
	if len(t) < len(fence) || len(line)-len(strings.TrimLeft(line, " ")) > 3 {
		return false
	}
	return strings.Trim(t, fence[:1]) == ""
}

var listItemRe = regexp.MustCompile(`^\s*([-*+]|\d+[.)])\s+`)

func isListItem(line string) bool { return listItemRe.MatchString(line) }

var inlineRe = regexp.MustCompile("[*_`]+")

// cleanInline quita marcas de énfasis y código de un título.
func cleanInline(s string) string {
	s = inlineRe.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}
