package gate

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/level"
	"github.com/bezmoradi/software-factory/internal/core/review"
	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

type Result string

const (
	Allowed Result = "allowed"
	Pushed  Result = "pushed"
	Held    Result = "held"
	Denied  Result = "denied"
	Refused Result = "refused"
)

type Request struct {
	Commit string
	Branch string
	Remote string
	Force  bool
}

type Answer struct {
	Allow   bool
	Reason  string
	Commit  string
	Branch  string
	Remote  string
	Expires time.Time
	Used    bool
}

type Facts struct {
	Type     workflow.WorkType
	Level    level.Result
	Main     string
	Required []workflow.Requirement
	Offers   map[string]review.Offer
	Reviews  map[review.Key]review.Record
	Items    []string
	Answer   *Answer
	Now      time.Time
	Capped   map[string]int
}

type Decision struct {
	Result    Result
	Reason    string
	Missing   []string
	Exception string
}

var (
	fullCommit  = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	refRefused  = regexp.MustCompile(`[\x00-\x20\x7f~^:?*\[\\]|\.\.|@\{|//|/\.|\.lock(/|$)`)
	controlChar = regexp.MustCompile(`[\x00-\x08\x0b-\x1f\x7f]`)
)

func Reason(raw string) string {
	r := strings.TrimSpace(raw)
	if r == "" || len([]rune(r)) > 500 || controlChar.MatchString(r) {
		return ""
	}
	return r
}

func branchProblem(b string) string {
	switch {
	case b == "" || b == "HEAD":
		return "there is no branch to push"
	case strings.HasPrefix(b, "-") || strings.HasPrefix(b, "/") || strings.HasPrefix(b, ".") || strings.HasSuffix(b, "/") || strings.HasSuffix(b, ".") || refRefused.MatchString(b):
		return "the branch name " + clipped(b) + " isn't one git accepts"
	}
	return ""
}

func clipped(s string) string {
	r := []rune(s)
	if len(r) > 80 {
		return string(r[:80]) + "…"
	}
	return string(r)
}

func (a *Answer) binds(r Request, now time.Time) bool {
	return a != nil && !a.Used && a.Commit == r.Commit && a.Branch == r.Branch && a.Remote == r.Remote && now.Before(a.Expires)
}

func CappedItem(agent string, rounds int) string {
	unit := "rounds"
	if rounds == 1 {
		unit = "round"
	}
	return "the developer's decision on " + agent + ", which blocked in " + strconv.Itoa(rounds) + " " + unit + " on this branch, the limit this project set: they override its verdict, allow or deny the push, or hand the open findings off, on the page"
}

func Push(r Request, f Facts) Decision {
	refuse := func(why string) Decision { return Decision{Result: Refused, Reason: why} }
	if !fullCommit.MatchString(r.Commit) {
		return refuse("the commit to push isn't a full commit id")
	}
	if why := branchProblem(r.Branch); why != "" {
		return refuse(why)
	}
	if r.Remote == "" {
		return refuse("the engine has no remote URL recorded for this repository")
	}
	if !f.Type.Push {
		return refuse("this session's work type is " + f.Type.Name + ", which can't push; a push needs a work type that pushes, picked on the software factory page")
	}
	if r.Force && (f.Main == "" || r.Branch == f.Main) {
		return refuse("a push that rewrites the remote's main branch, or a branch the engine can't tell apart from it, is never allowed")
	}
	missing := append([]string{}, f.Items...)
	unsandboxed := false
	switch {
	case f.Level.Level == level.Locked && f.Level.Sandboxed:
	case f.Level.Level == level.Stopped && f.Level.Sandboxed:
	default:
		why := f.Level.Reason
		if why == "" {
			why = "no level was computed for this session"
		}
		missing = append(missing, "the sandbox check passing: "+why)
		unsandboxed = true
	}
	roles := workflow.MinimumRoles(f.Type.Edits)
	for _, role := range f.Type.Roles {
		if !slices.Contains(roles, role) {
			roles = append(roles, role)
		}
	}
	for _, role := range roles {
		if !slices.ContainsFunc(f.Required, func(q workflow.Requirement) bool { return q.Role == role && q.Agent != "" }) {
			missing = append(missing, "the "+role.Name()+" role filled by an agent: none is set for this work type")
		}
	}
	for _, q := range f.Required {
		var offer *review.Offer
		if o, ok := f.Offers[q.Agent]; ok {
			offer = &o
		}
		if why := review.Problem(f.Reviews, review.Key{Commit: r.Commit, Branch: r.Branch, Agent: q.Agent}, offer); why != "" {
			if n := f.Capped[q.Agent]; n > 0 {
				why = CappedItem(q.Agent, n)
			}
			missing = append(missing, why)
		}
	}
	if f.Answer.binds(r, f.Now) {
		if !f.Answer.Allow {
			why := "the developer denied this push"
			if note := Reason(f.Answer.Reason); note != "" {
				why += ": " + note
			}
			return Decision{Result: Denied, Reason: why, Missing: missing}
		}
		if len(missing) == 0 {
			return Decision{Result: Allowed}
		}
		if unsandboxed {
			return Decision{Result: Held, Reason: "no Allow pushes while the sandbox check fails, because the work may have come from an agent that could reach a git host; fix the check, then start a new session, review the work there and push it", Missing: missing}
		}
		if why := Reason(f.Answer.Reason); why != "" {
			return Decision{Result: Allowed, Missing: missing, Exception: why}
		}
		return Decision{Result: Held, Reason: "allowing a push with items missing needs the developer's reason", Missing: missing}
	}
	if len(missing) == 0 {
		return Decision{Result: Allowed}
	}
	return Decision{Result: Held, Reason: "held until it has: " + strings.Join(missing, "; "), Missing: missing}
}
