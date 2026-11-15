package session

import (
	"context"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/gate"
	"github.com/bezmoradi/software-factory/internal/core/review"
)

const (
	maxFactReason = 300
	maxFactItem   = 150
)

var (
	docPath = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,200}$`)
	docHash = regexp.MustCompile(`^[0-9a-f]{12,128}$`)
)

type ApprovedDoc struct {
	Kind     string
	Path     string
	Hash     string
	Approved bool
	Changed  bool
}

func (w *Work) ApprovedDocs(sid string) []ApprovedDoc {
	v, err := w.View(context.Background(), sid)
	if err != nil {
		return nil
	}
	var out []ApprovedDoc
	for _, d := range []struct {
		kind string
		doc  *WorkDoc
	}{{"requirements", v.Requirements}, {"plan", v.Plan}} {
		if d.doc == nil || d.doc.Reported || d.doc.OnBase {
			continue
		}
		a := ApprovedDoc{Kind: d.kind, Approved: d.doc.Approved, Changed: d.doc.Changed}
		if docPath.MatchString(d.doc.Path) {
			a.Path = d.doc.Path
		}
		if docHash.MatchString(d.doc.Hash) {
			a.Hash = d.doc.Hash
		}
		out = append(out, a)
	}
	return out
}

func (r *Registry) CompactNote(sid string, docs []ApprovedDoc) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.admitted[sid]
	if a == nil {
		return ""
	}
	facts := []string{"Your conversation was compacted. These are the engine's own records for this session, not a summary, and they outrank what the summary says:"}
	facts = append(facts, docFacts(docs)...)
	facts = append(facts, pushFact(a.push))
	facts = append(facts, findingFact(a.review))
	facts = append(facts, r.waitingFact(sid, a))
	return strings.Join(slices.DeleteFunc(facts, func(s string) bool { return s == "" }), " ")
}

func docFacts(docs []ApprovedDoc) []string {
	if len(docs) == 0 {
		return []string{"No requirements or plan document is approved for this session."}
	}
	var out []string
	for _, d := range docs {
		name := d.Kind
		if d.Path != "" {
			name += " `" + d.Path + "`"
		}
		if d.Hash != "" {
			name += " (text hash " + d.Hash[:12] + ")"
		}
		switch {
		case d.Approved:
			out = append(out, "The person approved the "+name+".")
		case d.Changed:
			out = append(out, "The "+name+" changed after the person approved it, so it isn't approved now.")
		default:
			out = append(out, "The "+name+" isn't approved.")
		}
	}
	return out
}

func pushFact(p *PushView) string {
	switch {
	case p == nil:
		return "No push is held."
	case p.Running:
		return "A push is being decided now."
	case p.Result == string(gate.Held):
		fact := "A push"
		if p.Commit != "" {
			fact += " of commit `" + shortSHA(CleanLine(p.Commit, 64)) + "`"
		}
		fact += " is held"
		if why := unmetFact(p.Unmet); why != "" {
			fact += " until it has: " + why
		}
		return fact + "."
	}
	return "No push is held."
}

func unmetFact(items []string) string {
	var parts []string
	size := 0
	for i, item := range items {
		item = strings.TrimSuffix(CleanLine(item, maxFactItem), ".")
		if item == "" {
			continue
		}
		if size+len(item) > maxFactReason {
			parts = append(parts, "and "+strconv.Itoa(len(items)-i)+" more")
			break
		}
		parts = append(parts, item)
		size += len(item) + 2
	}
	return strings.Join(parts, "; ")
}

func findingFact(v *ReviewView) string {
	if v == nil {
		return "No reviewer has open blocking findings."
	}
	var roles []string
	for _, role := range v.Roles {
		if role.State == review.Block && role.Override == "" {
			roles = append(roles, CleanLine(role.Name, 100)+" ("+CleanLine(role.Agent, 100)+")")
		}
	}
	if len(roles) == 0 {
		return "No reviewer has open blocking findings."
	}
	fact := "Open blocking findings"
	if v.Commit != "" {
		fact += " on commit `" + shortSHA(CleanLine(v.Commit, 64)) + "`"
	}
	return fact + " from " + strconv.Itoa(len(roles)) + " " + plural(len(roles), "reviewer", "reviewers") + ": " + strings.Join(roles, "; ") + "."
}

func (r *Registry) waitingFact(sid string, a *admitted) string {
	issues, pulls, comments := 0, 0, 0
	for _, v := range a.forge {
		if v.State != ForgeWaiting {
			continue
		}
		switch v.Kind {
		case ForgeIssue:
			issues++
		case ForgePull:
			pulls++
		default:
			comments++
		}
	}
	asks := 0
	for _, k := range r.cards {
		if k.sid == sid && !k.answered {
			asks++
		}
	}
	var parts []string
	for _, c := range []struct {
		n         int
		one, many string
	}{{issues, "issue card", "issue cards"}, {pulls, "pull request card", "pull request cards"}, {comments, "pull request comment card", "pull request comment cards"}, {asks, "question or permission card", "question or permission cards"}} {
		if c.n > 0 {
			parts = append(parts, strconv.Itoa(c.n)+" "+plural(c.n, c.one, c.many))
		}
	}
	if len(parts) == 0 {
		return "Nothing waits for the person on the page."
	}
	return "Waiting for the person on the page: " + strings.Join(parts, ", ") + "; don't ask for these again."
}
