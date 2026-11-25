package session

import (
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

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
	ReadyVerified     = "verified"

	FixInCommands = "commands"

	MaxListedEntries = 5000
	maxReadyFile     = 1 << 20
	maxReadyFound    = 10
	npmNoTest        = "no test specified"
	readyNotFetched  = "Main isn't fetched yet; start a session to fetch it"
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
	Value string
	Note  string
	FixIn string

	Commit string
	At     time.Time
	Result string
	Code   int
	Tail   string
}

var (
	makeTestTarget        = regexp.MustCompile(`(?m)^test[ \t]*:([^=]|$)`)
	ruffTable             = regexp.MustCompile(`(?m)^[ \t]*\[tool\.ruff[.\]]`)
	readyInstructionFiles = []string{"CLAUDE.md", "AGENTS.md"}
	readyMakefiles        = []string{"Makefile", "makefile", "GNUmakefile"}
	readyLintFiles        = map[string]string{".golangci.yml": "golangci-lint", ".golangci.yaml": "golangci-lint", ".golangci.toml": "golangci-lint", ".golangci.json": "golangci-lint", "ruff.toml": "ruff", ".ruff.toml": "ruff", ".rubocop.yml": "RuboCop", "biome.json": "Biome", "biome.jsonc": "Biome"}
	readyLintPrefixes     = [][2]string{{".eslintrc", "ESLint"}, {"eslint.config.", "ESLint"}}
	readyTestFiles        = [][3]string{{"go.mod", "go.mod (go test)", "go test"}, {"pyproject.toml", "pyproject.toml (pytest)", "pytest"}, {"pytest.ini", "pytest.ini (pytest)", "pytest"}, {"Cargo.toml", "Cargo.toml (cargo test)", "cargo test"}}
)

