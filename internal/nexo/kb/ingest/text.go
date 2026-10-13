package ingest

import (
	"context"
	"strings"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
)

// TextConverter trata texto plano: párrafos separados por líneas en blanco,
// sin títulos. El Markdown normalizado es el mismo texto.
type TextConverter struct{}

func (TextConverter) Format() string       { return domain.FormatText }
func (TextConverter) Extensions() []string { return []string{".txt", ".text"} }
func (TextConverter) MIMETypes() []string  { return []string{"text/plain"} }

// Convert implementa Converter.
func (TextConverter) Convert(ctx context.Context, in Input) (Result, error) {
	text := normalizeText(in.Data)
	lines := strings.Split(text, "\n")
	start := 0
	for start < len(lines) && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	end := len(lines)
	for end > start && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	var blocks []Block
	i := start
	for i < end {
		if i%512 == 0 {
			if err := ctx.Err(); err != nil {
				return Result{}, err
			}
		}
		if strings.TrimSpace(lines[i]) == "" {
			i++
			continue
		}
		j := i
		for j < end && strings.TrimSpace(lines[j]) != "" {
			j++
		}
		blocks = append(blocks, Block{Kind: BlockParagraph, Text: strings.Join(lines[i:j], "\n"), HeadingPath: []string{}, Extraction: domain.ExtractionText,
			Location: domain.SourceLocation{"line_start": i + 1, "line_end": j}})
		i = j
	}
	md := strings.Join(lines[start:end], "\n")
	if md != "" {
		md += "\n"
	}
	meta := FrontMatter{}
	if len(blocks) > 0 {
		first := strings.TrimSpace(strings.SplitN(blocks[0].Text, "\n", 2)[0])
		if len([]rune(first)) <= 120 {
			meta.Title = first
		}
	}
	return Result{Format: domain.FormatText, MIME: "text/plain", Markdown: md, Meta: meta, Blocks: blocks}, nil
}
