//go:build unix

package agenthost

import (
	"context"
	"net/http"
	"time"

	"github.com/creack/pty"
	"github.com/hinshun/vt10x"
)

func (s *Server) answerTrust(parent context.Context, q commandJSON, answer bool) (bool, *problem) {
	ctx, cancel := context.WithTimeout(parent, trustWait)
	defer cancel()
	cmd, p := s.prepare(ctx, q)
	if p != nil {
		return false, p
	}
	cmd.SysProcAttr = nil
	cmd.Cancel = func() error { return signalGroup(cmd.Process.Pid, true) }
	cmd.WaitDelay = drainWait
	term, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: trustRows, Cols: trustCols})
	if err != nil {
		return false, fail(http.StatusUnprocessableEntity, "start", "Claude Code couldn't start in a terminal.")
	}
	defer func() {
		_ = signalGroup(cmd.Process.Pid, true)
		_ = term.Close()
		_ = cmd.Wait()
	}()
	chunks := make(chan []byte, 64)
	go func() {
		defer close(chunks)
		buf := make([]byte, 32<<10)
		for {
			n, err := term.Read(buf)
			if n > 0 {
				chunks <- append([]byte(nil), buf[:n]...)
			}
			if err != nil {
				return
			}
		}
	}()
	screen := vt10x.New(vt10x.WithSize(trustCols, trustRows))
	limit := trustAsk
	if answer {
		limit = trustWait
	}
	quiet := time.NewTimer(limit)
	defer quiet.Stop()
	stuck := func() *problem {
		return fail(http.StatusUnprocessableEntity, "trust", "Claude Code stopped taking input.")
	}
	unconfirmed := func(status int, why string) *problem {
		s.log.Info("agent-host trust", "step", "check", "ended", why)
		return fail(status, "trust", "Software Factory couldn't confirm that Claude Code trusts this folder. Trust it in a terminal instead.")
	}
	press := func(keys ...string) bool {
		for i, k := range keys {
			if i > 0 {
				time.Sleep(keyPause)
			}
			if _, err := term.Write([]byte(k)); err != nil {
				return false
			}
		}
		return true
	}
	last := screenNone
	answered := false
	for {
		select {
		case <-ctx.Done():
			if !answer {
				return false, unconfirmed(http.StatusGatewayTimeout, "timeout")
			}
			if answered {
				return true, nil
			}
			return false, fail(http.StatusGatewayTimeout, "trust", "Claude Code didn't show its trust question in time. Trust the folder in a terminal instead.")
		case <-quiet.C:
			if !answer {
				return false, unconfirmed(http.StatusGatewayTimeout, "no ready screen")
			}
			if answered {
				return true, nil
			}
			return false, fail(http.StatusGatewayTimeout, "trust", "Claude Code didn't show its trust question in time. Trust the folder in a terminal instead.")
		case chunk, ok := <-chunks:
			if !ok {
				if !answer {
					return false, unconfirmed(http.StatusUnprocessableEntity, "claude exited")
				}
				if answered {
					return true, nil
				}
				return false, fail(http.StatusUnprocessableEntity, "trust", "Claude Code ended before it asked whether you trust this folder.")
			}
			_, _ = screen.Write(chunk)
			if answered {
				quiet.Reset(trustSettle)
				continue
			}
			now := screenOf(screen.String())
			if now == last {
				continue
			}
			last = now
			switch now {
			case screenStyle, screenNotes:
				time.Sleep(keyPause)
				if !press("\r") {
					return false, stuck()
				}
			case screenQuestion:
				if !answer {
					return true, nil
				}
				time.Sleep(keyPause)
				if !press("\x1b[B", "\r") {
					return false, stuck()
				}
				answered = true
				quiet.Reset(trustSettle)
			case screenReady:
				return false, nil
			case screenUnknown:
				if !answer {
					return false, unconfirmed(http.StatusUnprocessableEntity, "unknown question")
				}
				return false, fail(http.StatusUnprocessableEntity, "trust", "Claude Code showed a question Software Factory doesn't know, so it answered nothing. Trust the folder in a terminal instead.")
			}
		}
	}
}
