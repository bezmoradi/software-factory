package session

import (
	"regexp"
	"slices"
	"strings"
)

const (
	MaxWorkflowFiles = 100
	MaxWorkflowBytes = 1 << 20
	maxWorkflowMarks = 12
	SampleBranch     = "sf/0123abcd"
)

type WorkflowEvent struct {
	Name           string
	Branches       []string
	BranchesIgnore []string
	HasBranches    bool
	HasIgnore      bool
	HasTags        bool
	HasPaths       bool
}

type WorkflowFile struct {
	Path    string
	SHA     string
	Name    string
	Text    string
	Problem string
	Events  []WorkflowEvent
	IDToken bool
	Inherit bool
}

type WorkflowReader interface {
	Workflows(repo, commit string) ([]WorkflowFile, error)
}

type WorkflowRun struct {
	Path    string
	Name    string
	When    string
	Secrets []string
	IDToken bool
	Trusted []string
	New     bool
	Changed bool
	Problem string
}

var (
	secretDot     = regexp.MustCompile(`secrets\s*\.\s*([A-Za-z_][A-Za-z0-9_-]*)`)
	secretIndex   = regexp.MustCompile(`secrets\s*\[\s*['"]([^'"\]]{1,100})['"]\s*\]`)
	secretWhole   = regexp.MustCompile(`(?i)toJSON\(\s*secrets\s*\)`)
	secretInherit = regexp.MustCompile(`(?m)^\s*secrets\s*:\s*inherit\s*$`)
)

func WorkflowRuns(head, base []WorkflowFile, branch string, compare bool) []WorkflowRun {
	before := map[string]string{}
	for _, f := range base {
		before[f.Path] = f.SHA
	}
	var out []WorkflowRun
	for _, f := range head {
		run := WorkflowRun{Path: f.Path, Name: CleanLine(f.Name, 120)}
		if compare {
			sha, ok := before[f.Path]
			run.New = !ok
			run.Changed = ok && sha != f.SHA
		}
		if f.Problem != "" {
			run.Problem = CleanLine(f.Problem, 300)
			out = append(out, run)
			continue
		}
		when, runs := pushRuns(f.Events, branch)
		if !runs {
			continue
		}
		run.When = when
		run.Secrets = secretsUsed(f.Text, f.Inherit)
		run.IDToken = f.IDToken
		for _, e := range f.Events {
			if (e.Name == "pull_request_target" || e.Name == "workflow_run") && !slices.Contains(run.Trusted, e.Name) {
				run.Trusted = append(run.Trusted, e.Name)
			}
		}
		out = append(out, run)
	}
	slices.SortFunc(out, func(a, b WorkflowRun) int { return strings.Compare(a.Path, b.Path) })
	return out
}

func pushRuns(events []WorkflowEvent, branch string) (string, bool) {
	var whens []string
	for _, e := range events {
		switch e.Name {
		case "push":
			if !e.HasBranches && !e.HasIgnore && e.HasTags {
				continue
			}
			if e.HasBranches && !branchFilter(e.Branches, branch) {
				continue
			}
			if e.HasIgnore && anyBranch(e.BranchesIgnore, branch) {
				continue
			}
			if e.HasPaths {
				whens = append(whens, "on push, when the change touches the paths it names")
			} else {
				whens = append(whens, "on push")
			}
		case "create":
			whens = append(whens, "when the branch is created")
		}
	}
	return strings.Join(whens, "; "), len(whens) > 0
}

func branchFilter(patterns []string, branch string) bool {
	matched := false
	for _, p := range patterns {
		if neg, ok := strings.CutPrefix(p, "!"); ok {
			if globMatch(neg, branch) {
				matched = false
			}
			continue
		}
		if globMatch(p, branch) {
			matched = true
		}
	}
	return matched
}

func anyBranch(patterns []string, branch string) bool {
	return slices.ContainsFunc(patterns, func(p string) bool { return globMatch(p, branch) })
}

func globMatch(pattern, name string) bool {
	if len(pattern) > 255 {
		return false
	}
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch {
		case c == '*' && i+1 < len(pattern) && pattern[i+1] == '*':
			b.WriteString(".*")
			i++
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?' || c == '+':
			if i == 0 {
				b.WriteString(regexp.QuoteMeta(string(c)))
			} else {
				b.WriteByte(c)
			}
		case c == '[':
			end := strings.IndexByte(pattern[i:], ']')
			if end < 0 {
				b.WriteString(`\[`)
				continue
			}
			class := pattern[i+1 : i+end]
			b.WriteString("[" + strings.ReplaceAll(class, `\`, `\\`) + "]")
			i += end
		case c == '\\' && i+1 < len(pattern):
			b.WriteString(regexp.QuoteMeta(string(pattern[i+1])))
			i++
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	return err == nil && re.MatchString(name)
}

func secretsUsed(text string, inherit bool) []string {
	var out []string
	add := func(name string) {
		if strings.EqualFold(name, "GITHUB_TOKEN") || slices.Contains(out, name) || len(out) >= maxWorkflowMarks {
			return
		}
		out = append(out, CleanLine(name, 100))
	}
	for line := range strings.SplitSeq(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		for _, m := range secretDot.FindAllStringSubmatch(line, -1) {
			add(m[1])
		}
		for _, m := range secretIndex.FindAllStringSubmatch(line, -1) {
			add(m[1])
		}
		if secretWhole.MatchString(line) {
			add("every secret, through toJSON(secrets)")
		}
		if secretInherit.MatchString(line) {
			add("every secret, passed on with secrets: inherit")
		}
	}
	if inherit {
		add("every secret, passed on with secrets: inherit")
	}
	return out
}
