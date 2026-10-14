// Package proc administra únicamente procesos registrados por PID e identidad.
package proc

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/secrets"
)

type Identity struct {
	PID        int
	Executable string
	Started    string
}

func Save(path string, id Identity) error {
	b, e := json.Marshal(id)
	if e != nil {
		return e
	}
	return secrets.Write(path, b)
}
func Read(path string) (Identity, error) {
	var id Identity
	b, e := os.ReadFile(path)
	if e == nil {
		e = json.Unmarshal(b, &id)
	}
	return id, e
}
func Inspect(pid int) (Identity, error) { return inspect(pid) }
func Alive(id Identity) bool            { now, e := inspect(id.PID); return e == nil && same(id, now) }
func same(a, b Identity) bool {
	return a.PID > 0 && a.PID == b.PID && a.Started == b.Started && strings.EqualFold(filepath.Clean(a.Executable), filepath.Clean(b.Executable))
}
func Stop(id Identity) error {
	base := strings.ToLower(filepath.Base(id.Executable))
	if base != "engram" && base != "engram.exe" {
		return errors.New("se rechaza detener un proceso que no sea engram")
	}
	now, e := inspect(id.PID)
	if e != nil {
		return nil
	}
	if !same(id, now) {
		return errors.New("el PID pertenece a otro proceso: no se detuvo")
	}
	p, e := os.FindProcess(id.PID)
	if e != nil {
		return e
	}
	return p.Kill()
}
func Start(bin string, args, env []string, pidPath string) (Identity, error) {
	base := strings.ToLower(filepath.Base(bin))
	if base != "engram" && base != "engram.exe" {
		return Identity{}, errors.New("el binario debe llamarse engram o engram.exe para verificar su identidad")
	}
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	detach(cmd)
	if e := cmd.Start(); e != nil {
		return Identity{}, errors.New("no se pudo lanzar el servidor")
	}
	id, e := inspect(cmd.Process.Pid)
	if e == nil {
		e = Save(pidPath, id)
	}
	if e != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return Identity{}, e
	}
	_ = cmd.Process.Release()
	return id, nil
}
