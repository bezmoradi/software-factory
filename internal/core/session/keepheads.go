package session

import (
	"context"
	"slices"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/review"
	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

const (
	keepInTurn   = "The session is in its turn. Restore works once the turn ends or is stopped."
	keepRolesFor = 5 * time.Second
)

type KeptVerdict string

const (
	KeptPass       KeptVerdict = "pass"
	KeptBlock      KeptVerdict = "block"
	KeptOverridden KeptVerdict = "overridden"
	KeptNone       KeptVerdict = "none"
)

type KeptRole struct {
	Name    string
	Agent   string
	Verdict KeptVerdict
}

type KeptHead struct {
	Commit     string
	At         time.Time
	Holds      bool
	Restorable bool
	Refusal    string
	Reviewed   bool
	RolesRead  bool
	Roles      []KeptRole
	Passed     bool
}

func (k KeepRecord) kept(commit string) bool {
	return commit != "" && slices.ContainsFunc(k.Packs, func(p KeptPack) bool { return p.Commit == commit })
}

func pastLast(branch, head, last string) string {
	return "The branch " + branch + " is at " + shortSHA(head) + ", past the last copied commit " + shortSHA(last) + ", and the engine has no copy of the commits after it, so restoring an earlier commit would drop them. The engine copies them when a turn ends, unless copying stopped."
}

func earlier(chosen, last string) string {
	if chosen == last {
		return ""
	}
	return ", an earlier kept commit than the last copied one, " + shortSHA(last)
}

func (k *Keep) keptHeads(ctx context.Context, sid string, t PushTarget, rec KeepRecord, v KeepView, history, inTurn, broken bool) []KeptHead {
	last := rec.Last()
	past := v.Holds && v.Head != last
	out := make([]KeptHead, 0, len(rec.Packs))
	seen := map[string]bool{}
	for _, p := range slices.Backward(rec.Packs) {
		if seen[p.Commit] {
			continue
		}
		seen[p.Commit] = true
		h := KeptHead{Commit: p.Commit, At: p.At, Holds: v.Head == p.Commit}
		switch {
		case p.Commit == last:
			h.Holds, h.Restorable, h.Refusal = v.Holds, v.Restorable, v.Refusal
		case history:
			h.Refusal = historyMessage
		case h.Holds && !broken:
			h.Refusal = "The branch is at this commit, so there is nothing to restore."
		case inTurn:
			h.Refusal = keepInTurn
		case past:
			h.Refusal = pastLast(v.Branch, v.Head, last)
		default:
			h.Restorable = true
		}
		out = append(out, h)
	}
	k.keptReviews(ctx, sid, t, out)
	return out
}

func (k *Keep) keptReviews(ctx context.Context, sid string, t PushTarget, heads []KeptHead) {
	rv := k.d.cfg.Reviews
	if rv == nil || len(heads) == 0 {
		return
	}
	entries, err := rv.Entries()
	if err != nil {
		return
	}
	reviewed := map[string]bool{}
	for key := range entries {
		if key.Branch == t.Branch {
			reviewed[key.Commit] = true
		}
	}
	main := ""
	if k.cfg.Main != nil {
		if m, err := k.cfg.Main(t.Name); err == nil && sha1Hex.MatchString(m) {
			main = m
		}
	}
	for i := range heads {
		h := &heads[i]
		if !reviewed[h.Commit] {
			continue
		}
		h.Reviewed = true
		if ctx.Err() != nil {
			continue
		}
		required, ok := k.requiredAt(ctx, sid, t, main, h.Commit)
		if !ok {
			continue
		}
		h.RolesRead = true
		h.Passed = len(required) > 0
		for _, q := range required {
			verdict := keptVerdict(entries, review.Key{Commit: h.Commit, Branch: t.Branch, Agent: q.Agent}, q)
			h.Roles = append(h.Roles, KeptRole{Name: ReviewerName(q), Agent: q.Agent, Verdict: verdict})
			if verdict != KeptPass && verdict != KeptOverridden {
				h.Passed = false
			}
		}
	}
}

func (k *Keep) requiredAt(ctx context.Context, sid string, t PushTarget, main, head string) ([]workflow.Requirement, bool) {
	w := k.d.cfg.Work
	if w == nil {
		rv := k.d.cfg.Reviews
		var out []workflow.Requirement
		for _, role := range rv.roles() {
			out = append(out, workflow.Requirement{Agent: rv.cfg.Agents[role], Role: role})
		}
		return out, true
	}
	if main == "" {
		return nil, false
	}
	base, err := w.cfg.History.MergeBase(ctx, t.Repo, head, main)
	if err != nil || base == "" {
		return nil, false
	}
	reviewers := w.Reviewers(ctx, sid, t, base, head)
	if ctx.Err() != nil {
		return nil, false
	}
	var out []workflow.Requirement
	for _, q := range reviewers {
		if !q.Advisory {
			out = append(out, q)
		}
	}
	return out, true
}

func keptVerdict(entries map[review.Key]ReviewEntry, key review.Key, q workflow.Requirement) KeptVerdict {
	if q.Missing {
		return KeptNone
	}
	none, overridden := false, false
	for _, rk := range key.Runs(q.RunCount()) {
		e, ok := entries[rk]
		switch {
		case !ok || e.Record.State == review.Running:
			none = true
		case e.Record.Override != "":
			overridden = true
		case e.Record.State == review.Block:
			return KeptBlock
		case e.Record.State != review.Pass:
			none = true
		}
	}
	switch {
	case none:
		return KeptNone
	case overridden:
		return KeptOverridden
	}
	return KeptPass
}
