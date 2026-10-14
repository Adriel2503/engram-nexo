// Package state persiste metadatos públicos del entorno, nunca sus credenciales.
package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/secrets"
)

type State struct {
	Mode            string
	Name            string
	Project         string
	Dir             string
	Bind            string
	Port            int
	PGPort          int
	ServerURL       string
	Version         string
	ExternalDB      bool
	Prepared        bool
	UsersReady      bool
	ExampleLoaded   bool
	Embedder        string
	Reranker        string
	R1              string
	Image           string
	Subnet, ProxyIP string
}

var namePattern = regexp.MustCompile("^[a-z0-9][a-z0-9-]{0,31}$")

func Directory(home, mode, name, override string) (string, error) {
	if mode != "demo" && mode != "dev" && mode != "prod" {
		return "", errors.New("indica --mode demo, dev o prod")
	}
	if name != "" && !namePattern.MatchString(name) {
		return "", errors.New("--name admite letras minúsculas, números y guiones (hasta 32)")
	}
	if override != "" {
		return filepath.Abs(override)
	}
	if home == "" {
		u, e := os.UserHomeDir()
		if e != nil {
			return "", e
		}
		home = filepath.Join(u, ".nexo-stack")
	}
	leaf := mode
	if name != "" {
		leaf += "-" + name
	}
	return filepath.Abs(filepath.Join(home, leaf))
}
func Project(mode, name string) string {
	p := "nexo-stack-" + mode
	if name != "" {
		p += "-" + name
	}
	return p
}
func Save(s State) error {
	b, e := json.MarshalIndent(s, "", "  ")
	if e != nil {
		return e
	}
	return secrets.Write(filepath.Join(s.Dir, "estado.json"), b)
}
func Load(dir string) (State, error) {
	var s State
	b, e := os.ReadFile(filepath.Join(dir, "estado.json"))
	if e != nil {
		return s, e
	}
	if e = json.Unmarshal(b, &s); e != nil {
		return s, errors.New("estado.json inválido")
	}
	if s.Project != Project(s.Mode, s.Name) || s.Dir != dir {
		return s, errors.New("el estado no pertenece a este entorno")
	}
	return s, nil
}

// Lock impide dos arranques o borrados concurrentes. Un corte exige revisar el bloqueo.
func Lock(dir string) (func(), error) {
	if e := os.MkdirAll(dir, 0700); e != nil {
		return nil, e
	}
	p := filepath.Join(dir, ".operacion")
	f, e := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return nil, errors.New("hay otra operación o un bloqueo pendiente: revisa .operacion antes de reintentar")
	}
	_ = f.Close()
	return func() { _ = os.Remove(p) }, nil
}
