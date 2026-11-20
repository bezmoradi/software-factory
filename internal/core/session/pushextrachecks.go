package session

import (
	"path"
	"slices"
	"strings"
	"time"
)

type CheckKind string

const (
	CheckTest  CheckKind = "test"
	CheckLint  CheckKind = "lint"
	CheckTypes CheckKind = "type-check"
	CheckBuild CheckKind = "build"

	maxPendingChecks = 200
	maxCheckCommand  = 300
)

var checkKinds = []CheckKind{CheckTest, CheckLint, CheckTypes, CheckBuild}

type AgentCheck struct {
	Kind     CheckKind
	Command  string
	OK       bool
	Done     bool
	At       time.Time
	EditedAt time.Time
	Hidden   HiddenStatus
}

type pendingCheck struct {
	kind     CheckKind
	command  string
	hidden   HiddenStatus
	at       time.Time
	editedAt time.Time
}

var agentEditKinds = map[string]bool{"edit": true, "delete": true, "move": true}

func editsFiles(tool string) bool {
	return editTools[tool] || agentEditKinds[tool]
}

var (
	testPrograms  = map[string]bool{"pytest": true, "py.test": true, "jest": true, "vitest": true, "mocha": true, "jasmine": true, "karma": true, "ava": true, "rspec": true, "phpunit": true, "ctest": true, "nextest": true, "playwright": true, "cypress": true, "tox": true, "nox": true, "gotestsum": true}
	lintPrograms  = map[string]bool{"golangci-lint": true, "staticcheck": true, "eslint": true, "biome": true, "ruff": true, "flake8": true, "pylint": true, "shellcheck": true, "hadolint": true, "rubocop": true, "stylelint": true, "markdownlint": true, "markdownlint-cli2": true, "actionlint": true, "yamllint": true, "revive": true}
	typePrograms  = map[string]bool{"tsc": true, "mypy": true, "pyright": true, "vue-tsc": true, "flow": true}
	buildPrograms = map[string]bool{"webpack": true, "vite": true, "esbuild": true, "rollup": true, "parcel": true, "gcc": true, "g++": true, "clang": true, "javac": true, "rustc": true, "cmake": true, "ninja": true}
	systemTest    = map[string]bool{"/bin/test": true, "/usr/bin/test": true, "/usr/local/bin/test": true}
	scriptKinds   = map[string]CheckKind{"test": CheckTest, "tests": CheckTest, "lint": CheckLint, "typecheck": CheckTypes, "type-check": CheckTypes, "types": CheckTypes, "check-types": CheckTypes, "tsc": CheckTypes, "build": CheckBuild, "compile": CheckBuild, "vet": CheckLint}
)

func checkOf(command string) (CheckKind, bool) {
	segments, _ := splitCommand(command)
	for _, words := range segments {
		if k, ok := segmentCheck(words); ok {
			return k, true
		}
	}
	return "", false
}

func isCheckSegment(words []string) bool {
	_, ok := segmentCheck(words)
	return ok
}

func isTestSegment(words []string) bool {
	k, ok := segmentCheck(words)
	return ok && k == CheckTest
}

func runsTestCheck(command string) bool {
	segments, _ := splitCommand(command)
	return slices.ContainsFunc(segments, isTestSegment)
}

func testScript(program string) bool {
	return strings.Contains(program, "/") && !systemTest[program] && strings.Contains(strings.ToLower(path.Base(program)), "test")
}

