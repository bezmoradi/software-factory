package session

import (
	"cmp"
	"slices"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/review"
	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

const (
	MaxGivenPrompt = MaxFullReply
	MaxGivenFlags  = 32
	MaxGivenFlag   = 2 << 10
	MaxGivenFiles  = maxGivenFiles + 1
	MaxGivenFile   = 512
	MaxGivenWhy    = 1000
)

type KeptGiven struct {
	Why   string
	Given *RunGiven
}

type KeptBranch struct {
	Branch  string
	Rounds  int
	Recheck bool
}

func keptGivenOf(why string, g *RunGiven) KeptGiven {
	var out KeptGiven
	if !secretText(why) {
		out.Why = clip(strings.ToValidUTF8(why, "�"), MaxGivenWhy)
	}
	if g == nil || secretText(g.Prompt) || slices.ContainsFunc(g.Flags, secretText) || slices.ContainsFunc(g.Files, secretText) {
		return out
	}
	prompt, _ := keptWhole(strings.ToValidUTF8(g.Prompt, "�"), MaxGivenPrompt)
	kept := &RunGiven{Prompt: prompt}
	for _, f := range g.Flags[:min(len(g.Flags), MaxGivenFlags)] {
		kept.Flags = append(kept.Flags, clip(strings.ToValidUTF8(f, "�"), MaxGivenFlag))
	}
	for _, f := range g.Files[:min(len(g.Files), MaxGivenFiles)] {
		kept.Files = append(kept.Files, clip(strings.ToValidUTF8(f, "�"), MaxGivenFile))
	}
	out.Given = kept
	return out
}

func (r *Reviews) keepGiven(sid string, e ReviewEntry, g KeptGiven) {
	if r.cfg.Replies == nil {
		return
	}
	r.store.Lock()
	defer r.store.Unlock()
	if err := r.cfg.Replies.PutGiven(sid, e, g); err != nil {
		r.cfg.Logger.Warn("review", "event", "given-not-saved")
	}
}

func (r *Reviews) givenFor(sid string, e ReviewEntry) (KeptGiven, bool) {
	if r.cfg.Replies == nil {
		return KeptGiven{}, false
	}
	return r.cfg.Replies.GetGiven(sid, e.Key, e.StartedAt)
}

func (r *Reviews) uncheckedLocked(sid string, v ReviewView) ReviewView {
	if v.Commit == "" || r.rebuilt[sid] != v.Commit {
		return v
	}
	for i, role := range v.Roles {
		if role.State == review.Pass && !role.Advisory {
			v.Roles[i].Unchecked = true
		}
	}
	return v
}

func (r *Reviews) ShowKept(sids []string, branches map[string]KeptBranch) {
	var entries map[review.Key]ReviewEntry
	if len(branches) > 0 {
		all, err := r.Entries()
		if err != nil {
			r.cfg.Logger.Warn("review", "event", "records-unreadable", "for", "kept-views")
		}
		entries = all
	}
	for _, sid := range sids {
		view, saved := ReviewView{}, false
		b, ok := branches[sid]
		if ok {
			view, saved = r.savedView(sid, b, entries)
		}
		r.mu.Lock()
		_, kept := r.tests[sid]
		_, shown := r.views[sid]
		if shown || !saved && !kept {
			r.mu.Unlock()
			continue
		}
		if saved && b.Recheck {
			r.rebuilt[sid] = view.Commit
		}
		v := r.keptLocked(sid, view)
		r.mu.Unlock()
		r.cfg.Show(sid, v)
	}
}

type runsOfRole struct {
	agent    string
	advisory bool
}

func (r *Reviews) savedView(sid string, b KeptBranch, entries map[review.Key]ReviewEntry) (ReviewView, bool) {
	if b.Branch == "" {
		return ReviewView{}, false
	}
	var mine []ReviewEntry
	for _, e := range entries {
		if e.Key.Branch == b.Branch {
			mine = append(mine, e)
		}
	}
	if len(mine) == 0 {
		return ReviewView{}, false
	}
	latest := slices.MaxFunc(mine, func(a, c ReviewEntry) int { return a.StartedAt.Compare(c.StartedAt) })
	mine = slices.DeleteFunc(mine, func(e ReviewEntry) bool { return e.Key.Commit != latest.Key.Commit })
	slices.SortFunc(mine, func(a, c ReviewEntry) int { return c.StartedAt.Compare(a.StartedAt) })
	view := ReviewView{Commit: latest.Key.Commit, Base: latest.Coverage(), Branch: b.Branch}
	if i := slices.IndexFunc(mine, func(e ReviewEntry) bool { return e.Since == "" }); i >= 0 {
		view.Base = mine[i].Base
	}
	runs := map[runsOfRole]int{}
	for _, e := range mine {
		k := runsOfRole{e.Key.Agent, e.Record.Advisory}
		runs[k] = max(runs[k], e.Key.Number())
	}
	limit := roundLimit(b.Rounds)
	for _, e := range mine {
		q := workflow.Requirement{Agent: e.Key.Agent, Role: e.Role, Advisory: e.Record.Advisory}
		role := finishedRole(q, r.withReply(sid, e), RoundOf(entries, b.Branch, e.Key.Agent, e.Record.Advisory).Count, limit)
		role.Run, role.Runs, role.Advisory = e.Key.Number(), runs[runsOfRole{e.Key.Agent, e.Record.Advisory}], e.Record.Advisory
		if g, ok := r.givenFor(sid, e); ok {
			role.Why, role.Given = g.Why, g.Given
		}
		view.Roles = append(view.Roles, role)
		if e.StartedAt.Equal(latest.StartedAt) {
			view.CostUSD += e.CostUSD
		}
	}
	slices.SortStableFunc(view.Roles, func(a, c RoleReview) int {
		return cmp.Or(cmp.Compare(roleRank(a.Role), roleRank(c.Role)), cmp.Compare(rankOf(a.Advisory), rankOf(c.Advisory)), cmp.Compare(a.Agent, c.Agent), cmp.Compare(a.Run, c.Run))
	})
	for _, e := range entries {
		if e.Key.Branch == b.Branch {
			view.SessionCostUSD += e.CostUSD
		}
	}
	return view, true
}

func roleRank(role review.Role) int {
	if i := slices.Index(review.Roles, role); i >= 0 {
		return i
	}
	return len(review.Roles)
}

func rankOf(later bool) int {
	if later {
		return 1
	}
	return 0
}
