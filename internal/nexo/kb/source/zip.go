package source

// Lectura segura de un zip subido (carpeta comprimida): nombres saneados
// (zip-slip), tope de entradas, tope de bytes descomprimidos por entrada y en
// total (zip bomb), relación de compresión máxima, sin seguir enlaces ni
// descomprimir lo que no se indexa. Nunca escribe en disco.

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
)

// ZipLimits son los límites de la lectura de un zip.
type ZipLimits struct {
	MaxEntries      int   // entradas del zip (incluidas las que no se indexan)
	MaxFileBytes    int64 // por entrada descomprimida que se lee
	MaxTotalBytes   int64 // suma de lo descomprimido que se lee
	MaxRatio        int64 // relación descomprimido/comprimido admitida para entradas > 1 MB
	IndexableSuffix func(name string) bool
}

// DefaultZipLimits: 10 000 entradas, 20 MB por archivo, 64 MB en total,
// relación 100:1.
var DefaultZipLimits = ZipLimits{MaxEntries: 10000, MaxFileBytes: 20 << 20, MaxTotalBytes: 64 << 20, MaxRatio: 100}

// ErrZipBomb indica un zip con relación de compresión o tamaño sospechosos.
var ErrZipBomb = errors.New("zip rejected: decompressed size exceeds the limits (possible zip bomb)")

// ReadZip lee las entradas de un zip. Solo descomprime las que indexable
// acepta (y los .order); las demás vuelven con Data nil para el resumen de
// archivos no indexados. Entradas con nombre inválido o cifradas se informan
// en skipped.
func ReadZip(ctx context.Context, r io.ReaderAt, size int64, lim ZipLimits) ([]File, []Skipped, error) {
	if lim.MaxEntries <= 0 {
		lim.MaxEntries = DefaultZipLimits.MaxEntries
	}
	if lim.MaxFileBytes <= 0 {
		lim.MaxFileBytes = DefaultZipLimits.MaxFileBytes
	}
	if lim.MaxTotalBytes <= 0 {
		lim.MaxTotalBytes = DefaultZipLimits.MaxTotalBytes
	}
	if lim.MaxRatio <= 0 {
		lim.MaxRatio = DefaultZipLimits.MaxRatio
	}
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, nil, domain.Errorf(domain.KindInvalid, domain.CodeInvalid, "the uploaded file is not a valid zip: %v", err)
	}
	if len(zr.File) > lim.MaxEntries {
		return nil, nil, domain.Errorf(domain.KindTooLarge, domain.CodeTooLarge, "the zip has %d entries; the limit is %d", len(zr.File), lim.MaxEntries)
	}
	var (
		files   []File
		skipped []Skipped
		total   int64
		names   = map[string]bool{}
	)
	prefix := commonRoot(zr.File)
	for _, zf := range zr.File {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if zf.FileInfo().IsDir() || strings.HasSuffix(zf.Name, "/") {
			continue
		}
		if !zf.Mode().IsRegular() {
			skipped = append(skipped, Skipped{Path: safeLabel(zf.Name), Reason: "not a regular file (links are not followed)"})
			continue
		}
		name := CleanPath(strings.TrimPrefix(strings.ReplaceAll(zf.Name, "\\", "/"), prefix))
		if name == "" {
			skipped = append(skipped, Skipped{Path: safeLabel(zf.Name), Reason: "invalid path inside the zip (absolute, .. or control characters)"})
			continue
		}
		if names[name] {
			skipped = append(skipped, Skipped{Path: name, Reason: "duplicated path inside the zip"})
			continue
		}
		names[name] = true
		base := path.Base(name)
		wanted := base == ".order" || (lim.IndexableSuffix != nil && lim.IndexableSuffix(base))
		if !wanted || hasHiddenSegment(strings.Split(path.Dir(name), "/")) {
			files = append(files, File{Path: name})
			continue
		}
		if zf.Flags&0x1 != 0 {
			skipped = append(skipped, Skipped{Path: name, Reason: "encrypted zip entry"})
			continue
		}
		if zf.UncompressedSize64 > uint64(lim.MaxFileBytes) {
			return nil, nil, domain.Errorf(domain.KindTooLarge, domain.CodeTooLarge, "%s is %d bytes once decompressed; the limit per file is %d", name, zf.UncompressedSize64, lim.MaxFileBytes)
		}
		if zf.UncompressedSize64 > 1<<20 && zf.CompressedSize64 > 0 && zf.UncompressedSize64/zf.CompressedSize64 > uint64(lim.MaxRatio) {
			return nil, nil, ErrZipBomb
		}
		rc, err := zf.Open()
		if err != nil {
			skipped = append(skipped, Skipped{Path: name, Reason: "unreadable zip entry: " + err.Error()})
			continue
		}
		data, err := io.ReadAll(io.LimitReader(rc, lim.MaxFileBytes+1))
		_ = rc.Close()
		if err != nil {
			skipped = append(skipped, Skipped{Path: name, Reason: "unreadable zip entry: " + err.Error()})
			continue
		}
		if int64(len(data)) > lim.MaxFileBytes {
			return nil, nil, domain.Errorf(domain.KindTooLarge, domain.CodeTooLarge, "%s exceeds %d bytes once decompressed", name, lim.MaxFileBytes)
		}
		total += int64(len(data))
		if total > lim.MaxTotalBytes {
			return nil, nil, domain.Errorf(domain.KindTooLarge, domain.CodeTooLarge, "the zip decompresses to more than %d bytes of indexable files", lim.MaxTotalBytes)
		}
		files = append(files, File{Path: name, Data: data})
	}
	return files, skipped, nil
}

// commonRoot: si todas las entradas cuelgan de una sola carpeta raíz (zip de
// una carpeta), se quita para que doc_key no dependa del nombre del zip.
func commonRoot(files []*zip.File) string {
	root := ""
	for _, f := range files {
		n := strings.TrimPrefix(strings.ReplaceAll(f.Name, "\\", "/"), "./")
		i := strings.Index(n, "/")
		if i <= 0 {
			if f.FileInfo().IsDir() {
				continue
			}
			return "" // un archivo en la raíz: no hay carpeta común
		}
		top := n[:i+1]
		if root == "" {
			root = top
		} else if root != top {
			return ""
		}
	}
	return root
}

func safeLabel(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, s)
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// FormatSkipped resume los avisos para el log o la CLI.
func FormatSkipped(s []Skipped) string {
	var b strings.Builder
	for _, x := range s {
		fmt.Fprintf(&b, "  %s: %s\n", x.Path, x.Reason)
	}
	return b.String()
}
