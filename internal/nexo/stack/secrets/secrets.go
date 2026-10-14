// Package secrets genera y protege credenciales del entorno; nunca las registra.
package secrets

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/redact"
)

func Hex(n int) (string, error) {
	if n < 32 {
		return "", errors.New("el secreto requiere al menos 32 bytes")
	}
	b := make([]byte, n)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return hex.EncodeToString(b), nil
}

// Write protege el temporal antes de escribir y lo reemplaza en la misma carpeta.
func Write(path string, b []byte) error {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".privado-*")
	if e != nil {
		return e
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	if e = f.Chmod(0600); e == nil {
		e = Protect(name)
	}
	if e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	return os.Rename(name, path)
}
func Clean(s string, values []string) string {
	for _, v := range values {
		if v != "" {
			s = strings.ReplaceAll(s, v, "[secreto oculto]")
		}
	}
	return redact.Text(s)
}

// Writer retiene líneas completas: una clave repartida entre Write nunca sale parcialmente.
type Writer struct {
	mu       sync.Mutex
	out      io.Writer
	values   []string
	pending  []byte
	overflow bool
}

func NewWriter(out io.Writer, values []string) *Writer { return &Writer{out: out, values: values} }
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			if len(w.pending)+len(p) > 1024*1024 {
				w.pending = nil
				w.overflow = true
			} else if !w.overflow {
				w.pending = append(w.pending, p...)
			}
			break
		}
		if !w.overflow {
			w.pending = append(w.pending, p[:i]...)
		}
		if e := w.flush(); e != nil {
			return 0, e
		}
		p = p[i+1:]
	}
	return n, nil
}
func (w *Writer) flush() error {
	s := "[línea demasiado larga omitida]"
	if !w.overflow {
		s = Clean(string(w.pending), w.values)
	}
	w.pending = nil
	w.overflow = false
	_, e := io.WriteString(w.out, s+"\n")
	return e
}
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pending) > 0 || w.overflow {
		return w.flush()
	}
	return nil
}
