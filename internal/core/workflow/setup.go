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
	Protect     []string
	Verify      []string
	Catalog     Catalog
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
	return Setup{Adapter: chosen, Types: types, DefaultType: defaultType, Roles: roles, Agents: agents, FileRules: slices.Clone(team.FileRules), Rounds: rounds, Protect: slices.Clone(team.Protect), Verify: slices.Clone(team.Verify)}, problems
}

type Requirement struct {
	Agent    string
	Role     review.Role
	Why      string
	Runs     int
	Advisory bool
	Missing  bool
}

func (q Requirement) RunCount() int {
	return min(max(q.Runs, 1), review.MaxRuns)
}

const (
	MaxAdvisory  = 5
	MaxDeeper    = 5
	MaxDepthSize = 10000000
)

type Depth struct {
	Advisory  []string
	Runs      map[string]int
	MaxFiles  int
	MaxLines  int
	Extra     []string
	ExtraRuns int
}

func (d Depth) Sized() bool {
	return (d.MaxFiles > 0 || d.MaxLines > 0) && (len(d.Extra) > 0 || d.ExtraRuns > 1)
}

func (d Depth) Problem() string {
	if len(d.Advisory) > MaxAdvisory || len(d.Extra) > MaxDeeper {
		return "a project names at most 5 advisory roles and 5 extra roles above the size"
	}
	for _, a := range append(slices.Clone(d.Advisory), d.Extra...) {
		if !agentName.MatchString(a) {
			return "each role is an agent name: letters, digits and . _ : @ -, at most 120 characters"
		}
	}
	if len(d.Runs) > 20 {
		return "a project sets repeated runs for at most 20 roles"
	}
	for a, n := range d.Runs {
		if !agentName.MatchString(a) || n < 1 || n > review.MaxRuns {
			return "a role runs 1 to 3 times per commit"
		}
	}
	if d.ExtraRuns < 0 || d.ExtraRuns > review.MaxRuns || d.MaxFiles < 0 || d.MaxLines < 0 || d.MaxFiles > MaxDepthSize || d.MaxLines > MaxDepthSize {
		return "a size is a whole number of files or lines, 0 to switch it off, and roles above it run 1 to 3 times"
	}
	return ""
}

func AgentName(s string) bool {
	return agentName.MatchString(s)
}

func Deepen(required []Requirement, d Depth, above bool, why string) []Requirement {
	out := make([]Requirement, 0, len(required)+len(d.Extra))
	for _, q := range required {
		q.Runs = max(q.RunCount(), min(max(d.Runs[q.Agent], 1), review.MaxRuns))
		out = append(out, q)
	}
	if !above || !d.Sized() {
		return out
	}
	for _, a := range d.Extra {
		if a != "" && !slices.ContainsFunc(out, func(x Requirement) bool { return x.Agent == a }) {
			out = append(out, Requirement{Agent: a, Why: why, Runs: min(max(d.Runs[a], 1), review.MaxRuns)})
		}
	}
	if d.ExtraRuns > 1 {
		for i := range out {
			out[i].Runs = max(out[i].Runs, min(d.ExtraRuns, review.MaxRuns))
		}
	}
	return out
}

func Advisory(required []Requirement, d Depth) []Requirement {
	var out []Requirement
	for _, a := range d.Advisory {
		if a == "" || slices.ContainsFunc(required, func(x Requirement) bool { return x.Agent == a }) || slices.ContainsFunc(out, func(x Requirement) bool { return x.Agent == a }) {
			continue
		}
		out = append(out, Requirement{Agent: a, Why: "Advisory for this project: it only warns, and never counts for the push", Advisory: true, Runs: 1})
	}
	return out
}

func RunTotal(list []Requirement) int {
	n := 0
	for _, q := range list {
		n += q.RunCount()
	}
	return n
}

func Effective(t WorkType, files []string, listed bool) (WorkType, string) {
	if t.Edits != DocsEdits {
		return t, ""
	}
	feature, _ := builtinType("feature")
	if !listed {
		return feature, "Docs only needs what a Feature needs until the gate can list the files this branch changes."
	}
	if others := NotDocs(files); len(others) > 0 {
		return feature, "Docs only covers documentation, but this branch also changes " + clip(others[0], 120) + ", so it needs what a Feature needs."
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
	byCatalog := ""
	if s.Catalog.State == CatalogUsed {
		byCatalog = ", which knackpack's catalog sends to this reviewer"
	}
	for _, m := range s.Catalog.matches(files) {
		add(Requirement{Agent: m.agent, Why: "This branch changes " + clip(m.file, 120) + byCatalog})
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
