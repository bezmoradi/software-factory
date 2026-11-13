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
	if d.cfg.Reviews == nil {
		return explained(conflict("no reviewer"), "This engine has no reviewer container, so it can't run reviews.")
	}
	t, ok := d.PushTarget(sid)
	if !ok {
		return explained(conflict("not started here"), "Only a session this engine started can be reviewed from here.")
	}
	rt := ReviewTarget{SID: sid, Repo: t.Repo, Name: t.Name, Branch: t.Branch, Auto: true, Retry: true}
	if w := d.cfg.Work; w != nil {
		rt.Plan = func(base, head string) []workflow.Requirement { return w.Reviewers(sid, t, base, head) }
		rt.Rounds = w.Rounds(sid, t.Name)
	}
	return d.cfg.Reviews.Start(ctx, rt)
}
