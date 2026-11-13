//go:build linux

package agenthost

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

func ProtectMemory() error {
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return fmt.Errorf("agent-host: mark the process not dumpable: %w", err)
	}
	dumpable, err := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
	if err != nil {
		return fmt.Errorf("agent-host: read the process's dumpable attribute: %w", err)
	}
	if dumpable != 0 {
		return errors.New("agent-host: the process is still dumpable")
	}
	return nil
}
