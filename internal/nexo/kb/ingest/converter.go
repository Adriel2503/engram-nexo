// Package ingest convierte archivos a Markdown normalizado con procedencia por
// bloque y corta el resultado en secciones citables (NX-10, design §3, §6.1 y
// "Ingesta y procedencia"). Lo usan el servidor (al publicar) y la CLI (para
// leer el front matter y validar antes de enviar).
//
// Primera entrega: Markdown y texto. Word y Excel (K2), PDF y PowerPoint (K3)
// se agregan registrando nuevos Converter por extensión y tipo MIME, sin
// cambiar el modelo: cada bloque ya trae su SourceLocation y su Extraction.
package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
)

// Clases de bloque.
const (
	BlockHeading   = "heading"
	BlockParagraph = "paragraph"
	BlockTable     = "table"
	BlockCode      = "code"
)

// Block es una unidad mínima del documento normalizado. Nunca se parte al
// seccionar (tablas y bloques de código quedan enteros).
type Block struct {
	Kind        string
	Level       int      // nivel del título (solo BlockHeading)
	Text        string   // Markdown del bloque
	HeadingPath []string // títulos que lo contienen (para un título, incluido él)
	// AnchorPath, si no es nil, reemplaza a HeadingPath solo para calcular el
	// ancla. Markdown lo usa cuando el título visible conserva '_', '*' o '`'
	// que antes se quitaban, para que las anclas ya publicadas no cambien.
	AnchorPath []string
	// Location es la procedencia en el archivo original (md: heading_path,
	// line_start, line_end; pdf: page_start, page_end; xlsx: sheet,
	// row_start, row_end; pptx: slide_start, slide_end).
	Location   domain.SourceLocation
	Extraction string // text | table | code (ocr reservado)
}

// FrontMatter son los metadatos declarados en el archivo (o en el pedido).
type FrontMatter struct {
	ID         string
	Title      string
	Category   string
	Tags       []string
	Owner      string
	ValidFrom  *time.Time
	ValidUntil *time.Time
	Extra      map[string]any
}

// Input es un archivo a convertir.
type Input struct {
	Name string // nombre o ruta (decide el conversor por extensión)
	MIME string // opcional; si viene, tiene prioridad sobre la extensión
	Data []byte
}

// Result es la salida de un conversor.
type Result struct {
	Format       string // md, txt (luego docx, xlsx, pdf, pptx)
	MIME         string
	Markdown     string // Markdown normalizado, sin front matter (content_md)
	Meta         FrontMatter
	Blocks       []Block
	SourceSHA256 string
	SourceSize   int64
}

// Limits son los límites de seguridad que se aplican a todo conversor,
// también a Markdown: tamaño de entrada, tiempo y tope de salida.
type Limits struct {
	MaxInputBytes  int64         // 0 = DefaultLimits
	Timeout        time.Duration // 0 = DefaultLimits
	MaxOutputBytes int           // Markdown normalizado
	MaxBlocks      int
	// MaxBinaryInputBytes es el tope de entrada de los formatos binarios
	// (docx). NX-10 2b.
	MaxBinaryInputBytes int64
}

// DefaultLimits: 2 MB de entrada y de salida (design §8), 10 s, 20 000
// bloques; 20 MB de entrada para formatos binarios (2b).
var DefaultLimits = Limits{MaxInputBytes: 2 << 20, Timeout: 10 * time.Second, MaxOutputBytes: 2 << 20, MaxBlocks: 20000, MaxBinaryInputBytes: 20 << 20}

func (l Limits) withDefaults() Limits {
	if l.MaxInputBytes <= 0 {
		l.MaxInputBytes = DefaultLimits.MaxInputBytes
	}
	if l.MaxBinaryInputBytes <= 0 {
		l.MaxBinaryInputBytes = DefaultLimits.MaxBinaryInputBytes
	}
	if l.Timeout <= 0 {
		l.Timeout = DefaultLimits.Timeout
	}
	if l.MaxOutputBytes <= 0 {
		l.MaxOutputBytes = DefaultLimits.MaxOutputBytes
	}
	if l.MaxBlocks <= 0 {
		l.MaxBlocks = DefaultLimits.MaxBlocks
	}
	return l
}

// Converter convierte un formato a Markdown con bloques y procedencia. Debe
// respetar ctx (el registro lo cancela al vencer Limits.Timeout).
type Converter interface {
	Format() string
	Extensions() []string // con punto: ".md"
	MIMETypes() []string
	Convert(ctx context.Context, in Input) (Result, error)
}

// Registry elige el conversor por tipo MIME o extensión y aplica los límites.
type Registry struct {
	mu     sync.RWMutex
	byExt  map[string]Converter
	byMIME map[string]Converter
	byFmt  map[string]Converter
}

// NewRegistry crea un registro vacío.
func NewRegistry() *Registry {
	return &Registry{byExt: map[string]Converter{}, byMIME: map[string]Converter{}, byFmt: map[string]Converter{}}
}

