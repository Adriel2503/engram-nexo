//go:build !windows

package proc

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
func inspect(pid int) (Identity, error) {
	if pid <= 0 {
		return Identity{}, fmt.Errorf("PID inválido")
	}
	b, e := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "lstart=", "-o", "stat=", "-o", "comm=").Output()
	if e != nil {
		return Identity{}, e
	}
	f := strings.Fields(string(b))
	// Un proceso zombi (estado Z) ya terminó y solo espera a que su padre lo
	// recoja: se trata como detenido.
	if len(f) < 7 || strings.HasPrefix(f[5], "Z") {
		return Identity{}, fmt.Errorf("proceso detenido")
	}
	return Identity{PID: pid, Executable: strings.Join(f[6:], " "), Started: strings.Join(f[:5], " ")}, nil
}

func HideChild(_ *exec.Cmd) {}
