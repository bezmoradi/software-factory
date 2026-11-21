package session

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	AuditBringMain = "bring-main"
	noteMain       = "main"
)

func (d *Dispatcher) BringMain(ctx context.Context, sid string) (string, error) {
	if err := d.historyOnly(sid); err != nil {
		return "", err
	}
	r, err := d.claim(sid)
	if err != nil {
		return "", err
	}
	defer d.release(r)
	d.mu.Lock()
	running := r.running
	d.mu.Unlock()
	if !running {
		return "", conflict("not running", "The session isn't running. Wake it, then bring in the latest main.")
	}
	if !d.reg.TurnSince(sid).IsZero() {
		return "", conflict("in turn", "The session is in its turn. Wait for it to end, then bring in the latest main.")
	}
	if d.cfg.Repositories == nil {
		return "", conflict("no agent-host", "This engine has no repositories to fetch.")
	}
	commit, err := d.cfg.Repositories.Fresh(ctx, r.repo)
	if err != nil {
		if e, ok := errors.AsType[*Error](err); ok {
			return "", explained(e, strings.Replace(e.Message, ", so no session started", "", 1))
		}
		return "", conflict("fetch", "The engine couldn't fetch the project's main branch.")
	}
	main := "main"
	if repos, err := d.cfg.Repositories.List(); err == nil {
		if i := slices.IndexFunc(repos, func(x Repository) bool { return x.Name == r.repo }); i >= 0 && repos[i].Branch != "" {
			main = repos[i].Branch
		}
	}
	if err := d.syncTo(r, main, commit); err != nil {
		return "", conflict("clone", "The engine fetched "+main+" at "+shortSHA(commit)+", but couldn't bring it into the session's clone, so it asked the agent nothing: "+clip(err.Error(), 300))
	}
	text := main + " is now at " + commit + " as origin/" + main + "; merge it into your branch and resolve conflicts"
	sent := d.reg.Message(sid, NoteMarker+text, false, nil, nil) == nil
	if !sent {
		d.reg.NoteFor(noteMain)(sid, text)
	}
	d.bringMainAudit(r, commit, sent)
	shared.For(ctx, d.cfg.Logger).Info("bring main", "sent", sent)
	return commit, nil
}

func (d *Dispatcher) bringMainAudit(r *run, commit string, sent bool) {
	if d.cfg.Audit == nil {
		return
	}
	result, reason := ResultSent, "the developer brought the latest main into the clone as origin/main and asked the agent to merge it"
	if !sent {
		result, reason = "noted", "the developer brought the latest main into the clone as origin/main; the request to merge it goes with the next message to the session"
	}
	e := AuditEntry{Time: d.reg.now(), Session: r.sid, Repository: r.repo, Branch: r.branch, Commit: commit, By: ByPage, Kind: AuditBringMain, Result: result, Reason: reason, Missing: []string{}}
	if err := d.cfg.Audit.Append(e); err != nil {
		d.cfg.Logger.Warn("bring main", "event", "audit-not-written")
	}
}
