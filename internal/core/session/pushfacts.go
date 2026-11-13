package session

import (
	"slices"

	"github.com/bezmoradi/software-factory/internal/core/gate"
	"github.com/bezmoradi/software-factory/internal/core/review"
)

func (p *Pushes) OnPushed(fn func(PushedFacts)) {
	p.mu.Lock()
	p.pushed = fn
	p.mu.Unlock()
}

func (p *Pushes) announcePushed(x *pushing, t PushTarget, facts gate.Facts, missing int) {
	p.mu.Lock()
	fn := p.pushed
	p.mu.Unlock()
	if fn == nil {
		return
	}
	e := x.entry
	out := PushedFacts{SID: x.sid, Repository: t.Name, Branch: t.Branch, Commit: e.Commit, Remote: e.Remote, By: e.By, At: e.Time,
		Allowed: x.answer != nil && x.answer.Allow, Missing: missing}
	var entries map[review.Key]ReviewEntry
	if p.cfg.Reviews != nil {
		entries, _ = p.cfg.Reviews.Entries()
	}
	for _, q := range facts.Required {
		k := review.Key{Commit: e.Commit, Branch: t.Branch, Agent: q.Agent}
		r := PushedRole{Role: q.Role, Name: ReviewerName(q), Agent: q.Agent}
		if rec, ok := facts.Reviews[k]; ok {
			r.Recorded = true
			r.Passed = rec.State == review.Pass
			r.Overridden = rec.State == review.Block && rec.Override != ""
		}
		if en, ok := entries[k]; ok {
			r.Since, r.PluginCommit, r.ClaudeCode, r.Models = en.Since, en.PluginCommit, en.ClaudeCode, slices.Clone(en.Models)
		}
		out.Roles = append(out.Roles, r)
	}
	fn(out)
}
