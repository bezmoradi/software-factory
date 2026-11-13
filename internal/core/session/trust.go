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

func (d *Dispatcher) Trust(ctx context.Context, path string) error {
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
	tctx, cancel := context.WithTimeout(ctx, trustFor)
	defer cancel()
	err = host.Trust(tctx, Command{Argv: []string{spec.Command[0]}, Dir: repos[i].Path, Pass: spec.Pass, Timeout: trustFor})
	d.cfg.Logger.Info("claude trust", "repo", repos[i].Name, "ok", err == nil)
	if err != nil {
		return explained(conflict("trust"), hostMessage(err))
	}
	return nil
}
