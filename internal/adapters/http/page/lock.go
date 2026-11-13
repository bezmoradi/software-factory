package page

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

const (
	LockSocket     = "engine.sock"
	MaxSocketBytes = 103
	lockTimeout    = 2 * time.Second
)

var ErrEngineRunning = errors.New("another engine already uses this state folder")

func LockPath(stateDir string) (string, error) {
	p := filepath.Join(stateDir, LockSocket)
	if len(p) > MaxSocketBytes {
		return "", fmt.Errorf("the socket path is longer than %d bytes; choose a shorter state folder", MaxSocketBytes)
	}
	return p, nil
}

func LockState(stateDir string) (net.Listener, error) {
	if !ownDir(stateDir) {
		return nil, errStateDir
	}
	p, err := LockPath(stateDir)
	if err != nil {
		return nil, err
	}
	if info, err := os.Lstat(p); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("%s exists and isn't a socket", LockSocket)
		}
		if c, err := net.DialTimeout("unix", p, lockTimeout); err == nil {
			_ = c.Close()
			return nil, ErrEngineRunning
		}
		if err := os.Remove(p); err != nil {
			return nil, err
		}
	}
	ln, err := net.Listen("unix", p)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(p, 0o600); err != nil {
		_ = ln.Close()
		return nil, err
	}
	return ln, nil
}

func HoldLock(ctx context.Context, ln net.Listener) {
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		_ = c.Close()
	}
}
