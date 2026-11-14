package session

import (
	"context"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	CommandCheck      = "check"
	CommandSetup      = "setup"
	CheckFor          = 5 * time.Minute
	SetupFor          = 15 * time.Minute
	MaxProjectCommand = 300
	MaxCommandTail    = 8 << 10
	maxCommandNote    = maxNote - 100
	commandSlack      = 30 * time.Second
	AuditProjectCmd   = "project-command"
	AuditSetupRun     = "setup-command"
	CheckFailed       = "The project's check failed; fix it before you finish."
	RunRunning        = "running"
	RunPassed         = "passed"
	RunFailed         = "failed"
	RunTimedOut       = "timed-out"
	RunError          = "error"
	CheckNone         = "none"
	CheckBusy         = "busy"
	withheldTail      = "The output held a token-shaped string, so the engine didn't keep it. Run the command in the worktree to see it."
)

type ProjectCommands struct {
	Check string
	Setup string
}

type ProjectID struct {
	Name string
	URL  string
}

type CommandResult struct {
	Code     int
	Tail     string
	Cut      bool
	TimedOut bool
}

type CommandHost interface {
	ProjectCommand(ctx context.Context, cwd, kind, command string) (CommandResult, error)
}

func (w *SessionWork) hosted() bool {
	return w.d != nil && w.d.boxed()
}

func (w *SessionWork) commandHost(ctx context.Context, sid string) (CommandHost, error) {
	if w.d == nil {
		return nil, &HostError{Code: "no-helper", Message: "This engine has no box helper to run sessions' containers."}
	}
	d := w.d
	d.mu.Lock()
	r := d.runs[sid]
	box := ""
	if r != nil {
		box = r.box
	}
	d.mu.Unlock()
	if err := d.ConfirmBox(ctx, box); err != nil && box != "" {
		return nil, &HostError{Code: "box", Message: errText(err)}
	}
	host := d.hostFor(box)
	if h, ok := host.(CommandHost); ok {
		return h, nil
	}
	if down, ok := host.(downHost); ok {
		return nil, down.err
	}
	return nil, &HostError{Code: "command", Message: "agent-host in the session's container doesn't run project commands"}
}

type ProjectCommandStore interface {
	Load() (map[ProjectID]ProjectCommands, error)
	Save(map[ProjectID]ProjectCommands) error
}

type CommandRun struct {
	Kind     string
	State    string
	Code     int
	Tail     string
	Cut      bool
	Withheld bool
	Reason   string
	Turn     int
	Retry    bool
	Started  time.Time
	Ended    time.Time
}

type TurnCheckAnswer struct {
	Result string
	Block  bool
	Text   string
}

