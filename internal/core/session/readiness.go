package session

import (
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/push"
)

const (
	ReadyFound   = "found"
	ReadyMissing = "missing"
	ReadyUnknown = "unknown"

	ReadyInstructions = "instructions"
	ReadyTests        = "tests"
	ReadyCI           = "ci"
	ReadyLint         = "lint"
	ReadyCheck        = "check"
	ReadyTeam         = "team"
	ReadyProtection   = "protection"

	MaxListedEntries = 5000
	maxReadyFile     = 1 << 20
	maxReadyFound    = 10
	npmNoTest        = "no test specified"
)

type TreeEntry struct {
	Name   string
	File   bool
	Folder bool
}

type MainFiles interface {
	ListTree(repo, commit, dir string) ([]TreeEntry, error)
	ReadFile(repo, commit, path string) (push.File, error)
}

type ReadinessLine struct {
	Kind  string
	State string
	Found []string
	Note  string
}

var (
	makeTestTarget        = regexp.MustCompile(`(?m)^test[ \t]*:([^=]|$)`)
	ruffTable             = regexp.MustCompile(`(?m)^[ \t]*\[tool\.ruff[.\]]`)
	readyInstructionFiles = []string{"CLAUDE.md", "AGENTS.md"}
	readyMakefiles        = []string{"Makefile", "makefile", "GNUmakefile"}
	readyLintFiles        = []string{".golangci.yml", ".golangci.yaml", ".golangci.toml", ".golangci.json", "ruff.toml", ".ruff.toml", ".rubocop.yml", "biome.json", "biome.jsonc"}
	readyLintPrefixes     = []string{".eslintrc", "eslint.config."}
	readyTestFiles        = [][2]string{{"go.mod", "go.mod (go test)"}, {"pyproject.toml", "pyproject.toml (pytest)"}, {"pytest.ini", "pytest.ini (pytest)"}, {"Cargo.toml", "Cargo.toml (cargo test)"}}
)

var readySuggestion = map[string]string{
	ReadyInstructions: "Add a CLAUDE.md or AGENTS.md at the repository's root that says how to build, test and lint the project and what its rules are. Claude Code reads it at the start of every session.",
	ReadyTests:        "Add a test command the agents can run: a test script in package.json, a test target in the Makefile, or the language's own runner (go test, pytest, cargo test) with its project file.",
	ReadyCI:           "Add a workflow in .github/workflows/ that runs the tests and linters on pull requests, so every change is checked before it merges.",
	ReadyLint:         "Add a linter's configuration at the root (.golangci.yml, eslint.config.js, ruff.toml or [tool.ruff] in pyproject.toml, .rubocop.yml, biome.json), so the agents and CI check the code the same way.",
	ReadyCheck:        "Set a check command under the project's Check and setup commands; the engine runs it in the session's worktree when the agent ends a turn.",
	ReadyTeam:         "Add " + TeamFile + " on the main branch to set the project's work types, review roles and file rules, and approve it under Team rules from main.",
	ReadyProtection:   "Protect the main branch on the git host and require reviews or status checks, so nothing merges into main unchecked.",
}

type readyRoot struct {
	files   MainFiles
	path    string
	commit  string
	entries []TreeEntry
}

func (rr readyRoot) has(name string) bool {
	return slices.ContainsFunc(rr.entries, func(e TreeEntry) bool { return e.Name == name && e.File })
}

func (rr readyRoot) text(name string) ([]byte, bool) {
	if !rr.has(name) {
		return nil, false
	}
	f, err := rr.files.ReadFile(rr.path, rr.commit, name)
	if err != nil || len(f.Data) > maxReadyFile {
		return nil, false
	}
	return f.Data, true
}

func readyLine(kind string, found []string, unknown string) ReadinessLine {
	switch {
	case len(found) > 0:
		if len(found) > maxReadyFound {
			found = found[:maxReadyFound]
		}
		return ReadinessLine{Kind: kind, State: ReadyFound, Found: found}
	case unknown != "":
		return ReadinessLine{Kind: kind, State: ReadyUnknown, Note: unknown}
	}
	return ReadinessLine{Kind: kind, State: ReadyMissing, Note: readySuggestion[kind]}
}

func (rr readyRoot) instructions() ReadinessLine {
	var found []string
	for _, name := range readyInstructionFiles {
		if rr.has(name) {
			found = append(found, name)
		}
	}
	return readyLine(ReadyInstructions, found, "")
}

