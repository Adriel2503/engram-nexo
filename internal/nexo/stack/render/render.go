// Package render genera archivos del entorno desde plantillas embebidas.
package render

import (
	"bytes"
	"embed"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/secrets"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/state"
)

//go:embed templates/*
var templates embed.FS

type Access struct {
	Username string
	Role     string
	Token    string
}
type Data struct {
	State       state.State
	Env         map[string]string
	Access      []Access
	TwoAccounts bool
}

func Template(name string, d Data) ([]byte, error) {
	s, e := templates.ReadFile("templates/" + name)
	if e != nil {
		return nil, e
	}
	t, e := template.New(name).Parse(string(s))
	if e != nil {
		return nil, e
	}
	var b bytes.Buffer
	e = t.Execute(&b, d)
	return b.Bytes(), e
}
func Files(d Data) error {
	for _, n := range []string{"compose.yaml", "Caddyfile", "01-cuentas.sh"} {
		if n != "compose.yaml" && !d.TwoAccounts && d.State.Mode != "prod" {
			continue
		}
		b, e := Template(n, d)
		if e != nil {
			return e
		}
		p := filepath.Join(d.State.Dir, n)
		if n == "01-cuentas.sh" {
			p = filepath.Join(d.State.Dir, "postgres-init", n)
		}
		if e = secrets.Write(p, b); e != nil {
			return e
		}
	}
	return WriteEnv(d.State.Dir, d.Env)
}
func WriteEnv(dir string, m map[string]string) error {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		v := m[k]
		if strings.ContainsAny(v, "\r\n\x00'") {
			return errors.New("un valor de configuración contiene caracteres no admitidos")
		}
		b.WriteString(k + "='" + v + "'\n")
	}
	return secrets.Write(filepath.Join(dir, ".env"), []byte(b.String()))
}
func ReadEnv(dir string) (map[string]string, error) {
	b, e := os.ReadFile(filepath.Join(dir, ".env"))
	if e != nil {
		return nil, e
	}
	m := map[string]string{}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		if !ok || len(v) < 2 || v[0] != '\'' || v[len(v)-1] != '\'' {
			return nil, errors.New(".env inválido")
		}
		m[k] = v[1 : len(v)-1]
	}
	return m, nil
}
func AccessFile(d Data) error {
	b, e := Template("accesos.md", d)
	if e != nil {
		return e
	}
	return secrets.Write(filepath.Join(d.State.Dir, "accesos.md"), b)
}
func ReadAccess(dir string) ([]Access, error) {
	b, e := os.ReadFile(filepath.Join(dir, "accesos.md"))
	if e != nil {
		return nil, e
	}
	var a []Access
	for _, l := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(l, "| ") {
			continue
		}
		f := strings.Split(l, "|")
		if len(f) == 5 && strings.HasPrefix(strings.TrimSpace(f[3]), "egc_") {
			a = append(a, Access{strings.TrimSpace(f[1]), strings.TrimSpace(f[2]), strings.TrimSpace(f[3])})
		}
	}
	return a, nil
}
