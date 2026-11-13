package session

import (
	"bytes"
	"path"
	"slices"
	"strings"
	"unicode/utf8"
)

type InstructionList struct {
	Names   []string
	Folders []string
	Above   []string
	Source  string
}

var InstructionLists = map[string]InstructionList{
	"claude": {Names: []string{"CLAUDE.md", "CLAUDE.local.md", "AGENTS.md"}, Folders: []string{".claude/rules"},
		Above:  []string{"CLAUDE.md", "CLAUDE.local.md", ".claude/CLAUDE.md", "AGENTS.md", ".claude/AGENTS.md"},
		Source: "code.claude.com/docs/en/memory: CLAUDE.md and CLAUDE.local.md load from the starting folder and every folder above it, and, once Claude works there, from subfolders; AGENTS.md and .claude/AGENTS.md in the starting folder and above when no CLAUDE.md applies; .md files in .claude/rules/, recursively, nested .claude/rules/ on demand (Claude Code 2.1.322)"},
	"codex": {Names: []string{"AGENTS.override.md", "AGENTS.md"},
		Source: "codex source at tag rust-v0.167.1, core/src/agents_md.rs: AGENTS.override.md, then AGENTS.md, in each folder from the project root (the nearest folder holding .git) down to the working folder, never above the root; learn.chatgpt.com/docs/agent-configuration/agents-md"},
}

var WalkSkipped = []string{".git", "node_modules", "dist", "build", "out", "target", "vendor", "coverage", "__pycache__", "venv"}

const (
	MaxWalkDepth        = 12
	MaxWalkFolders      = 5000
	MaxWalkEntries      = 5000
	MaxInstructionFiles = 100
	MaxInstructionBytes = 2 << 20
	maxMarks            = 40
	maxMarkText         = 200
)

type StartupScan struct {
	Entries      []StartupEntry
	Instructions InstructionList
	Walk         bool
	Above        []string
}

const AboveWorktree StartupPlace = "above"

func foldersAbove(worktree, workspace string) []string {
	if worktree == "" || workspace == "" || !path.IsAbs(worktree) || !path.IsAbs(workspace) {
		return nil
	}
	worktree, workspace = path.Clean(worktree), path.Clean(workspace)
	if !strings.HasPrefix(worktree, workspace+"/") {
		return nil
	}
	var out []string
	for dir := path.Dir(worktree); ; dir = path.Dir(dir) {
		out = append(out, dir)
		if dir == workspace || dir == "/" {
			return out
		}
	}
}

func (l InstructionList) Matches(rel string) bool {
	if slices.Contains(l.Names, path.Base(rel)) {
		return true
	}
	if !strings.HasSuffix(rel, ".md") {
		return false
	}
	for _, f := range l.Folders {
		if strings.HasPrefix(rel, f+"/") || strings.Contains(rel, "/"+f+"/") {
			return true
		}
	}
	return false
}

func WalkSkips(name string) bool { return slices.Contains(WalkSkipped, name) }

func NestedAt(entries []StartupEntry, rel string) (StartupEntry, bool) {
	for _, e := range entries {
		if e.Nested && e.Place == InWorktree && strings.HasSuffix(rel, "/"+e.Path) {
			e.Path, e.Nested = rel, false
			return e, true
		}
	}
	return StartupEntry{}, false
}

func startupHash(list string, read StartupRead) string {
	if len(read.Walk) == 0 {
		return SetHash(list, read.Files)
	}
	return SetHash(list+"\x00the walk stopped\x00"+strings.Join(read.Walk, "\x00"), read.Files)
}

type Mark struct {
	Line int
	What string
	Text string
}

const (
	markBash   = "allowed-tools names Bash: the skill or command runs these commands without asking during its turn"
	markHooks  = "hooks in front matter: commands Claude Code runs while the skill, command or agent is in use"
	markShell  = "inline shell: Claude Code runs this command when the skill or command loads, before the model reads it"
	markScript = "a script bundled with the skill, which the skill can run"
)

var scriptExt = []string{".sh", ".bash", ".zsh", ".py", ".js", ".mjs", ".cjs", ".ts", ".rb", ".pl", ".php", ".ps1", ".lua"}

func skillPart(rel string) (inSkill bool, name string) {
	parts := strings.Split(rel, "/")
	for i := 0; i+2 < len(parts); i++ {
		if parts[i+1] == "skills" && (parts[i] == ".claude" || parts[i] == ".agents" || parts[i] == ".codex") {
			return true, strings.Join(parts[i+3:], "/")
		}
	}
	return false, ""
}

func runsShell(rel string) bool {
	parts := strings.Split(rel, "/")
	for i := 0; i+1 < len(parts)-1; i++ {
		if parts[i] == ".claude" && (parts[i+1] == "skills" || parts[i+1] == "commands") {
			return true
		}
	}
	return false
}

func markText(line []byte) string {
	cut := min(len(line), maxMarkText)
	for cut > 0 && cut < len(line) && !utf8.RuneStart(line[cut]) {
		cut--
	}
	return Escape(bytes.TrimRight(line[:cut], "\r"))
}

func Marks(rel string, data []byte, link bool) []Mark {
	if link && data == nil {
		return nil
	}
	var out []Mark
	add := func(n int, what string, line []byte) {
		if len(out) < maxMarks {
			out = append(out, Mark{Line: n, What: what, Text: markText(line)})
		}
	}
	inSkill, inner := skillPart(rel)
	if inSkill && inner != "" && inner != "SKILL.md" && (bytes.HasPrefix(data, []byte("#!")) || slices.Contains(scriptExt, strings.ToLower(path.Ext(rel)))) {
		first, _, _ := bytes.Cut(data, []byte("\n"))
		add(0, markScript, first)
	}
	if !strings.HasSuffix(strings.ToLower(rel), ".md") {
		return out
	}
	lines := bytes.Split(data, []byte("\n"))
	front := len(lines) > 0 && string(bytes.TrimRight(lines[0], " \t\r")) == "---"
	shell := runsShell(rel)
	key := ""
	for i, line := range lines {
		n := i + 1
		trimmed := bytes.TrimRight(line, " \t\r")
		if front && i > 0 && (string(trimmed) == "---" || string(trimmed) == "...") {
			front = false
			continue
		}
		if front && i > 0 {
			if len(line) > 0 && line[0] != ' ' && line[0] != '\t' && line[0] != '-' && line[0] != '#' {
				k, _, ok := bytes.Cut(line, []byte(":"))
				key = ""
				if ok {
					key = strings.TrimSpace(string(k))
				}
				if key == "hooks" {
					add(n, markHooks, line)
				}
			}
			if key == "allowed-tools" && bytes.Contains(line, []byte("Bash")) {
				add(n, markBash, line)
			}
			continue
		}
		if shell && inlineShell(line) {
			add(n, markShell, line)
		}
	}
	return out
}

func inlineShell(line []byte) bool {
	t := bytes.TrimLeft(line, " \t")
	if bytes.HasPrefix(t, []byte("```!")) || bytes.HasPrefix(t, []byte("~~~!")) {
		return true
	}
	for i := 0; i+1 < len(line); i++ {
		if line[i] == '!' && line[i+1] == '`' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t') {
			return true
		}
	}
	return false
}

func Hidden(data []byte) bool {
	for len(data) > 0 {
		r, n := utf8.DecodeRune(data)
		if (r == utf8.RuneError && n <= 1) || escaped(r) {
			return true
		}
		data = data[n:]
	}
	return false
}
