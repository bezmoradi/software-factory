package workflow

import (
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/bezmoradi/software-factory/internal/core/review"
)

const MaxTeamBytes = 64 * 1024

type FileRule struct {
	Pattern string
	Agent   string
}

type Team struct {
	Adapter     AdapterID
	DefaultType string
	WorkTypes   []WorkType
	Roles       map[review.Role]string
	Agents      []string
	FileRules   []FileRule
	Rounds      int
}

var (
	typeID      = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	agentName   = regexp.MustCompile(`^[A-Za-z0-9][\w.:@-]{0,119}$`)
	plainName   = regexp.MustCompile(`^[^\x00-\x1f\x7f<>]{1,40}$`)
	controlOrBS = regexp.MustCompile(`[\x00-\x1f\\]`)
)

func PatternProblem(p string) string {
	switch {
	case p == "" || len([]rune(p)) > 120:
		return "a pattern must be 1 to 120 characters"
	case strings.Count(p, "*") > 4:
		return "a pattern may hold at most 4 wildcards"
	case strings.HasPrefix(p, "/") || slices.Contains(strings.Split(p, "/"), "..") || controlOrBS.MatchString(p):
		return "a pattern must be a relative path inside the repository"
	}
	return ""
}

func known(o *object, keys []string, at string, out *[]string) {
	for _, k := range o.keys {
		if !slices.Contains(keys, k) {
			*out = append(*out, at+": the key \""+clip(k, 40)+"\" isn't one the software factory knows")
		}
	}
}

func asString(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}

func ParseTeam(text string) (*Team, []string) {
	if len(text) > MaxTeamBytes {
		return nil, []string{"the file is over " + strconv.Itoa(MaxTeamBytes/1024) + " KiB"}
	}
	value, problem := strictJSON(text)
	if problem != "" {
		return nil, []string{"the file isn't strict JSON: " + problem}
	}
	root, ok := value.(*object)
	if !ok {
		return nil, []string{"the file must hold one JSON object"}
	}
	var out []string
	say := func(s string) { out = append(out, s) }
	known(root, []string{"version", "adapter", "defaultType", "workTypes", "roles", "agents", "fileRules", "checks", "rounds"}, "the file", &out)
	if v, ok := root.values["version"].(float64); !ok || v != 1 {
		say("\"version\" must be 1")
	}
	team := &Team{Roles: map[review.Role]string{}}
	if v, ok := root.values["adapter"]; ok {
		if s, _ := asString(v); s == string(Knackpack) || s == string(SpecKit) {
			team.Adapter = AdapterID(s)
		} else {
			say("\"adapter\" must be \"knackpack\" or \"spec-kit\"")
		}
	}
	parseWorkTypes(root, team, &out)
	if _, ok := root.values["roles"]; ok {
		say("\"roles\" can't pick the agents that fill the bug hunter and simplicity checker roles: only the reviewers installed in the reviewer image fill them, never a file in the repository")
	}
	if v, ok := root.values["agents"]; ok {
		list, isList := v.([]any)
		agents := []string{}
		valid := isList && len(list) <= 20
		for _, a := range list {
			s, isStr := asString(a)
			if !isStr || !agentName.MatchString(s) {
				valid = false
				break
			}
			if !slices.Contains(agents, s) {
				agents = append(agents, s)
			}
		}
		if valid {
			team.Agents = agents
		} else {
			say("\"agents\" must list at most 20 agent names")
		}
	}
	parseFileRules(root, team, &out)
	if _, ok := root.values["checks"]; ok {
		say("\"checks\" isn't supported: the engine has no trusted place to run the repository's commands, since a result from the agent container can be forged and reviewers only read")
	}
	if v, ok := root.values["rounds"]; ok {
		if n, isNum := v.(float64); isNum && n == math.Trunc(n) && n >= 1 && n <= 20 {
			team.Rounds = int(n)
		} else {
			say("\"rounds\" must be a whole number from 1 to 20")
		}
	}
	if v, ok := root.values["defaultType"]; ok {
		s, isStr := asString(v)
		_, builtin := builtinType(s)
		if isStr && (builtin || slices.ContainsFunc(team.WorkTypes, func(w WorkType) bool { return w.ID == s })) {
			team.DefaultType = s
		} else {
			say("\"defaultType\" must name a work type")
		}
	}
	if len(out) > 0 {
		return nil, out[:min(len(out), 40)]
	}
	return team, nil
}