var readySuggestion = map[string]string{
	ReadyInstructions: "Add CLAUDE.md or AGENTS.md at the root",
	ReadyTests:        "Add a test script, a test target or a test runner's project file",
	ReadyCI:           "Add a workflow in .github/workflows/",
	ReadyLint:         "Add a linter's configuration at the root",
	ReadyCheck:        "Set it under Check and setup commands",
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

func readyLine(kind string, found []string, value, unknown string) ReadinessLine {
	switch {
	case len(found) > 0:
		if len(found) > maxReadyFound {
			found = found[:maxReadyFound]
		}
		return ReadinessLine{Kind: kind, State: ReadyFound, Found: found, Value: value}
	case unknown != "":
		return ReadinessLine{Kind: kind, State: ReadyUnknown, Note: unknown}
	}
	line := ReadinessLine{Kind: kind, State: ReadyMissing, Note: readySuggestion[kind]}
	if kind == ReadyCheck {
		line.FixIn = FixInCommands
	}
	return line
}

func shortList(names []string) string {
	var out []string
	for _, n := range names {
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return strings.Join(out, ", ")
}

func countOf(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

func (rr readyRoot) instructions() ReadinessLine {
	var found []string
	for _, name := range readyInstructionFiles {
		if rr.has(name) {
			found = append(found, name)
		}
	}
	return readyLine(ReadyInstructions, found, strings.Join(found, ", "), "")
}

func (rr readyRoot) tests() ReadinessLine {
	var found, short []string
	note := ""
	if data, ok := rr.text("package.json"); ok {
		var pkg struct{ Scripts map[string]any }
		if json.Unmarshal(data, &pkg) == nil {
			if s, ok := pkg.Scripts["test"].(string); ok && strings.TrimSpace(s) != "" {
				if strings.Contains(s, npmNoTest) {
					note = "package.json's test script is npm's placeholder"
				} else {
					found, short = append(found, "package.json test script"), append(short, "npm test")
				}
			}
		}
	}
	for _, name := range readyMakefiles {
		if data, ok := rr.text(name); ok && makeTestTarget.Match(data) {
			found, short = append(found, name+" test target"), append(short, "make test")
			break
		}
	}
	for _, f := range readyTestFiles {
		if rr.has(f[0]) {
			found, short = append(found, f[1]), append(short, f[2])
		}
	}
	line := readyLine(ReadyTests, found, shortList(short), "")
	if line.State == ReadyMissing && note != "" {
		line.Note = note
	}
	return line
}

func (rr readyRoot) lint() ReadinessLine {
	var found, short []string
	for _, e := range rr.entries {
		if !e.File {
			continue
		}
		if tool, ok := readyLintFiles[e.Name]; ok {
			found, short = append(found, CleanLine(e.Name, 100)), append(short, tool)
			continue
		}
		for _, p := range readyLintPrefixes {
			if strings.HasPrefix(e.Name, p[0]) {
				found, short = append(found, CleanLine(e.Name, 100)), append(short, p[1])
				break
			}
		}
	}
	if data, ok := rr.text("pyproject.toml"); ok && ruffTable.Match(data) {
		found, short = append(found, "[tool.ruff] in pyproject.toml"), append(short, "ruff")
	}
	slices.Sort(found)
	slices.Sort(short)
	return readyLine(ReadyLint, found, shortList(short), "")
}

func ciLine(workflows []WorkflowFile, problem string) ReadinessLine {
	if problem != "" {
		return readyLine(ReadyCI, nil, "", problem)
	}
	var found []string
	for _, w := range workflows {
		found = append(found, CleanLine(w.Path, 200))
	}
	return readyLine(ReadyCI, found, countOf(len(workflows), "workflow", "workflows"), "")
}

func checkLine(command string) ReadinessLine {
	if strings.TrimSpace(command) == "" {
		return readyLine(ReadyCheck, nil, "", "")
	}
	shown := CleanLine(maskSecrets(command), MaxProjectCommand)
	return readyLine(ReadyCheck, []string{shown}, shown, "")
}

func (f *ProjectFacts) readiness(r Repository, main string, workflows []WorkflowFile, workflowsProblem string) []ReadinessLine {
	check := ""
	if f.cfg.Check != nil {
		check = f.cfg.Check(r.Name)
	}
	unknown := ""
	var root readyRoot
	switch {
	case main == "":
		unknown = readyNotFetched
	case f.cfg.Files == nil:
		unknown = "This engine doesn't read main's files"
	default:
		entries, err := f.cfg.Files.ListTree(r.Path, main, "")
		if err != nil {
			unknown = "Couldn't list main's files"
			f.cfg.Logger.Warn("project facts", "event", "main-unlisted", "err", clip(maskSecrets(err.Error()), 400))
		}
		root = readyRoot{files: f.cfg.Files, path: r.Path, commit: main, entries: entries}
	}
	var out []ReadinessLine
	if unknown != "" {
		for _, kind := range []string{ReadyInstructions, ReadyTests, ReadyLint} {
			out = append(out, readyLine(kind, nil, "", unknown))
		}
	} else {
		out = append(out, root.instructions(), root.tests(), root.lint())
	}
	ci := ""
	switch {
	case main == "":
		ci = readyNotFetched
	case workflowsProblem != "":
		ci = "Couldn't read the workflows"
	}
	out = append(out, ciLine(workflows, ci), checkLine(check))
	if strings.TrimSpace(check) != "" {
		out = append(out, f.verifiedLine(r.Name, main))
	}
	order := []string{ReadyInstructions, ReadyTests, ReadyCI, ReadyLint, ReadyCheck, ReadyVerified}
	slices.SortStableFunc(out, func(a, b ReadinessLine) int { return slices.Index(order, a.Kind) - slices.Index(order, b.Kind) })
	return out
}

func (f *ProjectFacts) UseVerified(line func(project, main string) ReadinessLine) {
	f.mu.Lock()
	f.verified = line
	f.mu.Unlock()
}

func (f *ProjectFacts) verifiedLine(project, main string) ReadinessLine {
	f.mu.Lock()
	line := f.verified
	f.mu.Unlock()
	if line == nil {
		return ReadinessLine{Kind: ReadyVerified, State: ReadyUnknown, Commit: main, Value: "can't run", Note: "This engine runs no verification"}
	}
	return line(project, main)
}
