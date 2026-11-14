package agenthost

import (
	"context"
	"errors"
	"net/http"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	projectCommandRoute = "/v1/project-command"
	maxProjectCommands  = 4
	projectShell        = "/bin/sh"
)

type projectCommandJSON struct {
	Cwd     string `json:"cwd"`
	Kind    string `json:"kind"`
	Command string `json:"command"`
}

type projectCommandReply struct {
	Code     int    `json:"code"`
	Tail     string `json:"tail"`
	Cut      bool   `json:"cut,omitempty"`
	TimedOut bool   `json:"timedOut,omitempty"`
}

type outputTail struct {
	buf   []byte
	limit int
	cut   bool
}

func (t *outputTail) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.limit; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
		t.cut = true
	}
	return len(p), nil
}

func projectCommandFor(kind string) (time.Duration, bool) {
	switch kind {
	case session.CommandCheck:
		return session.CheckFor, true
	case session.CommandSetup:
		return session.SetupFor, true
	}
	return 0, false
}

func (s *Server) projectCommand(w http.ResponseWriter, r *http.Request) {
	var q projectCommandJSON
	if p := decode(w, r, &q); p != nil {
		refuse(w, p)
		return
	}
	limit, ok := projectCommandFor(q.Kind)
	if !ok {
		refuse(w, fail(http.StatusBadRequest, "kind", "A project command is the check or the setup command."))
		return
	}
	if _, ok := session.CheckProjectCommand(q.Command); !ok || q.Command == "" {
		refuse(w, fail(http.StatusBadRequest, "command", "A project command is one line of at most 300 bytes."))
		return
	}
	s.mu.Lock()
	if s.commands >= maxProjectCommands {
		s.mu.Unlock()
		refuse(w, fail(http.StatusTooManyRequests, "full", "agent-host runs as many project commands as it allows."))
		return
	}
	s.commands++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.commands--
		s.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(r.Context(), limit)
	defer cancel()
	cmd, p := s.prepare(ctx, commandJSON{Argv: []string{projectShell, "-c", q.Command}, Cwd: q.Cwd})
	if p != nil {
		refuse(w, p)
		return
	}
	out := &outputTail{limit: session.MaxCommandTail}
	cmd.Stdout, cmd.Stderr = out, out
	cmd.Cancel = func() error { return signalGroup(cmd.Process.Pid, true) }
	cmd.WaitDelay = drainWait
	err := cmd.Run()
	code := -1
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) && !errors.Is(err, exec.ErrWaitDelay) && ctx.Err() == nil {
		refuse(w, fail(http.StatusUnprocessableEntity, "start", "The command couldn't start."))
		return
	}
	s.log.Info("agent-host project command", "kind", q.Kind, "exit", code, "timedOut", ctx.Err() != nil)
	send(w, http.StatusOK, projectCommandReply{Code: code, Tail: validTail(out.buf), Cut: out.cut, TimedOut: ctx.Err() != nil})
}

func validTail(b []byte) string {
	for len(b) > 0 && !utf8.RuneStart(b[0]) {
		b = b[1:]
	}
	return strings.ToValidUTF8(string(b), "?")
}

func (c *Client) ProjectCommand(ctx context.Context, cwd, kind, command string) (session.CommandResult, error) {
	limit, ok := projectCommandFor(kind)
	if !ok {
		return session.CommandResult{}, &session.HostError{Code: "kind", Message: "not a project command kind"}
	}
	hc := *c.http
	hc.Timeout = limit + clientSlack
	long := *c
	long.http = &hc
	var out projectCommandReply
	if err := long.call(ctx, http.MethodPost, projectCommandRoute, projectCommandJSON{Cwd: cwd, Kind: kind, Command: command}, &out); err != nil {
		return session.CommandResult{}, err
	}
	tail := out.Tail
	if len(tail) > session.MaxCommandTail {
		tail = tail[len(tail)-session.MaxCommandTail:]
	}
	return session.CommandResult{Code: out.Code, Tail: validTail([]byte(tail)), Cut: out.Cut, TimedOut: out.TimedOut}, nil
}
