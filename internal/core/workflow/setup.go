package workflow

import (
	"slices"

	"github.com/bezmoradi/software-factory/internal/core/review"
)

type Personal struct {
	Agents []string
	Rounds int
	Type   string
}

type Setup struct {
	Adapter     AdapterID
	Types       []WorkType
	DefaultType string
	Roles       map[review.Role]string
	Agents      []string
	FileRules   []FileRule
	Rounds      int
}

func SetupOf(team *Team, personal Personal, adapter AdapterID) (Setup, []string) {
	if team == nil {
		team = &Team{}
	}
	var problems []string
	chosen := adapter
	if team.Adapter != "" {
		chosen = team.Adapter
	}
	defaults := Adapters[Knackpack].Agents
	if chosen != "" {
		defaults = Adapters[chosen].Agents
	}
	types := append(slices.Clone(Builtin), team.WorkTypes...)
	var agents []string
	for _, a := range append(slices.Clone(team.Agents), personal.Agents...) {
		if !slices.Contains(agents, a) {
			agents = append(agents, a)
		}
	}
	if len(agents) > 20 {
		problems = append(problems, "the team file and this project's settings together name more than 20 agents")
	}
	rounds := 20
	if team.Rounds > 0 {
		rounds = team.Rounds
	}
	rounds = min(rounds, personal.Rounds)
	defaultType := team.DefaultType
	if personal.Type != "" && slices.ContainsFunc(types, func(t WorkType) bool { return t.ID == personal.Type }) {
		defaultType = personal.Type
	}
	roles := map[review.Role]string{}
	for _, r := range review.Roles {
		roles[r] = defaults[r]
		if a := team.Roles[r]; a != "" {
			roles[r] = a
		}
	}
	return Setup{Adapter: chosen, Types: types, DefaultType: defaultType, Roles: roles, Agents: agents, FileRules: slices.Clone(team.FileRules), Rounds: rounds}, problems
}

type Requirement struct {
	Agent string
	Role  review.Role
	Why   string
}

func Effective(t WorkType, files []string, listed bool) (WorkType, string) {
	if t.Edits != DocsEdits {
		return t, ""
	}
	feature, _ := builtinType("feature")
	if !listed {
		return feature, "Docs only needs what a Feature needs until the gate can list the files this branch changes."
	}
	for _, f := range files {
		if !DocFile(f) {
			return feature, "Docs only covers documentation, but this branch also changes " + clip(f, 120) + ", so it needs what a Feature needs."
		}
	}
	return t, ""
}

func Required(s Setup, t WorkType, files []string) []Requirement {
	var out []Requirement
	add := func(r Requirement) {
		if r.Agent != "" && !slices.ContainsFunc(out, func(x Requirement) bool { return x.Agent == r.Agent }) {
			out = append(out, r)
		}
	}
	article := "a "
	if t.ID == "docs" {
		article = ""
	}
	for _, r := range t.Roles {
		add(Requirement{Agent: s.Roles[r], Role: r, Why: "Needed for " + article + t.Name + ", as its " + r.Name()})
	}
	if t.Profile() == Exploring {
		return out
	}
	for _, rule := range fileReviewers {
		for _, f := range files {
			if rule.re.MatchString(f) {
				add(Requirement{Agent: rule.agent, Why: "This branch changes " + clip(f, 120)})
				break
			}
		}
	}
	for _, rule := range s.FileRules {
		re, ok := globRegexp(rule.Pattern, false)
		if !ok {
			continue
		}
		for _, f := range files {
			if re.MatchString(f) {
				add(Requirement{Agent: rule.Agent, Why: "This branch changes " + clip(f, 120) + ", which the team file sends to " + rule.Agent})
				break
			}
		}
	}
	for _, a := range s.Agents {
		add(Requirement{Agent: a, Why: "Added for this project"})
	}
	return out
}
