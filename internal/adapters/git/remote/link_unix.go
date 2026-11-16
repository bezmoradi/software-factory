//go:build unix

package remote

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

const noFollow = syscall.O_NOFOLLOW

func linkAt(src, dst *os.File, name string) error {
	return unix.Linkat(int(src.Fd()), name, int(dst.Fd()), name, 0)
}