func parseWorkTypes(root *object, team *Team, out *[]string) {
	v, ok := root.values["workTypes"]
	if !ok {
		return
	}
	types, isList := v.([]any)
	if !isList {
		*out = append(*out, "\"workTypes\" must be a list")
		return
	}
	if len(types) > 10 {
		*out = append(*out, "\"workTypes\" may hold at most 10 types")
	}
	for n, raw := range types[:min(len(types), 10)] {
		at := "workTypes[" + strconv.Itoa(n) + "]"
		say := func(s string) { *out = append(*out, at+s) }
		t, isObj := raw.(*object)
		if !isObj {
			say(" must be an object")
			continue
		}
		known(t, []string{"id", "name", "steps", "edits", "push", "roles"}, at, out)
		id, _ := asString(t.values["id"])
		if !typeID.MatchString(id) {
			id = ""
			say(": \"id\" must be 1 to 32 lowercase letters, digits or hyphens, starting with a letter")
		}
		if _, builtin := builtinType(id); builtin {
			say(": \"" + id + "\" is a built-in work type")
		}
		if slices.ContainsFunc(team.WorkTypes, func(w WorkType) bool { return w.ID == id }) {
			say(": \"" + id + "\" appears twice")
		}
		name, _ := asString(t.values["name"])
		name = strings.TrimFunc(name, unicode.IsSpace)
		if !plainName.MatchString(name) {
			name = ""
			say(": \"name\" must be 1 to 40 plain characters")
		}
		steps, stepsOK := stepsOf(t.values["steps"])
		if !stepsOK {
			say(": \"steps\" must list step kinds from " + stepList())
		}
		edits, _ := asString(t.values["edits"])
		editsOK := edits == string(AnyEdits) || edits == string(DocsEdits)
		switch {
		case edits == string(NoEdits):
			say(": \"edits\" can't be \"none\": a work type that changes no file reviewed a pull request from the git host, which the agent container can't reach")
		case !editsOK:
			say(": \"edits\" must be \"any\" or \"docs\"")
		}
		push, pushOK := t.values["push"].(bool)
		if !pushOK {
			say(": \"push\" must be true or false")
		}
		roles, rolesSet, rolesOK := rolesOf(t.values)
		if !rolesOK {
			say(": \"roles\" must list roles from " + string(review.BugHunter) + ", " + string(review.Simplicity))
		}
		if id == "" || name == "" || !stepsOK || !editsOK || !pushOK {
			continue
		}
		if !rolesSet || !rolesOK {
			roles = []review.Role{}
			if push {
				roles = MinimumRoles(Edits(edits))
			}
		}
		w := WorkType{ID: id, Name: name, Steps: steps, Edits: Edits(edits), Push: push, Roles: roles}
		*out = append(*out, TypeProblems(w, team.Adapter)...)
		team.WorkTypes = append(team.WorkTypes, w)
	}
}

func stepsOf(v any) ([]Step, bool) {
	list, ok := v.([]any)
	if !ok {
		return nil, false
	}
	steps := []Step{}
	for _, s := range list {
		name, isStr := asString(s)
		if !isStr || !slices.Contains(StepOrder, Step(name)) {
			return nil, false
		}
		steps = append(steps, Step(name))
	}
	return steps, true
}

func rolesOf(values map[string]any) (roles []review.Role, set, ok bool) {
	v, set := values["roles"]
	if !set {
		return nil, false, true
	}
	list, isList := v.([]any)
	if !isList {
		return nil, true, false
	}
	roles = []review.Role{}
	for _, r := range list {
		name, isStr := asString(r)
		if !isStr || !slices.Contains(review.Roles, review.Role(name)) {
			return nil, true, false
		}
		if !slices.Contains(roles, review.Role(name)) {
			roles = append(roles, review.Role(name))
		}
	}
	return roles, true, true
}

func parseFileRules(root *object, team *Team, out *[]string) {
	v, ok := root.values["fileRules"]
	if !ok {
		return
	}
	rules, isList := v.([]any)
	if !isList {
		*out = append(*out, "\"fileRules\" must be a list")
		return
	}
	if len(rules) > 20 {
		*out = append(*out, "\"fileRules\" may hold at most 20 rules")
	}
	for n, raw := range rules[:min(len(rules), 20)] {
		at := "fileRules[" + strconv.Itoa(n) + "]"
		f, isObj := raw.(*object)
		if !isObj {
			*out = append(*out, at+" must be an object")
			continue
		}
		known(f, []string{"pattern", "agent"}, at, out)
		pattern, isStr := asString(f.values["pattern"])
		why := "a pattern must be 1 to 120 characters"
		if isStr {
			why = PatternProblem(pattern)
		}
		if why != "" {
			*out = append(*out, at+": "+why)
		}
		agent, agentOK := asString(f.values["agent"])
		agentOK = agentOK && agentName.MatchString(agent)
		if !agentOK {
			*out = append(*out, at+": \"agent\" must name an agent")
		}
		if why == "" && agentOK {
			team.FileRules = append(team.FileRules, FileRule{Pattern: pattern, Agent: agent})
		}
	}
}
