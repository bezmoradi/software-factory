package session

import (
	"slices"
	"strings"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/review"
	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

const maxCheckedReviewers = 20

type PushReviewer struct {
	Agent        string
	Name         string
	Required     bool
	Advisory     bool
	State        string
	Overridden   bool
	Counted      bool
	SetAside     string
	Unchecked    bool
	Runs         int
	Blocking     int
	Requirements int
	Verified     int
	Violated     int
	NotVerified  int
}

type PushRun struct {
	Kind  string
	State string
	Code  int
}

type PushChecked struct {
	Reviewers  []PushReviewer
	Runs       []PushRun
	Unreadable bool
}

func (c *PushChecked) clone() *PushChecked {
	if c == nil {
		return nil
	}
	out := *c
	out.Reviewers = slices.Clone(c.Reviewers)
	out.Runs = slices.Clone(c.Runs)
	return &out
}

func (p *Pushes) checkedKept(sid, commit string) *PushChecked {
	p.mu.Lock()
	defer p.mu.Unlock()
	if v, ok := p.views[sid]; ok && v.Commit == commit {
		return v.CheckedBy.clone()
	}
	return nil
}

func (p *Pushes) checkedOf(sid string, t PushTarget, head string, required []workflow.Requirement, records map[review.Key]review.Record, unchecked bool) *PushChecked {
	out := &PushChecked{Reviewers: []PushReviewer{}, Runs: p.commitRuns(sid, head)}
	var entries map[review.Key]ReviewEntry
	if p.cfg.Reviews != nil {
		var err error
		if entries, err = p.cfg.Reviews.Entries(); err != nil {
			out.Unreadable = true
		}
	}
	merged := map[review.Key]ReviewEntry{}
	for k, e := range entries {
		if k.Commit != head || k.Branch != t.Branch {
			continue
		}
		if r, ok := records[k]; ok {
			e.Record = r
		}
		merged[k] = e
	}
	kept := map[review.Key]review.Record{}
	runs := map[string]int{}
	for k, e := range merged {
		kept[k] = e.Record
		runs[k.Agent] = max(runs[k.Agent], k.Number())
	}
	seen := map[string]bool{}
	for _, q := range required {
		if seen[q.Agent] || len(out.Reviewers) >= maxCheckedReviewers {
			continue
		}
		seen[q.Agent] = true
		out.Reviewers = append(out.Reviewers, p.reviewerOf(sid, merged, kept, head, t.Branch, q, true, unchecked))
	}
	var others []workflow.Requirement
	for k, e := range merged {
		if seen[k.Agent] {
			continue
		}
		seen[k.Agent] = true
		others = append(others, workflow.Requirement{Agent: k.Agent, Role: e.Role, Runs: runs[k.Agent], Advisory: e.Record.Advisory})
	}
	slices.SortFunc(others, func(a, b workflow.Requirement) int { return strings.Compare(a.Agent, b.Agent) })
	for _, q := range others {
		if len(out.Reviewers) >= maxCheckedReviewers {
			break
		}
		out.Reviewers = append(out.Reviewers, p.reviewerOf(sid, merged, kept, head, t.Branch, q, false, unchecked))
	}
	return out
}

func (p *Pushes) reviewerOf(sid string, merged map[review.Key]ReviewEntry, records map[review.Key]review.Record, head, branch string, q workflow.Requirement, required, unchecked bool) PushReviewer {
	out := PushReviewer{Agent: q.Agent, Name: ReviewerName(q), Required: required, Advisory: q.Advisory, State: "none"}
	k := review.Key{Commit: head, Branch: branch, Agent: q.Agent}
	for _, e := range runsOf(merged, k) {
		out.Runs++
		if e.Record.Advisory {
			out.Advisory = true
		}
		if s := e.Record.State; s == review.Pass || s == review.Block || s == review.Unknown {
			out.Blocking += len(BlockingFindings(p.replyText(sid, e)))
		}
	}
	e, complete, found := pooledEntry(merged, k, q.RunCount())
	if !found {
		return out
	}
	out.State = string(e.Record.State)
	if !complete && e.Record.State == review.Pass {
		out.State = "none"
	}
	_, _, out.Overridden = review.Outcome(records, k, q.RunCount())
	switch e.Record.Channel {
	case review.ApprovalChanged, review.BelowMinimum, review.Unchecked:
		if e.Record.State == review.Unknown {
			out.SetAside = CleanLine(e.Record.Held, 300)
		}
	}
	if required {
		offer := offerFor(p.cfg.RoleAgents, q.Agent)
		why, _ := review.Pooled(records, k, q.RunCount(), &offer)
		out.Counted = why == ""
	}
	out.Unchecked = unchecked && out.State == string(review.Pass) && !out.Advisory
	if len(e.ReqIDs) > 0 && e.Record.State != review.Running && e.Record.State != review.Failed {
		out.Requirements = len(e.ReqIDs)
		for _, c := range CoverageLines(p.replyText(sid, e), e.ReqIDs).Lines {
			switch c.Status {
			case CoverageVerified:
				out.Verified++
			case CoverageViolated:
				out.Violated++
			case CoverageNotVerified:
				out.NotVerified++
			}
		}
	}
	return out
}

func (p *Pushes) replyText(sid string, e ReviewEntry) string {
	if p.cfg.Reviews == nil {
		return e.Message
	}
	text, _ := p.cfg.Reviews.ReplyOf(sid, e)
	return text
}

func (p *Pushes) commitRuns(sid, commit string) []PushRun {
	v := p.verifier()
	if v == nil {
		return []PushRun{}
	}
	out := []PushRun{}
	for _, r := range v.CommitRuns(sid, commit, p.cfg.Now()) {
		out = append(out, PushRun{Kind: string(r.Kind), State: r.State, Code: r.Code})
	}
	return out
}

func (v *Verifier) CommitRuns(sid, commit string, at time.Time) []VerifyRun {
	t, ok := v.targetOf(sid)
	if !ok {
		return nil
	}
	id, _, known := v.project(t.Name)
	if !known {
		return nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	var out []VerifyRun
	for _, kind := range VerifyKinds {
		for _, r := range slices.Backward(v.runs) {
			if r.Project == id && r.Commit == commit && r.Kind == kind && !r.Started.After(at) {
				out = append(out, r)
				break
			}
		}
	}
	return out
}
