package ingest

// Nexo IA · NX-10 (entrega 2b, IN-08) — conversor "ado-md": Markdown de una
// wiki de Azure DevOps. Antes de pasar por el conversor de Markdown:
//
//   - quita las marcas [[_TOC_]] y [[_TOSP_]] (la tabla de contenido la arma
//     la wiki, no es contenido);
//   - convierte los bloques "::: mermaid ... :::" en bloques de código
//     ```mermaid (y cualquier otro bloque ":::" en un bloque de código con su
//     nombre: "::: video" queda como código, sin interpretar);
//   - convierte las tablas HTML (<table>) a tablas Markdown (texto plano, sin
//     ejecutar ni interpretar nada más; colspan se repite en las columnas que
//     ocupa).
//
// Las líneas reemplazadas se rellenan con líneas vacías para que line_start y
// line_end sigan apuntando a las líneas del archivo original (si una tabla HTML
// produce más filas Markdown que líneas tenía, las líneas siguientes quedan
// corridas; es raro porque las tablas HTML de la wiki ocupan una línea por
// celda).

import (
	"context"
	"regexp"
	"strings"
)

// FormatADOMarkdown es el formato de una página de wiki de Azure DevOps.
const FormatADOMarkdown = "ado-md"

// ADOMarkdownConverter convierte una página de wiki de Azure DevOps.
type ADOMarkdownConverter struct{}

func (ADOMarkdownConverter) Format() string       { return FormatADOMarkdown }
func (ADOMarkdownConverter) Extensions() []string { return nil } // solo por formato explícito
func (ADOMarkdownConverter) MIMETypes() []string  { return []string{"text/x-ado-wiki-markdown"} }

var (
	adoTOCRe        = regexp.MustCompile(`(?i)^\s*\[\[_(TOC|TOSP)_\]\]\s*$`)
	adoColonOpenRe  = regexp.MustCompile(`^\s*:::\s*([A-Za-z0-9_-]+)\s*$`)
	adoColonCloseRe = regexp.MustCompile(`^\s*:::\s*$`)
	htmlTableOpenRe = regexp.MustCompile(`(?i)<table[\s>]`)
	htmlTableEndRe  = regexp.MustCompile(`(?i)</table\s*>`)
)

// Convert implementa Converter.
func (ADOMarkdownConverter) Convert(ctx context.Context, in Input) (Result, error) {
	text := PreprocessADOMarkdown(normalizeText(in.Data))
	res, err := MarkdownConverter{}.Convert(ctx, Input{Name: in.Name, MIME: "text/markdown", Data: []byte(text)})
	if err != nil {
		return Result{}, err
	}
	res.Format, res.MIME = FormatADOMarkdown, "text/markdown"
	return res, nil
}

// PreprocessADOMarkdown aplica las transformaciones de la wiki conservando la
// cantidad de líneas siempre que sea posible.
func PreprocessADOMarkdown(text string) string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	inFence := ""
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if inFence == "" {
			if m := fenceRe.FindStringSubmatch(line); m != nil {
				inFence = m[1]
				out = append(out, line)
				continue
			}
		} else {
			if isFenceClose(line, inFence) {
				inFence = ""
			}
			out = append(out, line)
			continue
		}
		if adoTOCRe.MatchString(line) {
			out = append(out, "")
			continue
		}
		if m := adoColonOpenRe.FindStringSubmatch(line); m != nil {
			// Bloque ":::" hasta su cierre: código con el nombre del bloque.
			j := i + 1
			for j < len(lines) && !adoColonCloseRe.MatchString(lines[j]) {
				j++
			}
			if j < len(lines) {
				out = append(out, "```"+strings.ToLower(m[1]))
				out = append(out, lines[i+1:j]...)
				out = append(out, "```")
				i = j
				continue
			}
		}
		if htmlTableOpenRe.MatchString(line) {
			j := i
			for j < len(lines) && !htmlTableEndRe.MatchString(lines[j]) {
				j++
			}
			if j < len(lines) {
				raw := strings.Join(lines[i:j+1], "\n")
				md := HTMLTablesToMarkdown(raw)
				mdLines := strings.Split(md, "\n")
				span := j - i + 1
				// Una línea vacía antes y después separa la tabla del texto.
				block := append([]string{""}, mdLines...)
				block = append(block, "")
				for len(block) < span {
					block = append(block, "")
				}
				out = append(out, block...)
				i = j
				continue
			}
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// IsBlankADOPage informa si una página no tiene contenido propio (solo marcas
// de la wiki y espacios): las páginas contenedoras se saltan.
func IsBlankADOPage(data []byte) bool {
	for _, line := range strings.Split(normalizeText(data), "\n") {
		t := strings.TrimSpace(line)
		if t == "" || adoTOCRe.MatchString(t) {
			continue
		}
		return false
	}
	return true
}
