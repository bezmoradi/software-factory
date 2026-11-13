package level

import "strings"

type Level string

const (
	Locked  Level = "locked"
	Held    Level = "held"
	Stopped Level = "stopped"
)

func (l Level) rank() int {
	switch l {
	case Locked:
		return 2
	case Held:
		return 1
	}
	return 0
}

type Observer string

const (
	Engine Observer = "engine"
	Agent  Observer = "agent"
)

type Event string

const (
	CheckPassed    Event = "check-passed"
	CheckFailed    Event = "check-failed"
	CheckNotRun    Event = "check-not-run"
	ProcessEnded   Event = "process-ended"
	ProcessLost    Event = "process-lost"
	ProcessStarted Event = "process-started"
)

type Observation struct {
	By    Observer
	Event Event
	Parts []string
}

type Result struct {
	Level     Level
	Reason    string
	Dropped   bool
	Sandboxed bool
}

func resultOf(o Observation) (Result, bool) {
	switch o.Event {
	case CheckPassed:
		return Result{Level: Locked, Reason: "the sandbox check passed when the session last started and at every check since"}, true
	case CheckFailed:
		return Result{Level: Held, Reason: "the sandbox check failed" + parts(o.Parts)}, true
	case CheckNotRun:
		return Result{Level: Held, Reason: "the sandbox check couldn't run" + parts(o.Parts)}, true
	case ProcessEnded:
		return Result{Level: Stopped, Reason: "the session's process ended"}, true
	case ProcessLost:
		return Result{Level: Stopped, Reason: "agent-host lost the session's process"}, true
	}
	return Result{}, false
}

func Of(observations []Observation) Result {
	r := Result{Level: Held, Reason: "the sandbox check hasn't run for this session yet"}
	first, wasLocked, runLocked, broken := true, false, false, false
	for _, o := range observations {
		if o.By != Engine {
			continue
		}
		if o.Event == ProcessStarted {
			r = Result{Level: Held, Reason: "the sandbox check hasn't run since the session was woken"}
			first, runLocked = true, false
			continue
		}
		next, ok := resultOf(o)
		if !ok {
			continue
		}
		if next.Level == Held || (first && next.Level != Locked) {
			broken = true
		}
		if first || next.Level.rank() < r.Level.rank() {
			r = next
		}
		first = false
		runLocked = runLocked || r.Level == Locked
		wasLocked = wasLocked || runLocked
	}
	r.Dropped = runLocked && r.Level != Locked
	r.Sandboxed = wasLocked && !broken
	switch {
	case r.Level == Locked && broken:
		r.Reason = "the sandbox check passes now, but it failed or couldn't run in an earlier run of this session, so this session's work isn't counted as sandboxed"
	case r.Level == Stopped && broken:
		r.Reason = r.Reason + ", and the sandbox check failed or couldn't run while it ran or after it stopped, so its work isn't counted as sandboxed"
	case r.Level == Stopped && !wasLocked:
		r.Reason = r.Reason + " before the sandbox check passed, so its work isn't counted as sandboxed"
	}
	return r
}

func parts(p []string) string {
	if len(p) == 0 {
		return ""
	}
	return ": " + strings.Join(p, "; ")
}
