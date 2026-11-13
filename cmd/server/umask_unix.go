//go:build unix

package server

import (
	"fmt"
	"strconv"
	"syscall"
)

func ApplyUmask(getenv func(string) string) error {
	v := getenv("SF_UMASK")
	if v == "" {
		return nil
	}
	n, err := strconv.ParseUint(v, 8, 32)
	if err != nil || n&^0o077 != 0 || n&0o070 > 0o020 {
		return fmt.Errorf("SF_UMASK must be an octal umask that keeps the group able to read, such as 0002 or 0022, got %q", v)
	}
	syscall.Umask(int(n))
	return nil
}