// DefaultRegistry trae los conversores de la etapa K1 (Markdown y texto).
func DefaultRegistry() *Registry {
	r := NewRegistry()
	r.Register(MarkdownConverter{})
	r.Register(TextConverter{})
	r.Register(ADOMarkdownConverter{}) // NX-10 2b (IN-08): solo por formato explícito "ado-md"
	return r
}

// textFormats son los formatos de texto: el resto (docx; luego xlsx, pdf,
// pptx) son binarios y usan Limits.MaxBinaryInputBytes como tope de entrada
// (un .docx con imágenes pesa más que su texto). El tope de salida es el mismo.
var textFormats = map[string]bool{domain.FormatMarkdown: true, domain.FormatText: true, FormatADOMarkdown: true}

// Register agrega (o reemplaza) un conversor.
func (r *Registry) Register(c Converter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byFmt[c.Format()] = c
	for _, e := range c.Extensions() {
		r.byExt[strings.ToLower(e)] = c
	}
	for _, m := range c.MIMETypes() {
		r.byMIME[strings.ToLower(m)] = c
	}
}

// Lookup devuelve el conversor para un formato explícito, un tipo MIME o el
// nombre del archivo, en ese orden.
func (r *Registry) Lookup(format, mime, name string) (Converter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if c, ok := r.byFmt[strings.ToLower(strings.TrimSpace(format))]; ok {
		return c, true
	}
	if m := strings.ToLower(strings.TrimSpace(strings.SplitN(mime, ";", 2)[0])); m != "" {
		if c, ok := r.byMIME[m]; ok {
			return c, true
		}
	}
	if c, ok := r.byExt[strings.ToLower(path.Ext(strings.ReplaceAll(name, "\\", "/")))]; ok {
		return c, true
	}
	return nil, false
}

// Supported informa si el nombre tiene un conversor registrado.
func (r *Registry) Supported(name string) bool {
	_, ok := r.Lookup("", "", name)
	return ok
}

// Convert aplica los límites y el conversor elegido.
func (r *Registry) Convert(ctx context.Context, format string, in Input, lim Limits) (Result, error) {
	lim = lim.withDefaults()
	c, ok := r.Lookup(format, in.MIME, in.Name)
	if !ok {
		return Result{}, domain.Errorf(domain.KindInvalid, domain.CodeUnsupported, "unsupported format for %q (accepted: .md, .txt and .docx)", in.Name)
	}
	maxIn := lim.MaxInputBytes
	if !textFormats[c.Format()] {
		maxIn = lim.MaxBinaryInputBytes
	}
	if int64(len(in.Data)) > maxIn {
		return Result{}, domain.Errorf(domain.KindTooLarge, domain.CodeTooLarge, "input is %d bytes; the limit is %d", len(in.Data), maxIn)
	}
	cctx, cancel := context.WithTimeout(ctx, lim.Timeout)
	defer cancel()
	type out struct {
		res Result
		err error
	}
	ch := make(chan out, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				ch <- out{err: fmt.Errorf("ingest: converter %s panicked: %v", c.Format(), p)}
			}
		}()
		res, err := c.Convert(cctx, in)
		ch <- out{res, err}
	}()
	var o out
	select {
	case o = <-ch:
	case <-cctx.Done():
		return Result{}, domain.Errorf(domain.KindInvalid, domain.CodeTooLarge, "conversion of %q exceeded %s", in.Name, lim.Timeout)
	}
	if o.err != nil {
		return Result{}, o.err
	}
	if len(o.res.Markdown) > lim.MaxOutputBytes {
		return Result{}, domain.Errorf(domain.KindTooLarge, domain.CodeTooLarge, "converted Markdown is %d bytes; the limit is %d", len(o.res.Markdown), lim.MaxOutputBytes)
	}
	if len(o.res.Blocks) > lim.MaxBlocks {
		return Result{}, domain.Errorf(domain.KindTooLarge, domain.CodeTooLarge, "converted document has %d blocks; the limit is %d", len(o.res.Blocks), lim.MaxBlocks)
	}
	sum := sha256.Sum256(in.Data)
	o.res.SourceSHA256 = hex.EncodeToString(sum[:])
	o.res.SourceSize = int64(len(in.Data))
	if o.res.Format == "" {
		o.res.Format = c.Format()
	}
	if o.res.MIME == "" {
		if m := c.MIMETypes(); len(m) > 0 {
			o.res.MIME = m[0]
		}
	}
	return o.res, nil
}

// SHA256Hex es el hash hexadecimal de un texto (content_sha256).
func SHA256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// normalizeText: sin BOM, saltos LF, sin espacios al final de línea, un salto final.
func normalizeText(data []byte) string {
	s := strings.TrimPrefix(string(data), string(rune(0xFEFF)))
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	return strings.Join(lines, "\n")
}