func CheckProjectCommand(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if len(s) > MaxProjectCommand || !utf8.ValidString(s) {
		return "", false
	}
	for _, r := range s {
		if r == '\t' {
			continue
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == ' ' || r == ' ' {
			return "", false
		}
	}
	return s, true
}

func commandOf(c ProjectCommands, kind string) string {
	if kind == CommandSetup {
		return c.Setup
	}
	return c.Check
}

func resultState(res CommandResult) string {
	switch {
	case res.TimedOut:
		return RunTimedOut
	case res.Code == 0:
		return RunPassed
	}
	return RunFailed
}

func (w *SessionWork) runCommand(ctx context.Context, sid, kind, command, cwd string) CommandRun {
	run := CommandRun{Kind: kind, State: RunRunning, Started: w.cfg.Now()}
	w.mu.Lock()
	rs := w.runsOf(sid)
	run.Turn = rs.turn
	if kind == CommandSetup {
		rs.setup = &run
	} else {
		rs.check = &run
	}
	w.mu.Unlock()
	w.changed(sid)
	limit := CheckFor
	if kind == CommandSetup {
		limit = SetupFor
	}
	rctx, cancel := context.WithTimeout(ctx, limit+commandSlack)
	var res CommandResult
	host, err := w.commandHost(rctx, sid)
	if err == nil {
		res, err = host.ProjectCommand(rctx, cwd, kind, command)
	}
	cancel()
	done := run
	done.Ended = w.cfg.Now()
	switch {
	case err != nil:
		done.State, done.Reason = RunError, hostMessage(err)
	default:
		done.State, done.Code, done.Cut = resultState(res), res.Code, res.Cut
		tail := strings.TrimRight(ansi.ReplaceAllString(res.Tail, ""), "\n ")
		if secretText(tail) {
			done.Withheld = true
		} else {
			done.Tail = clip(tail, MaxCommandTail)
		}
	}
	w.mu.Lock()
	rs = w.runsOf(sid)
	if kind == CommandSetup {
		rs.setup = &done
	} else {
		rs.check = &done
	}
	w.mu.Unlock()
	w.cfg.Logger.Info("project command", "kind", kind, "state", done.State)
	w.changed(sid)
	return done
}

func failureText(run CommandRun, command string, room int) string {
	tail := run.Tail
	if run.Withheld {
		tail = withheldTail
	}
	shown, ok := CheckProjectCommand(command)
	if !ok || secretText(shown) {
		shown = ""
	}
	text := failureLines(run, shown, tail, room)
	if secretText(text) {
		text = failureLines(run, "", withheldTail, room)
	}
	return text
}

func failureLines(run CommandRun, command, tail string, room int) string {
	lead := CheckFailed + "\n"
	if command != "" {
		lead += "The check is the command the person set on Software Factory's page, run in the session's worktree: " + command + "\n"
	} else {
		lead += "The check is a command the person set on Software Factory's page, run in the session's worktree; the engine doesn't show it, since it or its output held a token-shaped string.\n"
	}
	switch {
	case run.State == RunTimedOut:
		lead += "It didn't finish within 5 minutes, so it was stopped."
	case run.Code < 0:
		lead += "It was ended by a signal or couldn't start, so it has no exit code."
	default:
		lead += "It exited with code " + strconv.Itoa(run.Code) + "."
	}
	if tail == "" {
		return lead + " It printed nothing."
	}
	lead += " The end of its output:"
	if room > 0 && len(lead)+2+len(tail) > room {
		keep := max(room-len(lead)-5, 0)
		tail = "…" + lastBytes(tail, keep)
	}
	return lead + "\n\n" + tail
}

func lastBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	for len(s) > 0 && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return s
}

func (w *SessionWork) TurnCheck(ctx context.Context, sid string, again bool) TurnCheckAnswer {
	info, ok := w.reg.workInfo(sid)
	if !ok || info.Agent != "claude" || !w.hosted() {
		return TurnCheckAnswer{Result: CheckNone}
	}
	command := w.command(info.Project, CommandCheck)
	if command == "" {
		return TurnCheckAnswer{Result: CheckNone}
	}
	w.mu.Lock()
	rs := w.runsOf(sid)
	if rs.checking {
		w.mu.Unlock()
		return TurnCheckAnswer{Result: CheckBusy}
	}
	rs.checking = true
	w.mu.Unlock()
	done := w.runCommand(ctx, sid, CommandCheck, command, info.Cwd)
	w.mu.Lock()
	rs = w.runsOf(sid)
	rs.checking = false
	failed := done.State == RunFailed || done.State == RunTimedOut
	if failed {
		if rs.failTurn != done.Turn || rs.fails == 0 {
			rs.failTurn, rs.fails = done.Turn, 0
		}
		rs.fails++
	}
	block := failed && !again && rs.fails == 1
	if rs.check != nil {
		rs.check.Retry = again || rs.fails > 1
	}
	w.mu.Unlock()
	w.changed(sid)
	if !failed {
		return TurnCheckAnswer{Result: done.State}
	}
	if !block {
		return TurnCheckAnswer{Result: done.State}
	}
	return TurnCheckAnswer{Result: done.State, Block: true, Text: failureText(done, command, 0)}
}

func (w *SessionWork) checkAfterTurn(sid string, info workFacts) {
	command := w.command(info.Project, CommandCheck)
	if command == "" || !w.hosted() {
		return
	}
	w.mu.Lock()
	rs := w.runsOf(sid)
	if rs.checking {
		w.mu.Unlock()
		return
	}
	rs.checking = true
	w.mu.Unlock()
	done := w.runCommand(w.cfg.Base, sid, CommandCheck, command, info.Cwd)
	w.mu.Lock()
	w.runsOf(sid).checking = false
	w.mu.Unlock()
	switch done.State {
	case RunFailed, RunTimedOut:
		w.cfg.Note(sid, failureText(done, command, maxCommandNote))
	case RunPassed:
		w.cfg.Note(sid, "")
	}
}
