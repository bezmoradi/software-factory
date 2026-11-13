//go:build !unix

package agenthost

import (
	"errors"
	"os"
	"os/exec"
)

func ownGroup(*exec.Cmd) {}

func signalGroup(pid int, _ bool) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return nil
	}
	if err := p.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}
