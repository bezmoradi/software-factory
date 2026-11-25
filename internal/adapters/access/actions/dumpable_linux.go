//go:build linux

package actions

import (
	"errors"
	"syscall"
)

func undumpable() error {
	if _, _, e := syscall.RawSyscall(syscall.SYS_PRCTL, syscall.PR_SET_DUMPABLE, 0, 0); e != 0 {
		return errors.New("the run's ssh agent couldn't make itself unreadable: " + e.Error())
	}
	if got, _, e := syscall.RawSyscall(syscall.SYS_PRCTL, syscall.PR_GET_DUMPABLE, 0, 0); e != 0 || got != 0 {
		return errors.New("the run's ssh agent is still readable by the command")
	}
	return nil
}
