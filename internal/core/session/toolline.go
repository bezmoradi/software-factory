package session

import (
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/gate"
)

const (
	maxSaid      = 120
	maxPrograms  = 8
	maxTail      = 5
	maxTailLine  = 300
	maxLinePath  = 1000
	maxLineCount = 10_000_000
	maxExit      = 999
)

const (
	outsideWords   = "Writes outside the worktree"
	helperWords    = "In a helper's copy"
	sandboxWords   = "Asks to run outside Claude Code's sandbox"
	refusalWords   = "Refused by Software Factory"
	doneWords      = "Done"
	failedWords    = "Failed"
	deniedWords    = "Denied"
	backgroundDone = "Runs in the background"
)

var (
	pathTools    = map[string]bool{"Read": true, "Edit": true, "MultiEdit": true, "Write": true, "NotebookEdit": true}
	searchTools  = map[string]bool{"Grep": true, "Glob": true}
	keptPrograms = map[string]string{"sudo": "Runs with sudo", "doas": "Runs with doas", "docker": "Runs docker", "podman": "Runs podman"}
	keptLoose    = regexp.MustCompile(`(?:^|[^A-Za-z0-9_.-])(sudo|doas|docker|podman)(?:$|[^A-Za-z0-9_.-])`)
	exitLine     = regexp.MustCompile(`^Exit code (\d{1,3})\s*$`)
)

func lineOf(kind string, ev Fields, cwd, area string) Fields {
	var line map[string]any
	switch kind {
	case "tool":
		line = callLine(ev, cwd, area)
	case "tool_done":
		line = endLine(ev)
	default:
		return nil
	}
	out := Fields{}
	if command, ok := ev["command"].(string); ok {
		out["command"] = maskSecrets(command)
	}
	if tool, _ := ev["tool"].(string); shellTools[tool] {
		if summary, ok := ev["summary"].(string); ok {
			out["summary"] = maskSecrets(summary)
		}
	}
	if len(line) > 0 {
		out["line"] = line
	}
	return out
}

func shellCall(tool string, ev Fields) bool {
	if shellTools[tool] || tool == "execute" {
		return true
	}
	command, _ := ev["command"].(string)
	return command != ""
}

func writesFile(tool string) bool {
	return editTools[tool] || agentEditKinds[tool]
}

func callLine(ev Fields, cwd, area string) map[string]any {
	tool, _ := ev["tool"].(string)
	line := map[string]any{}
	if p := toolPath(tool, ev); p != "" {
		short, inside := shortPath(p, cwd)
		line["path"] = short
		agentID, _ := ev["agentId"].(string)
		switch {
		case writesFile(tool) && !inside && HelperTop(area, p) != "":
			line["outside"] = true
			line["kept"] = helperWords
		case writesFile(tool) && !inside:
			line["outside"] = true
			line["kept"] = outsideWords
		case writesFile(tool) && agentID == "":
			line["edit"] = true
		}
	}
	if shellCall(tool, ev) {
		shellWords(line, tool, ev)
	}
	return line
}

func toolPath(tool string, ev Fields) string {
	p, _ := ev["path"].(string)
	if p == "" {
		summary, _ := ev["summary"].(string)
		if pathTools[tool] || searchTools[tool] && strings.HasPrefix(summary, "/") {
			p = summary
		}
	}
	if p == "" || len(p) > maxLinePath || strings.HasSuffix(p, "…") || controlChars.MatchString(p) || !utf8.ValidString(p) {
		return ""
	}
	return p
}

func shortPath(p, cwd string) (string, bool) {
	if strings.HasPrefix(p, "~") {
		return p, false
	}
	rel := p
	if path.IsAbs(p) {
		if cwd == "" || cwd == "/" {
			return p, false
		}
		if p == cwd {
			return ".", true
		}
		rest, ok := strings.CutPrefix(p, strings.TrimSuffix(cwd, "/")+"/")
		if !ok {
			return p, false
		}
		rel = rest
	}
	rel = path.Clean(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") || path.IsAbs(rel) {
		return p, false
	}
	return rel, true
}

