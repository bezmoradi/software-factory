package workflow

import (
	"slices"
	"strings"
	"unicode"

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
	Workflow    Adapter
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
	return Setup{Adapter: chosen, Workflow: Adapters[Knackpack], Types: types, DefaultType: defaultType, Roles: roles, Agents: agents, FileRules: slices.Clone(team.FileRules), Rounds: rounds, Protect: slices.Clone(team.Protect), Verify: slices.Clone(team.Verify)}, problems
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
	MaxAdded     = 5
	MaxDepthSize = 10000000
	TwiceRuns    = 2
)

type Depth struct {
	Always   []string
	Advisory []string
	Extra    []string
	MaxLines int
	Twice    bool
}

func (d Depth) Sized() bool {
	return d.MaxLines > 0 && (len(d.Extra) > 0 || d.Twice)
}

func (d Depth) Added() []string {
	return slices.Concat(d.Always, d.Extra, d.Advisory)
}

const (
	DepthSizeRule  = "A whole number of lines, 0 to switch it off."
	DepthCountRule = "At most 5 added reviewers."
	DepthNameRule  = "Agent names: letters, digits and . _ : @ -, up to 120 each."
)

func (d Depth) Problem() (string, string) {
	added := d.Added()
	if len(added) > MaxAdded {
		return "reviewers", DepthCountRule
	}
	if slices.ContainsFunc(added, func(a string) bool { return !agentName.MatchString(a) }) {
		return "reviewers", DepthNameRule
	}
	if d.MaxLines < 0 || d.MaxLines > MaxDepthSize {
		return "maxLines", DepthSizeRule
	}
	return "", ""
}

func AgentName(s string) bool {
	return agentName.MatchString(s)
}

func AgentNames(typed string) []string {
	out := []string{}
	for _, name := range strings.FieldsFunc(typed, func(r rune) bool { return r == ',' || unicode.IsSpace(r) }) {
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}

func Deepen(required []Requirement, d Depth, above bool, why string) []Requirement {
	out := slices.Clone(required)
	add := func(a, why string) {
		if a != "" && !slices.ContainsFunc(out, func(x Requirement) bool { return x.Agent == a }) {
			out = append(out, Requirement{Agent: a, Why: why})
		}
	}
	for _, a := range d.Always {
		add(a, "Required by this project on every change")
	}
	if !above || !d.Sized() {
		return out
	}
	for _, a := range d.Extra {
		add(a, why)
	}
	if d.Twice {
		for i := range out {
			out[i].Runs = max(out[i].RunCount(), TwiceRuns)
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
	switch t.ID {
	case "docs":
		article = ""
	case IncidentType:
		article = "an "
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
