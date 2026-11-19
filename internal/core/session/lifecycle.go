package session

import (
	"context"
	"slices"
	"strconv"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	AuditSessionStart  = "session-start"
	AuditSessionWake   = "session-wake"
	AuditSessionStop   = "session-stop"
	AuditSessionFork   = "session-fork"
	AuditSessionDelete = "session-delete"
)

const (
	StartFromMain   = "main"
	StartFromBranch = "branch"
	StartFromPull   = "pull-request"
	StartFromFork   = "fork"
	StartFromPinned = "pinned"
)

const (
	ResultStarted = "started"
	ResultWoken   = "woken"
	ResultStopped = "stopped"
	ResultForked  = "forked"
	ResultDeleted = "deleted"
)

func LifecycleAudit(kind string) bool {
	return slices.Contains([]string{AuditSessionStart, AuditSessionWake, AuditSessionStop, AuditSessionFork, AuditSessionDelete}, kind)
}

func ProjectWideAudit(kind string) bool {
	return kind == AuditSessionFork || kind == AuditSessionDelete
}

type startedFrom struct {
	kind   string
	ref    string
	source string
}

func startFromOf(q Dispatch) startedFrom {
	switch {
	case q.pinned != nil:
		return startedFrom{kind: StartFromPinned}
	case q.From.SID != "":
		return startedFrom{kind: StartFromFork, source: q.From.SID}
	case q.From.Branch != "":
		return startedFrom{kind: StartFromBranch, ref: q.From.Branch}
	case q.From.Pull > 0:
		return startedFrom{kind: StartFromPull, ref: strconv.Itoa(q.From.Pull)}
	}
	return startedFrom{kind: StartFromMain}
}

func (d *Dispatcher) lifecycle(ctx context.Context, kind string, r *run, by, result, reason string) {
	if d.cfg.Audit == nil || r == nil {
		return
	}
	d.mu.Lock()
	e := AuditEntry{Time: d.reg.now(), Session: r.sid, Repository: r.repo, Branch: r.branch, Box: r.box, Agent: r.spec.ID, By: by, Kind: kind,
		Result: result, Reason: clip(reason, 400), Req: shared.RequestOf(ctx)}
	if kind == AuditSessionStart || kind == AuditSessionFork {
		e.Commit, e.From, e.FromRef, e.Source = r.base, r.from.kind, r.from.ref, r.from.source
	}
	d.mu.Unlock()
	if err := d.cfg.Audit.Append(e); err != nil {
		shared.For(ctx, d.cfg.Logger).Warn("agent session", "event", "audit-not-written", "kind", kind)
	}
}

func (d *Dispatcher) audited(ctx context.Context, r *run) {
	if r.from.kind == StartFromFork {
		d.lifecycle(ctx, AuditSessionFork, r, ByPage, ResultForked, "")
	}
	d.lifecycle(ctx, AuditSessionStart, r, ByPage, ResultStarted, "")
}
