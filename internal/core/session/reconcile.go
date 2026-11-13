package session

import (
	"context"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/gate"
	"github.com/bezmoradi/software-factory/internal/core/push"
)

const ReconcileFor = 20 * time.Second

type AuditOpen interface {
	Open() ([]AuditEntry, error)
}

func (x *pushing) unclear(target push.Target, expected, why string, missing []string, exception string) push.Reply {
	p := x.p
	ctx, cancel := context.WithTimeout(p.cfg.Base, ReconcileFor)
	defer cancel()
	e := x.entry
	remote, err := p.cfg.Pusher.Remote(ctx, target)
	switch {
	case err == nil && remote.BranchCommit == e.Commit:
		return x.end(gate.Pushed, "pushed "+shortSHA(e.Commit)+" to "+e.Branch+" at "+e.Remote+" (confirmed on the remote after the transport failed: "+why+")", missing, exception)
	case err == nil && remote.BranchCommit == expected:
		return x.end(gate.Refused, "the push failed: "+why+"; the remote's branch is still where it was", missing, exception)
	}
	now := "the engine couldn't ask the remote again"
	if err == nil {
		now = "the remote's branch is at " + orNone(shortSHA(remote.BranchCommit)) + " now"
	}
	p.cfg.Logger.Warn("push", "event", "unclear")
	return x.end(ResultUnclear, "the engine can't tell whether commit "+shortSHA(e.Commit)+" reached "+e.Branch+": "+why+"; "+now+". Pressing Push again is safe: the engine checks the remote first and never pushes over a branch that moved", missing, exception)
}

func orNone(s string) string {
	if s == "" {
		return "no commit"
	}
	return s
}

func (p *Pushes) Reconcile(ctx context.Context, open AuditOpen) int {
	if open == nil {
		return 0
	}
	list, err := open.Open()
	if err != nil {
		p.cfg.Logger.Warn("push reconcile", "event", "audit-unreadable")
		return 0
	}
	done := 0
	for _, e := range list {
		if e.Kind != AuditPush || (e.Result != string(gate.Allowed) && e.Result != ResultUnclear) {
			continue
		}
		if p.reconcile(ctx, e) {
			done++
		}
	}
	if done > 0 {
		p.cfg.Logger.Info("push reconcile", "event", "settled", "count", done)
	}
	return done
}

func (p *Pushes) reconcile(ctx context.Context, e AuditEntry) bool {
	url, token, err := p.cfg.Repos.PushTarget(e.Repository)
	switch {
	case err != nil:
		p.cfg.Logger.Warn("push reconcile", "event", "skipped", "why", "project")
		return false
	case url != e.Remote || e.Commit == "" || !push.ValidBranch(e.Branch):
		p.cfg.Logger.Warn("push reconcile", "event", "skipped", "why", "remote")
		return false
	case token == "":
		p.cfg.Logger.Warn("push reconcile", "event", "skipped", "why", "token")
		return false
	}
	rctx, cancel := context.WithTimeout(ctx, ReconcileFor)
	remote, err := p.cfg.Pusher.Remote(rctx, push.Target{URL: url, Token: token, Branch: e.Branch})
	cancel()
	if err != nil {
		p.cfg.Logger.Warn("push reconcile", "event", "remote-unreachable")
		return false
	}
	out := e
	out.Time, out.By = p.cfg.Now(), ByStart
	said := ""
	sha := shortSHA7(e.Commit)
	if remote.BranchCommit == e.Commit {
		out.Result = string(gate.Pushed)
		out.Reason = "commit " + shortSHA(e.Commit) + " is on the remote's branch " + e.Branch + ", so the push the engine made before its restart went out"
		said = "Software Factory's push of commit " + sha + " to " + e.Branch + " went out before the engine restarted."
	} else {
		out.Result = string(gate.Refused)
		out.Reason = "the push of " + shortSHA(e.Commit) + " to " + e.Branch + " didn't go out before the engine's restart: the remote's branch is at " + orNone(shortSHA(remote.BranchCommit))
		switch {
		case p.planned(e.Session, e.Commit):
			out.Reason += "; the engine decides it again from its saved plan"
		case e.By != ByPage:
			out.Reason += "; press Push again"
			said = "Software Factory's push of commit " + sha + " to " + e.Branch + " didn't go out before the engine restarted. The person can push it from the page; don't call factory_push for this commit again."
		default:
			out.Reason += "; press Push again"
		}
	}
	out.Reason = clip(out.Reason, maxPushReason)
	if err := p.cfg.Audit.Append(out); err != nil {
		p.cfg.Logger.Warn("push reconcile", "event", "audit-not-written")
		return false
	}
	if t, ok := p.cfg.Targets(e.Session); ok && t.Branch == e.Branch {
		p.show(e.Session, PushView{Result: out.Result, Reason: out.Reason, Commit: e.Commit, Branch: e.Branch, Remote: e.Remote, Missing: out.Missing, At: out.Time, Audit: &out})
		if said != "" {
			p.cfg.Note(e.Session, said)
		}
	}
	return true
}
