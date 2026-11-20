package session

import "github.com/bezmoradi/software-factory/internal/core/review"

type WholeReply struct {
	Text    string
	Clipped bool
}

type WholeReplies interface {
	WholeReply(sid, agent, commit string, run int) (WholeReply, error)
}

func (d *Dispatcher) WholeReply(sid, agent, commit string, run int) (WholeReply, error) {
	gone := explained(notFound("reply"), "The engine keeps no whole report for that review.")
	t, ok := targetOrHeld(d.PushTarget, d.HeldTarget, sid)
	if !ok || d.cfg.Reviews == nil || run < 1 || run > review.MaxRuns {
		return WholeReply{}, gone
	}
	entries, err := d.cfg.Reviews.Entries()
	if err != nil {
		return WholeReply{}, explained(conflict("records"), "The engine couldn't read its review records.")
	}
	e, ok := entries[review.Key{Commit: commit, Branch: t.Branch, Agent: agent}.AtRun(run)]
	if !ok || e.Message == "" {
		return WholeReply{}, gone
	}
	text, cut := d.cfg.Reviews.ReplyOf(sid, e)
	if text == "" || len(text) > MaxFullReply {
		return WholeReply{}, gone
	}
	return WholeReply{Text: text, Clipped: cut == MaxFullReply}, nil
}
