// Package preflight comprueba requisitos sin crear recursos.
package preflight

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
)

type Runner func(context.Context, string, ...string) ([]byte, error)

func Exec(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}
func Docker(ctx context.Context, run Runner) error {
	if _, e := run(ctx, "docker", "version", "--format", "{{.Server.Version}}"); e != nil {
		return errors.New("Docker no responde: inicia el motor y comprueba sus permisos, o usa --database-url")
	}
	if _, e := run(ctx, "docker", "compose", "version", "--short"); e != nil {
		return errors.New("falta Docker Compose v2: instala el complemento docker compose")
	}
	return nil
}
func CheckBind(bind string, ack bool) error {
	ip := net.ParseIP(bind)
	if ip == nil {
		return errors.New("--bind debe ser una dirección IP")
	}
	if !ip.IsLoopback() && !ack {
		return errors.New("para exponer el entorno usa --bind y --i-understand-this-is-a-demo")
	}
	return nil
}
func Port(bind string, start int) (int, error) {
	if start < 1 || start > 65535 {
		return 0, errors.New("puerto fuera de rango")
	}
	for p := start; p < start+10 && p <= 65535; p++ {
		l, e := net.Listen("tcp", net.JoinHostPort(bind, strconv.Itoa(p)))
		if e == nil {
			_ = l.Close()
			return p, nil
		}
	}
	return 0, fmt.Errorf("no hay puerto libre entre %d y %d: detén el servicio que lo ocupa o indica otro puerto", start, start+9)
}
func Disk(dir string) error {
	if e := os.MkdirAll(dir, 0700); e != nil {
		return errors.New("no se puede crear la carpeta de estado")
	}
	return disk(dir)
}
