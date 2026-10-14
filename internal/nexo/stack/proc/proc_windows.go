//go:build windows

package proc

import (
	"fmt"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000208, HideWindow: true}
}
func inspect(pid int) (Identity, error) {
	h, e := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if e != nil {
		return Identity{}, e
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var code uint32
	if e = windows.GetExitCodeProcess(h, &code); e != nil || code != 259 {
		return Identity{}, fmt.Errorf("proceso detenido")
	}
	var c, x, k, u windows.Filetime
	if e = windows.GetProcessTimes(h, &c, &x, &k, &u); e != nil {
		return Identity{}, e
	}
	b := make([]uint16, 32768)
	n := uint32(len(b))
	if e = windows.QueryFullProcessImageName(h, 0, &b[0], &n); e != nil {
		return Identity{}, e
	}
	return Identity{PID: pid, Executable: windows.UTF16ToString(b[:n]), Started: fmt.Sprintf("%d:%d", c.HighDateTime, c.LowDateTime)}, nil
}

// HideChild evita que el hijo del supervisor desacoplado cree una consola nueva.
func HideChild(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000, HideWindow: true}
}
