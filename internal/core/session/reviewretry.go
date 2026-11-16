package session

import (
	"context"
	"slices"

	"github.com/bezmoradi/software-factory/internal/core/review"
	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

const (
	FailureNoOutcome = "no-outcome"
	FailureTimeout   = "timeout"
	FailureError     = "error"
	MaxAttempts      = 2
)

var noOutcome = []string{"engine-stopped", "host-unreachable", "host-refused:full"}

func classified(e ReviewEntry, kind string) ReviewEntry {
	if e.Record.State != review.Failed {
		e.Failure = ""
		return e
	}
	switch {
	case slices.Contains(noOutcome, kind):
		e.Failure = FailureNoOutcome
	case kind == "timeout":
		e.Failure = FailureTimeout
	default:
		e.Failure = FailureError
	}
	return e
}

func attemptOf(e ReviewEntry) int {
	return max(e.Attempt, 1)
}

func autoAttemptsOf(e ReviewEntry) int {
	switch {
	case e.AutoAttempts > 0:
		return e.AutoAttempts
	case e.Auto:
		return attemptOf(e)
	}
	return 0
}

func autoAttempt(auto bool) int {
	if auto {
		return 1
	}
	return 0
}

type ReviewScope int

const (
	ScopeEvery ReviewScope = iota
	ScopeUnfinished
	ScopeRetry
)

func (s ReviewScope) skips(e ReviewEntry) bool {
	switch s {
	case ScopeRetry:
		return !Retryable(e) && !Rerunnable(e)
	case ScopeUnfinished:
		return settled(e)
	}
	return false
}

func settled(e ReviewEntry) bool {
	switch e.Record.State {
	case review.Pass, review.Block:
		return true
	case review.Unknown:
		return e.Record.Channel == review.Checking
	}
	return false
}

func Retryable(e ReviewEntry) bool {
	return e.Record.State == review.Failed && e.Failure == FailureNoOutcome && attemptOf(e) < MaxAttempts
}

func nextAttempt(entries map[review.Key]ReviewEntry, k review.Key) int {
	if e, ok := entries[k]; ok && e.Record.State != review.Running {
		return attemptOf(e) + 1
	}
	return 1
}

func finishedRole(q workflow.Requirement, e ReviewEntry, round, limit int) RoleReview {
	v := RoleReview{Role: q.Role, Name: ReviewerName(q), Agent: q.Agent, Why: q.Why, Round: round, Limit: limit, Since: e.Since, Previous: e.Since != "", Override: e.Record.Override}
	return roleOf(v, e)
}

func (d *Dispatcher) Retry(ctx context.Context, sid string) error {
	if err := d.historyOnly(sid); err != nil {
		return err
	}
	if d.cfg.Reviews == nil {
		return explained(conflict("no reviewer"), "This engine runs no reviewer containers, so it can't run reviews.")
	}
	if why := d.reg.halt.automatic(); why != "" {
		return explained(conflict("halted"), why)
	}
	if why := d.automaticHeld(sid); why != "" {
		return explained(conflict("limits"), why)
	}
	t, ok := d.PushTarget(sid)
	if !ok {
		return explained(conflict("not started here"), "Only a session this engine started can be reviewed from here.")
	}
	rt := ReviewTarget{SID: sid, Repo: t.Repo, Name: t.Name, Branch: t.Branch, Auto: true, Scope: ScopeRetry}
	if w := d.cfg.Work; w != nil {
		rt.Plan = func(ctx context.Context, base, head string) ([]workflow.Requirement, error) {
			required := w.Reviewers(ctx, sid, t, base, head)
			return required, ctx.Err()
		}
		rt.Rounds = w.Rounds(sid, t.Name)
	}
	d.reviewGiven(&rt, sid, t)
	return d.cfg.Reviews.Start(ctx, rt)
}
