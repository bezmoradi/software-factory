package session

import (
	"context"
	"slices"
	"time"
)

const trustFor = 90 * time.Second

type TrustHost interface {
	Trust(ctx context.Context, c Command) error
}

func notTrusted(repo, path string) *Error {
	return &Error{Kind: Conflict, Reason: "not trusted", Message: "Claude Code doesn't trust " + repo + " yet. Trust it from the page, or in a terminal: docker compose exec -it -w " + path + " agent claude, then choose Yes, I trust this folder.",
		Needs: &Needs{Trust: path}}
}

func (d *Dispatcher) Trust(ctx context.Context, path, approve string) error {
	spec, ok := d.claudeSpec()
	host, isTrust := d.cfg.Host.(TrustHost)
	if !ok || !isTrust || d.cfg.Repositories == nil {
		return explained(conflict("trust"), "This engine can't answer Claude Code's trust question.")
	}
	repos, err := d.cfg.Repositories.List()
	if err != nil {
		return explained(conflict("workspace"), "The engine couldn't read the workspace's repositories.")
	}
	i := slices.IndexFunc(repos, func(r Repository) bool { return r.Path == path })
	if i < 0 {
		return explained(notFound("project"), "That project is no longer in the list.")
	}
	if err := d.trustFiles(spec, repos[i], approve); err != nil {
		return err
	}
	tctx, cancel := context.WithTimeout(ctx, trustFor)
	defer cancel()
	err = host.Trust(tctx, Command{Argv: []string{spec.Command[0]}, Dir: repos[i].Path, Pass: spec.Pass, Timeout: trustFor})
	d.cfg.Logger.Info("claude trust", "repo", repos[i].Name, "ok", err == nil)
	if err != nil {
		return explained(conflict("trust"), hostMessage(err))
	}
	return nil
}

func cloneEntries(list string) []StartupEntry {
	var out []StartupEntry
	for _, e := range StartupLists[list] {
		if slices.ContainsFunc(out, func(o StartupEntry) bool { return o.Path == e.Path }) {
			continue
		}
		e.Place = InClone
		out = append(out, e)
	}
	return out
}

func (d *Dispatcher) trustFiles(spec AgentSpec, repo Repository, approve string) error {
	entries := cloneEntries(spec.Startup)
	if len(entries) == 0 {
		return explained(conflict("no start-up list"), "Software Factory has no list of the files "+spec.Name+" loads at start, so it can't show them and doesn't answer the trust question.")
	}
	if d.cfg.Startup == nil {
		return explained(conflict("no start-up reader"), "This engine can't read the clone folder's start-up files, so it doesn't answer the trust question.")
	}
	read, err := d.cfg.Startup.Read(repo.Path, "", repo.Branch, entries)
	if err != nil {
		d.cfg.Logger.Warn("claude trust", "event", "start-up-unreadable")
		return explained(conflict("start-up files"), clip("The engine couldn't read the files "+spec.Name+" loads from the clone folder, so it can't show them and doesn't answer the trust question: "+err.Error(), maxOutput))
	}
	if len(read.Files) == 0 {
		return nil
	}
	hash := SetHash(spec.Startup+" trust", read.Files)
	if approve == hash {
		d.trustAudit(spec, repo, read.Commit, hash)
		d.cfg.Logger.Info("claude trust", "event", "start-up-approved")
		return nil
	}
	msg := spec.Name + " loads these files from " + repo.Name + "'s clone folder once it trusts the folder. No commit put them there, and they can change what it may do without asking. Read them, then approve to answer the trust question."
	if approve != "" {
		msg = "The files in the clone folder changed since you looked at them. Read them again, then approve to answer the trust question."
	}
	d.cfg.Logger.Info("claude trust", "event", "needs-approval")
	return &Error{Kind: Conflict, Reason: "needs approval", Message: msg, Needs: &Needs{Approval: approvalView(spec.ID, repo.Name, read, hash)}}
}

func (d *Dispatcher) trustAudit(spec AgentSpec, repo Repository, commit, hash string) {
	if d.cfg.Audit == nil {
		return
	}
	e := AuditEntry{Time: d.reg.now(), Repository: repo.Name, Commit: commit, By: ByPage, Kind: AuditApproveStartup, Result: ResultApproved, Agent: spec.ID, Hash: hash,
		Reason: "the developer approved the files " + spec.Name + " loads from the clone's own folder, before the engine answered its trust question"}
	if err := d.cfg.Audit.Append(e); err != nil {
		d.cfg.Logger.Warn("claude trust", "event", "audit-not-written")
	}
}
