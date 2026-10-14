//go:build !windows

package preflight

import (
	"errors"

	"golang.org/x/sys/unix"
)

func disk(dir string) error {
	var s unix.Statfs_t
	if e := unix.Statfs(dir, &s); e != nil {
		return e
	}
	if s.Bavail*uint64(s.Bsize) < 256*1024*1024 {
		return errors.New("se necesitan al menos 256 MiB libres")
	}
	return nil
}
