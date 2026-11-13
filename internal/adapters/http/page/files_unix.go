//go:build unix

package page

import (
	"os"
	"syscall"
)

const openFlags = os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