func (rr readyRoot) tests() ReadinessLine {
	var found []string
	note := ""
	if data, ok := rr.text("package.json"); ok {
		var pkg struct{ Scripts map[string]any }
		if json.Unmarshal(data, &pkg) == nil {
			if s, ok := pkg.Scripts["test"].(string); ok && strings.TrimSpace(s) != "" {
				if strings.Contains(s, npmNoTest) {
					note = "package.json's test script is npm's placeholder, which only fails. "
				} else {
					found = append(found, "package.json test script")
				}
			}
		}
	}
	for _, name := range readyMakefiles {
		if data, ok := rr.text(name); ok && makeTestTarget.Match(data) {
			found = append(found, name+" test target")
			break
		}
	}
	for _, f := range readyTestFiles {
		if rr.has(f[0]) {
			found = append(found, f[1])
		}
	}
	line := readyLine(ReadyTests, found, "")
	if line.State == ReadyMissing {
		line.Note = note + line.Note
	}
	return line
}

func (rr readyRoot) lint() ReadinessLine {
	var found []string
	for _, e := range rr.entries {
		if !e.File {
			continue
		}
		if slices.Contains(readyLintFiles, e.Name) || slices.ContainsFunc(readyLintPrefixes, func(p string) bool { return strings.HasPrefix(e.Name, p) }) {
			found = append(found, CleanLine(e.Name, 100))
		}
	}
	if data, ok := rr.text("pyproject.toml"); ok && ruffTable.Match(data) {
		found = append(found, "[tool.ruff] in pyproject.toml")
	}
	slices.Sort(found)
	return readyLine(ReadyLint, found, "")
}

func (rr readyRoot) team() ReadinessLine {
	if !rr.has(TeamFile) {
		return readyLine(ReadyTeam, nil, "")
	}
	if _, err := rr.files.ReadFile(rr.path, rr.commit, TeamFile); err != nil {
		if errors.Is(err, push.ErrNotFound) {
			return readyLine(ReadyTeam, nil, "")
		}
		return readyLine(ReadyTeam, nil, "The engine couldn't read "+TeamFile+" on the main branch.")
	}
	return readyLine(ReadyTeam, []string{TeamFile}, "")
}

func ciLine(workflows []WorkflowFile, problem string) ReadinessLine {
	if problem != "" {
		return readyLine(ReadyCI, nil, problem)
	}
	var found []string
	for _, w := range workflows {
		found = append(found, CleanLine(w.Path, 200))
	}
	return readyLine(ReadyCI, found, "")
}

func checkLine(command string) ReadinessLine {
	if strings.TrimSpace(command) == "" {
		return readyLine(ReadyCheck, nil, "")
	}
	return readyLine(ReadyCheck, []string{CleanLine(maskSecrets(command), MaxProjectCommand)}, "")
}

func protectionLine(p Protection) ReadinessLine {
	switch p.State {
	case ProtectionOn:
		return readyLine(ReadyProtection, []string{"main is protected"}, "")
	case ProtectionOff:
		return readyLine(ReadyProtection, nil, "")
	}
	why := "The engine can't tell whether main is protected; the token may need " + NeedAdministration + "."
	if len(p.Notes) > 0 {
		why = p.Notes[0]
	}
	return readyLine(ReadyProtection, nil, why)
}

func (f *ProjectFacts) readiness(r Repository, main string, workflows []WorkflowFile, workflowsProblem string, p Protection) []ReadinessLine {
	check := ""
	if f.cfg.Check != nil {
		check = f.cfg.Check(r.Name)
	}
	unknown := ""
	var root readyRoot
	switch {
	case main == "":
		unknown = "The engine hasn't fetched the main branch yet; start a session to fetch it."
	case f.cfg.Files == nil:
		unknown = "This engine doesn't read the main branch's files."
	default:
		entries, err := f.cfg.Files.ListTree(r.Path, main, "")
		if err != nil {
			unknown = clip("The engine couldn't list the main branch's files: "+maskSecrets(err.Error()), 400)
		}
		root = readyRoot{files: f.cfg.Files, path: r.Path, commit: main, entries: entries}
	}
	var out []ReadinessLine
	if unknown != "" {
		for _, kind := range []string{ReadyInstructions, ReadyTests, ReadyLint, ReadyTeam} {
			out = append(out, readyLine(kind, nil, unknown))
		}
	} else {
		out = append(out, root.instructions(), root.tests(), root.lint(), root.team())
	}
	ci := workflowsProblem
	if main == "" && ci == "" {
		ci = unknown
	}
	out = append(out, ciLine(workflows, ci), checkLine(check), protectionLine(p))
	order := []string{ReadyInstructions, ReadyTests, ReadyCI, ReadyLint, ReadyCheck, ReadyTeam, ReadyProtection}
	slices.SortStableFunc(out, func(a, b ReadinessLine) int { return slices.Index(order, a.Kind) - slices.Index(order, b.Kind) })
	return out
}
