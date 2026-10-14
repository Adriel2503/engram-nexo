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
	b, e := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "lstart=", "-o", "comm=").Output()
	if e != nil {
		return Identity{}, e
	}
	f := strings.Fields(string(b))
	if len(f) < 6 {
		return Identity{}, fmt.Errorf("proceso detenido")
	}
	return Identity{PID: pid, Executable: strings.Join(f[5:], " "), Started: strings.Join(f[:5], " ")}, nil
}

func HideChild(_ *exec.Cmd) {}
