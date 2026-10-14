//go:build windows

package secrets

import (
	"errors"
	"os/exec"
	"strings"
	"syscall"
)

func Protect(path string) error {
	cmd := exec.Command("whoami")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000, HideWindow: true}
	b, e := cmd.Output()
	if e != nil {
		return errors.New("no se pudo identificar al usuario para proteger el archivo")
	}
	u := strings.TrimSpace(string(b))
	cmd = exec.Command("icacls", path, "/inheritance:r", "/grant:r", u+":F")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000, HideWindow: true}
	if e = cmd.Run(); e != nil {
		return errors.New("no se pudo restringir el archivo con icacls")
	}
	return nil
}
