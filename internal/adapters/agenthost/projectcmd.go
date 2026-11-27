package agenthost

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	projectCommandRoute = "/v1/project-command"
	commandOutputRoute  = "/v1/project-command/output"
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
	Run      string `json:"run,omitempty"`
	File     bool   `json:"file,omitempty"`
}

type commandOutputJSON struct {
	Kind string `json:"kind"`
	Run  string `json:"run"`
}

type commandOutputReply struct {
	Head []byte `json:"head"`
	Tail []byte `json:"tail"`
	Left int64  `json:"left"`
}

type keptOutput struct {
	run  string
	name string
	head []byte
	tail []byte
	left int64
}

type outputKeep struct {
	head  []byte
	tail  []byte
	left  int64
	total int64
}

func (k *outputKeep) Write(p []byte) (int, error) {
	n := len(p)
	k.total += int64(n)
	if room := session.CommandOutputHead - len(k.head); room > 0 {
		take := min(room, len(p))
		k.head = append(k.head, p[:take]...)
		p = p[take:]
	}
	k.tail = append(k.tail, p...)
	if over := len(k.tail) - session.CommandOutputTail; over > session.CommandOutputTail {
		k.left += int64(over)
		k.tail = append(k.tail[:0], k.tail[over:]...)
	}
	return n, nil
}

func (k *outputKeep) settle() {
	if over := len(k.tail) - session.CommandOutputTail; over > 0 {
		k.left += int64(over)
		k.tail = slices.Clone(k.tail[over:])
	}
}

func (k *outputKeep) last(n int) []byte {
	if len(k.tail) >= n || k.left > 0 {
		return k.tail[len(k.tail)-min(n, len(k.tail)):]
	}
	whole := slices.Concat(k.head, k.tail)
	return whole[len(whole)-min(n, len(whole)):]
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
	out := &outputKeep{}
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
	out.settle()
	run, file := s.keepOutput(q.Kind, out)
	s.log.Info("agent-host project command", "kind", q.Kind, "exit", code, "timedOut", ctx.Err() != nil, "kept", out.total, "file", file)
	send(w, http.StatusOK, projectCommandReply{Code: code, Tail: validTail(out.last(session.MaxCommandTail)), Cut: out.total > session.MaxCommandTail, TimedOut: ctx.Err() != nil, Run: run, File: file})
}

func (s *Server) keepOutput(kind string, out *outputKeep) (string, bool) {
	run := randomID()
	kept := &keptOutput{run: run, name: session.CommandOutputName(kind, run), head: out.head, tail: out.tail, left: out.left}
	s.outMu.Lock()
	defer s.outMu.Unlock()
	if s.outputs == nil {
		s.outputs = map[string]*keptOutput{}
	}
	before := s.outputs[kind]
	s.outputs[kind] = kept
	root, err := os.OpenRoot(session.CommandOutputTop)
	if err != nil {
		s.log.Warn("agent-host project command", "event", "output-not-written", "kind", kind)
		return run, false
	}
	defer func() { _ = root.Close() }()
	if before != nil {
		_ = root.Remove(path.Join(session.CommandOutputFolder, before.name))
	}
	if err := writeOwn(root, path.Join(session.CommandOutputFolder, kept.name), session.JoinEnds(kept.head, kept.tail, kept.left), 0o600); err != nil {
		s.log.Warn("agent-host project command", "event", "output-not-written", "kind", kind)
		return run, false
	}
	return run, true
}

func (s *Server) commandOutput(w http.ResponseWriter, r *http.Request) {
	var q commandOutputJSON
	if p := decode(w, r, &q); p != nil {
		refuse(w, p)
		return
	}
	if _, ok := projectCommandFor(q.Kind); !ok || !session.ValidOutputRun(q.Run) {
		refuse(w, fail(http.StatusBadRequest, "run", "An output is asked for by its command's kind and run."))
		return
	}
	s.outMu.Lock()
	kept := s.outputs[q.Kind]
	s.outMu.Unlock()
	if kept == nil || kept.run != q.Run {
		refuse(w, fail(http.StatusNotFound, session.OutputGone, "agent-host no longer keeps that run's output."))
		return
	}
	send(w, http.StatusOK, commandOutputReply{Head: kept.head, Tail: kept.tail, Left: kept.left})
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
	return session.CommandResult{Code: out.Code, Tail: validTail([]byte(tail)), Cut: out.Cut, TimedOut: out.TimedOut, Run: out.Run, File: out.File}, nil
}

func (c *Client) CommandOutput(ctx context.Context, kind, run string) (session.KeptOutput, error) {
	var out commandOutputReply
	if err := c.call(ctx, http.MethodPost, commandOutputRoute, commandOutputJSON{Kind: kind, Run: run}, &out); err != nil {
		return session.KeptOutput{}, err
	}
	return session.KeptOutput{Head: out.Head, Tail: out.Tail, Left: out.Left}, nil
}