func shellWords(line map[string]any, tool string, ev Fields) {
	command, _ := ev["command"].(string)
	if command == "" && shellTools[tool] {
		command, _ = ev["summary"].(string)
	}
	if command == "" {
		return
	}
	line["command"] = maskSecrets(command)
	var kept []string
	if unsandboxed, _ := ev["unsandboxed"].(bool); unsandboxed {
		kept = append(kept, sandboxWords)
	}
	var names []string
	parsed := false
	if !strings.HasSuffix(command, "…") {
		names, parsed = gate.Programs(command)
	}
	if parsed {
		for _, n := range names {
			if words := keptPrograms[strings.ToLower(n)]; words != "" {
				kept = append(kept, words)
			}
		}
	} else {
		for _, m := range keptLoose.FindAllStringSubmatch(command, -1) {
			if words := keptPrograms[strings.ToLower(m[1])]; words != "" && !slices.Contains(kept, words) {
				kept = append(kept, words)
			}
		}
	}
	if len(kept) > 0 {
		line["kept"] = strings.Join(kept, "; ")
	}
	description, _ := ev["description"].(string)
	said := spoken(description)
	if !parsed || len(names) == 0 || said == "" {
		return
	}
	line["said"] = said
	shown := names
	if len(shown) > maxPrograms {
		line["more"] = len(shown) - maxPrograms
		shown = shown[:maxPrograms]
	}
	line["programs"] = shown
}

func spoken(s string) string {
	return spokenWithin(s, maxSaid)
}

func spokenWithin(s string, n int) string {
	s = strings.Join(strings.Fields(controlChars.ReplaceAllString(s, " ")), " ")
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	s = maskSecrets(s)
	if utf8.RuneCountInString(s) > n {
		s = string([]rune(s)[:n-1]) + "…"
	}
	return s
}

func endLine(ev Fields) map[string]any {
	tool, _ := ev["tool"].(string)
	ok, _ := ev["ok"].(bool)
	line := map[string]any{}
	if refused, _ := ev["refused"].(bool); refused && !ok {
		line["result"] = refusalWords
		return line
	}
	if denied, _ := ev["denied"].(bool); denied && !ok {
		line["result"] = deniedWords
		return line
	}
	if shellCall(tool, ev) {
		background, _ := ev["background"].(bool)
		code, coded := exitOf(ev["exit"])
		switch {
		case ok && background:
			line["result"] = backgroundDone
		case ok:
			line["result"] = doneWords
		case coded:
			line["result"] = failedWords + " (exit " + strconv.Itoa(code) + ")"
		default:
			line["result"] = failedWords
		}
		if tail := tailOf(ev["tail"]); !ok && len(tail) > 0 {
			line["tail"] = tail
		}
	}
	if writesFile(tool) && ok {
		added, a := lineTotal(ev["added"])
		removed, r := lineTotal(ev["removed"])
		if a && r {
			line["added"] = added
			line["removed"] = removed
		}
	}
	return line
}

func exitOf(v any) (int, bool) {
	f, ok := v.(float64)
	if !ok || f < 0 || f > maxExit || f != float64(int(f)) {
		return 0, false
	}
	return int(f), true
}

func lineTotal(v any) (int, bool) {
	f, ok := v.(float64)
	if !ok || f < 0 || f > maxLineCount || f != float64(int(f)) {
		return 0, false
	}
	return int(f), true
}

func tailOf(v any) []string {
	list, _ := v.([]any)
	if len(list) > maxTail {
		list = list[len(list)-maxTail:]
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		s, ok := item.(string)
		if !ok {
			return nil
		}
		s = strings.ToValidUTF8(strings.ReplaceAll(controlChars.ReplaceAllString(s, " "), "\t", " "), "�")
		out = append(out, maskSecrets(clip(s, maxTailLine)))
	}
	return out
}

func failureTail(text string) (int, bool, []string) {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	code, coded := 0, false
	if len(lines) > 0 {
		if m := exitLine.FindStringSubmatch(lines[0]); m != nil {
			code, _ = strconv.Atoi(m[1])
			coded = true
			lines = lines[1:]
		}
	}
	kept := make([]string, 0, maxTail)
	for i := len(lines) - 1; i >= 0 && len(kept) < maxTail; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			kept = append(kept, lines[i])
		}
	}
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}
	return code, coded, kept
}

func editedLines(oldText, newText string) (int, int) {
	before, after := textLines(oldText), textLines(newText)
	for len(before) > 0 && len(after) > 0 && before[0] == after[0] {
		before, after = before[1:], after[1:]
	}
	for len(before) > 0 && len(after) > 0 && before[len(before)-1] == after[len(after)-1] {
		before, after = before[:len(before)-1], after[:len(after)-1]
	}
	return len(after), len(before)
}

func textLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}
