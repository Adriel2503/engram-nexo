// Package source arma los documentos a publicar a partir de archivos sueltos
// o de una carpeta subida (NX-10 entrega 2b, design §17.7 (a)): saneamiento
// de nombres, detección de tipo por contenido, lectura segura de zip y
// adaptadores de fuente. Una fuente decide la identidad (doc_key), el título,
// la ruta de títulos de la carpeta y el estado sugerido de cada documento
// (Hint); el conversor (ingest.Registry) decide cómo se lee cada formato.
//
// Fuentes:
//
//	dir                 archivos sueltos o una carpeta (.md, .txt, .docx)
//	azure-devops-wiki   wiki de Azure DevOps (se detecta por los archivos .order), IN-08
//
// No toca el sistema de archivos: todo se hace en memoria sobre []File.
package source

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/ingest"
)

// Clases de fuente.
const (
	KindAuto  = "auto"
	KindDir   = "dir"
	KindADO   = "azure-devops-wiki"
	FormatDOC = "docx"
)

// File es un archivo recibido (ruta relativa con "/").
type File struct {
	Path string
	Data []byte
}

// Hint es el estado sugerido por la fuente (KP-11).
type Hint struct {
	Retire     bool       // publicar y dejar retirado
	Note       string     // motivo (retiro o vencimiento)
	ValidFrom  *time.Time // vigencia sugerida (vencido: en el pasado)
	ValidUntil *time.Time
	Tags       []string
}

// Doc es un documento listo para convertir y publicar.
type Doc struct {
	DocKey     string
	Path       string   // ruta en la subida
	Title      string   // título sugerido (el front matter manda)
	PathTitles []string // ruta de títulos de la carpeta (RC-03)
	Format     string   // formato explícito para el registro ("" = por extensión)
	Data       []byte
	SourceURL  string // enlace a la fuente (RC-08)
	Order      int    // posición según .order (0 = sin orden)
	Hint       Hint
}

// Skipped es un archivo que no se publica, con el motivo (aviso en el lote).
type Skipped struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Options configura una lectura.
type Options struct {
	Kind     string // auto (por defecto), dir, azure-devops-wiki
	Registry *ingest.Registry
	// WikiURL es la URL de la wiki de Azure DevOps
	// (https://dev.azure.com/org/proyecto/_wiki/wikis/nombre); arma el enlace
	// de cada página.
	WikiURL string
	// Vigencia de las carpetas "≤-AAAA" de la wiki (vencidas). Por defecto,
	// desde LegacyFrom (2021-11-01, inicio de la wiki) hasta el 1 de enero
	// del año siguiente al indicado en el nombre.
	LegacyFrom time.Time
}

// Result es la lectura de una subida.
type Result struct {
	Kind    string    `json:"source"`
	Docs    []Doc     `json:"-"`
	Skipped []Skipped `json:"skipped"`
}

// Detect elige la fuente: azure-devops-wiki si hay archivos .order.
func Detect(files []File) string {
	for _, f := range files {
		if path.Base(f.Path) == ".order" {
			return KindADO
		}
	}
	return KindDir
}

// Walk lee los archivos con la fuente pedida (o detectada).
func Walk(ctx context.Context, files []File, opts Options) (Result, error) {
	if opts.Registry == nil {
		opts.Registry = ingest.DefaultRegistry()
	}
	kind := strings.TrimSpace(strings.ToLower(opts.Kind))
	if kind == "" || kind == KindAuto {
		kind = Detect(files)
	}
	switch kind {
	case KindDir:
		return walkDir(ctx, files, opts)
	case KindADO:
		return walkADO(ctx, files, opts)
	}
	return Result{}, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "unknown source %q (use auto, dir or azure-devops-wiki)", opts.Kind)
}

// ─── Nombres ─────────────────────────────────────────────────────────────────

// MaxPathBytes y MaxPathDepth acotan los nombres recibidos.
const (
	MaxPathBytes = 512
	MaxPathDepth = 24
)

// CleanPath sanea una ruta recibida (nombre de archivo de un formulario o de
// una entrada de zip): barras normales, NFC, sin caracteres de control, sin
// raíz, sin "." ni ".." (zip-slip), sin segmentos vacíos. Devuelve "" si la
// ruta no es aceptable.
func CleanPath(p string) string {
	p = norm.NFC.String(strings.ReplaceAll(p, "\\", "/"))
	if !utf8.ValidString(p) || len(p) > MaxPathBytes {
		return ""
	}
	for _, r := range p {
		if unicode.IsControl(r) {
			return ""
		}
	}
	if strings.HasPrefix(p, "/") || (len(p) > 1 && p[1] == ':') {
		return "" // absoluta o con unidad de Windows
	}
	parts := strings.Split(p, "/")
	out := parts[:0]
	for _, s := range parts {
		s = strings.TrimSpace(s)
		switch s {
		case "", ".":
			continue
		case "..":
			return ""
		}
		out = append(out, s)
	}
	if len(out) == 0 || len(out) > MaxPathDepth {
		return ""
	}
	return strings.Join(out, "/")
}

