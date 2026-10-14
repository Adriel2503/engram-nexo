//go:build windows

package preflight

import (
	"errors"

	"golang.org/x/sys/windows"
)

func disk(dir string) error {
	p, e := windows.UTF16PtrFromString(dir)
	if e != nil {
		return e
	}
	var free, total, available uint64
	if e = windows.GetDiskFreeSpaceEx(p, &available, &total, &free); e != nil {
		return errors.New("no se pudo comprobar el espacio libre")
	}
	if available < 256*1024*1024 {
		return errors.New("se necesitan al menos 256 MiB libres para preparar el entorno")
	}
	return nil
}