func segmentCheck(words []string) (CheckKind, bool) {
	words = commandPrefixless(words)
	if len(words) == 0 {
		return "", false
	}
	program := path.Base(words[0])
	rest := words[1:]
	arg := firstArgument(rest)
	switch {
	case testScript(words[0]):
		return CheckTest, true
	case program == "npx" || program == "bunx" || program == "pnpx" || program == "uv" && arg == "run" || program == "poetry" && arg == "run":
		if program == "uv" || program == "poetry" {
			rest = rest[slices.Index(rest, arg)+1:]
		}
		return segmentCheck(rest)
	case (program == "python" || program == "python3") && len(rest) > 1 && rest[0] == "-m":
		return segmentCheck(rest[1:])
	case testPrograms[program]:
		return CheckTest, true
	case lintPrograms[program]:
		return CheckLint, true
	case typePrograms[program]:
		return CheckTypes, true
	case buildPrograms[program]:
		return CheckBuild, true
	case program == "go":
		return map[string]CheckKind{"test": CheckTest, "vet": CheckLint, "build": CheckBuild}[arg], arg == "test" || arg == "vet" || arg == "build"
	case program == "cargo":
		k, ok := map[string]CheckKind{"test": CheckTest, "nextest": CheckTest, "clippy": CheckLint, "check": CheckTypes, "build": CheckBuild}[arg]
		return k, ok
	case packageTools[program]:
		script := arg
		if script == "run" || script == "run-script" {
			if i := slices.Index(rest, arg); i+1 < len(rest) {
				script = firstArgument(rest[i+1:])
			}
		}
		k, ok := scriptKinds[script]
		if !ok && strings.Contains(script, ":") {
			k, ok = scriptKinds[script[:strings.IndexByte(script, ':')]]
		}
		return k, ok
	case program == "make" || program == "gmake" || program == "just" || program == "task":
		k, ok := scriptKinds[arg]
		if !ok && arg != "" {
			switch {
			case strings.Contains(arg, "test"):
				k, ok = CheckTest, true
			case strings.Contains(arg, "lint"):
				k, ok = CheckLint, true
			}
		}
		return k, ok
	}
	return "", false
}

func (b *worktreeBook) noteCheck(sid, kind string, ev map[string]any, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id, _ := ev["id"].(string)
	if id == "" || len(id) > maxToolID {
		return
	}
	switch kind {
	case "tool":
		tool, _ := ev["tool"].(string)
		summary, _ := ev["summary"].(string)
		if editsFiles(tool) {
			edits := b.edits[sid]
			if edits == nil || len(edits) >= maxPendingChecks {
				edits = map[string]bool{}
				b.edits[sid] = edits
			}
			edits[id] = true
			return
		}
		t := classifyTool(tool, summary, ev)
		if !t.shell || t.command == "" {
			return
		}
		k, ok := checkOf(t.command)
		if !ok {
			return
		}
		list := b.tools[sid]
		if list == nil || len(list) >= maxPendingChecks {
			list = map[string]pendingCheck{}
			b.tools[sid] = list
		}
		list[id] = pendingCheck{kind: k, command: clip(t.command, maxCheckCommand), hidden: checkHidden(t.command, t.background), at: now}
	case "tool_done":
		done, _ := ev["ok"].(bool)
		if b.edits[sid][id] {
			delete(b.edits[sid], id)
			if done {
				b.editedLocked(sid, now)
			}
			return
		}
		p, ok := b.tools[sid][id]
		background, _ := ev["background"].(bool)
		if command, _ := ev["command"].(string); command != "" {
			if k, found := checkOf(command); found {
				p, ok = pendingCheck{kind: k, command: clip(command, maxCheckCommand), hidden: checkHidden(command, p.hidden == HiddenByBackground), at: now, editedAt: p.editedAt}, true
			}
		}
		if !ok {
			return
		}
		if background {
			p.hidden = HiddenByBackground
		}
		if p.hidden == HiddenByBackground {
			done = false
		}
		delete(b.tools[sid], id)
		if b.checks[sid] == nil {
			b.checks[sid] = map[CheckKind]AgentCheck{}
		}
		b.checks[sid][p.kind] = AgentCheck{Kind: p.kind, Command: CleanLine(maskSecrets(p.command), maxCheckCommand), OK: done, Done: true, At: now, EditedAt: p.editedAt, Hidden: p.hidden}
	}
}

func checkHidden(command string, background bool) HiddenStatus {
	if background {
		return HiddenByBackground
	}
	return statusHiddenBy(command, isCheckSegment)
}

func (b *worktreeBook) editedLocked(sid string, now time.Time) {
	for id, p := range b.tools[sid] {
		if p.editedAt.IsZero() {
			p.editedAt = now
			b.tools[sid][id] = p
		}
	}
	for k, c := range b.checks[sid] {
		if c.EditedAt.IsZero() {
			c.EditedAt = now
			b.checks[sid][k] = c
		}
	}
}

func (b *worktreeBook) checksOf(sid string) []AgentCheck {
	var out []AgentCheck
	for _, k := range checkKinds {
		if c, ok := b.checks[sid][k]; ok {
			out = append(out, c)
		}
	}
	return out
}
