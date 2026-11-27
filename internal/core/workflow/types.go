package workflow

import (
	"slices"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/review"
)

type Step string

const (
	Define    Step = "define"
	Plan      Step = "plan"
	Implement Step = "implement"
	Review    Step = "review"
	Push      Step = "push"
)

var StepOrder = []Step{Define, Plan, Implement, Review, Push}

type Edits string

const (
	AnyEdits Edits = "any"
	NoEdits  Edits = "none"
)

type Profile string

const (
	Change     Profile = "change"
	Direct     Profile = "direct"
	Explore    Profile = "explore"
	CodeReview Profile = "review"
	Incident   Profile = "incident"
)

const (
	DefaultType  = "change"
	IncidentType = "incident"
)

const incidentUploadWarning = "Logs and screenshots go to Anthropic with the rest of the session. Remove customer data and secrets first."

const incidentStartWarning = "An incident is debugged at the commit that's deployed, and main is likely not what's running. Set Start from below to the branch whose head is deployed, and give the deployed commit or tag in the first message. When the session's start isn't that commit, knackpack diagnoses in a worktree of its own, outside this session's branch, and the page can't read or approve its root cause."

func (p Profile) StartWarning() string {
	if p == Incident {
		return incidentStartWarning
	}
	return ""
}

func (p Profile) UploadWarning() string {
	if p == Incident {
		return incidentUploadWarning
	}
	return ""
}

type WorkType struct {
	ID      string
	Name    string
	Steps   []Step
	Edits   Edits
	Push    bool
	Roles   []review.Role
	Builtin bool
}

var codeChange = WorkType{ID: DefaultType, Name: "Code change", Steps: []Step{Define, Plan, Implement, Review, Push}, Edits: AnyEdits, Push: true, Roles: []review.Role{review.BugHunter, review.Simplicity}, Builtin: true}

var Builtin = []WorkType{
	codeChange,
	{ID: IncidentType, Name: "Incident", Steps: []Step{Implement, Review, Push}, Edits: AnyEdits, Push: true, Roles: []review.Role{review.BugHunter, review.Simplicity}, Builtin: true},
	{ID: "explore", Name: "Explore", Steps: []Step{Implement}, Edits: AnyEdits, Roles: []review.Role{}, Builtin: true},
}

func builtinType(id string) (WorkType, bool) {
	for _, t := range Builtin {
		if t.ID == id {
			return t, true
		}
	}
	return WorkType{}, false
}

func TypeOr(types []WorkType, id string) WorkType {
	if i := slices.IndexFunc(types, func(t WorkType) bool { return t.ID == id }); i >= 0 {
		return types[i]
	}
	if i := slices.IndexFunc(types, func(t WorkType) bool { return t.ID == DefaultType }); i >= 0 {
		return types[i]
	}
	return codeChange
}

func (t WorkType) Plans() bool {
	return t.Profile() == Change && slices.Contains(t.Steps, Plan)
}

func MinimumRoles() []review.Role {
	return []review.Role{review.BugHunter, review.Simplicity}
}

func (t WorkType) Profile() Profile {
	switch {
	case t.Edits == NoEdits:
		return CodeReview
	case !t.Push:
		return Explore
	case t.Builtin && t.ID == IncidentType:
		return Incident
	case slices.Contains(t.Steps, Define):
		return Change
	}
	return Direct
}

func TypeProblems(t WorkType) []string {
	var out []string
	say := func(s string) { out = append(out, t.ID+": "+s) }
	seen := map[Step]bool{}
	last := -1
	for _, s := range t.Steps {
		at := slices.Index(StepOrder, s)
		switch {
		case at < 0:
			say(string(s) + " isn't a step kind")
		case seen[s]:
			say(string(s) + " appears twice")
		case at < last:
			say(string(s) + " comes after a step it must precede (the order is " + stepList() + ")")
		}
		seen[s] = true
		last = max(last, at)
	}
	if len(t.Steps) == 0 {
		say("a work type needs at least one step")
	}
	if seen[Plan] && !seen[Define] {
		say("plan needs a define step before it, since a plan is approved against agreed requirements")
	}
	if seen[Plan] && !seen[Implement] {
		say("a plan needs an implement step after it")
	}
	if t.Push {
		for _, s := range []Step{Implement, Review, Push} {
			if !seen[s] {
				article := "a"
				if s == Implement {
					article = "an"
				}
				say("a work type that pushes needs " + article + " " + string(s) + " step")
			}
		}
		for _, r := range MinimumRoles() {
			if !slices.Contains(t.Roles, r) {
				say("a work type that pushes needs the " + string(r) + " role")
			}
		}
		if t.Edits == NoEdits {
			say("a work type that changes no file can't push")
		}
	} else {
		if seen[Push] {
			say("a push step needs a work type that pushes")
		}
		if t.Edits == AnyEdits && (seen[Define] || seen[Plan] || seen[Review]) {
			say("a work type that changes files and doesn't push has only an implement step, as Explore does")
		}
	}
	if t.Edits == NoEdits && (seen[Implement] || seen[Plan] || seen[Define]) {
		say("a work type that changes no file has no define, plan or implement step")
	}
	return out
}

func stepList() string {
	names := make([]string, len(StepOrder))
	for i, s := range StepOrder {
		names[i] = string(s)
	}
	return strings.Join(names, ", ")
}