// DocKeyFromPath arma un doc_key válido (domain.ValidDocKey) desde una ruta:
// cada segmento pasa a slug (minúsculas, sin tildes, letras, números y
// guiones), sin extensión. Si queda demasiado largo, se recorta y se agrega un
// sufijo estable.
func DocKeyFromPath(p string) string {
	p = strings.TrimSuffix(p, path.Ext(p))
	var segs []string
	for _, s := range strings.Split(p, "/") {
		if s == "" {
			continue
		}
		segs = append(segs, ingest.Slugify(decodeName(s)))
	}
	key := strings.Join(segs, "/")
	if len(key) > 200 {
		sum := ingest.SHA256Hex(key)[:10]
		key = strings.TrimRight(key[:189], "/-.") + "-" + sum
	}
	return key
}

// decodeName decodifica un nombre de la wiki (%2D, %7C...); si no es válido,
// lo deja igual.
func decodeName(s string) string {
	if d, err := url.PathUnescape(s); err == nil {
		return d
	}
	return s
}

// TitleFromName: el título de una página desde su nombre de archivo (sin
// extensión): el guion es un espacio y luego se decodifican %2D (guion
// literal), %7C, etc.
func TitleFromName(name string) string {
	name = strings.TrimSuffix(name, path.Ext(name))
	t := decodeName(strings.ReplaceAll(name, "-", " "))
	return strings.Join(strings.Fields(t), " ")
}

// ─── Tipo por contenido ──────────────────────────────────────────────────────

// Sniff comprueba que el contenido coincide con la extensión (no se confía en
// el nombre ni en el Content-Type del cliente). Devuelve el motivo del
// rechazo o "".
func Sniff(name string, data []byte) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".md", ".markdown", ".mdx", ".txt", ".text", ".order":
		if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})) {
			return "the content is not UTF-8 text"
		}
	case ".docx":
		if !bytes.HasPrefix(data, []byte("PK\x03\x04")) {
			return "the content is not a .docx (Office Open XML) file"
		}
	}
	return ""
}

// ─── Fuente dir ──────────────────────────────────────────────────────────────

func walkDir(ctx context.Context, files []File, opts Options) (Result, error) {
	res := Result{Kind: KindDir}
	seen := map[string]string{}
	ignored := map[string]int{}
	sorted := sortedFiles(files)
	for _, f := range sorted {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		base := path.Base(f.Path)
		if strings.HasPrefix(base, ".") || strings.EqualFold(base, "nexo-kb.yaml") || hasHiddenSegment(strings.Split(path.Dir(f.Path), "/")) {
			continue
		}
		if !opts.Registry.Supported(base) || f.Data == nil {
			ignored[ignoredKind(f.Path)]++
			continue
		}
		if why := Sniff(base, f.Data); why != "" {
			res.Skipped = append(res.Skipped, Skipped{Path: f.Path, Reason: why})
			continue
		}
		key := DocKeyFromPath(f.Path)
		if prev, dup := seen[key]; dup {
			res.Skipped = append(res.Skipped, Skipped{Path: f.Path, Reason: "same doc_key as " + prev})
			continue
		}
		seen[key] = f.Path
		dir := path.Dir(f.Path)
		var titles []string
		if dir != "." {
			for _, s := range strings.Split(dir, "/") {
				titles = append(titles, TitleFromName(s))
			}
		}
		res.Docs = append(res.Docs, Doc{DocKey: key, Path: f.Path, Title: TitleFromName(base), PathTitles: titles, Data: f.Data})
	}
	res.Skipped = append(res.Skipped, summarizeIgnored(ignored)...)
	return res, nil
}

func summarizeIgnored(ignored map[string]int) []Skipped {
	kinds := make([]string, 0, len(ignored))
	for k := range ignored {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	out := make([]Skipped, 0, len(kinds))
	for _, k := range kinds {
		out = append(out, Skipped{Path: k, Reason: fmt.Sprintf("%d files not indexed (unsupported type or attachment)", ignored[k])})
	}
	return out
}

func sortedFiles(files []File) []File {
	out := append([]File(nil), files...)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// ErrEmpty indica que no hay nada que publicar.
var ErrEmpty = fmt.Errorf("no indexable files")
