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
	Tasks     Step = "tasks"
	Implement Step = "implement"
	Review    Step = "review"
	Deliver   Step = "deliver"
)

var StepOrder = []Step{Define, Plan, Tasks, Implement, Review, Deliver}

type Edits string

const (
	AnyEdits  Edits = "any"
	DocsEdits Edits = "docs"
	NoEdits   Edits = "none"
)

type Profile string

const (
	Feature    Profile = "feature"
	Fix        Profile = "fix"
	Docs       Profile = "docs"
	Exploring  Profile = "explore"
	CodeReview Profile = "review"
)

type WorkType struct {
	ID      string
	Name    string
	Steps   []Step
	Edits   Edits
	Push    bool
	Roles   []review.Role
	Builtin bool
}

var Builtin = []WorkType{
	{ID: "feature", Name: "Feature", Steps: []Step{Define, Plan, Implement, Review, Deliver}, Edits: AnyEdits, Push: true, Roles: []review.Role{review.BugHunter, review.Simplicity}, Builtin: true},
	{ID: "fix", Name: "Fix", Steps: []Step{Implement, Review, Deliver}, Edits: AnyEdits, Push: true, Roles: []review.Role{review.BugHunter, review.Simplicity}, Builtin: true},
	{ID: "docs", Name: "Docs only", Steps: []Step{Implement, Review, Deliver}, Edits: DocsEdits, Push: true, Roles: []review.Role{review.Simplicity}, Builtin: true},
	{ID: "explore", Name: "Exploring", Steps: []Step{Implement}, Edits: AnyEdits, Roles: []review.Role{}, Builtin: true},
}

func builtinType(id string) (WorkType, bool) {
	for _, t := range Builtin {
		if t.ID == id {
			return t, true
		}
	}
	return WorkType{}, false
}

func MinimumRoles(e Edits) []review.Role {
	if e == DocsEdits {
		return []review.Role{review.Simplicity}
	}
	return []review.Role{review.BugHunter, review.Simplicity}
}

func (t WorkType) Profile() Profile {
	switch {
	case t.Edits == NoEdits:
		return CodeReview
	case !t.Push:
		return Exploring
	case slices.Contains(t.Steps, Plan):
		return Feature
	case t.Edits == DocsEdits:
		return Docs
	}
	return Fix
}

func TypeProblems(t WorkType, adapter AdapterID) []string {
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
	if seen[Tasks] && !seen[Plan] {
		say("tasks needs a plan step before it")
	}
	if seen[Define] && !seen[Plan] {
		say("define needs a plan step after it")
	}
	if seen[Plan] && !seen[Define] {
		say("plan needs a define step before it, since a plan is approved against agreed requirements")
	}
	if adapter == "" {
		adapter = Knackpack
	}
	if seen[Tasks] && Adapters[adapter].Commands[Tasks] == "" {
		say("tasks needs an adapter with a tasks step, such as \"spec-kit\"")
	}
	if (seen[Plan] || seen[Tasks]) && !seen[Implement] {
		say("a plan needs an implement step after it")
	}
	if t.Push {
		for _, s := range []Step{Implement, Review, Deliver} {
			if !seen[s] {
				article := "a"
				if s == Implement {
					article = "an"
				}
				say("a work type that pushes needs " + article + " " + string(s) + " step")
			}
		}
		for _, r := range MinimumRoles(t.Edits) {
			if !slices.Contains(t.Roles, r) {
				say("a work type that pushes needs the " + string(r) + " role")
			}
		}
		if t.Edits == NoEdits {
			say("a work type that changes no file can't push")
		}
	} else {
		if seen[Deliver] {
			say("deliver needs a work type that pushes")
		}
		if t.Edits == DocsEdits {
			say("a documentation-only work type must push, as Docs only does; one that doesn't push changes any file, as Exploring does")
		}
		if t.Edits == AnyEdits && (seen[Define] || seen[Plan] || seen[Tasks] || seen[Review]) {
			say("a work type that changes files and doesn't push has only an implement step, as Exploring does")
		}
	}
	if t.Edits == NoEdits && (seen[Implement] || seen[Plan] || seen[Define]) {
		say("a work type that changes no file has no define, plan or implement step")
	}
	if t.Edits == DocsEdits && seen[Plan] {
		say("a documentation-only work type has no plan step")
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
