package session

import (
	"regexp"
	"strings"
	"unicode"
)

var pageBuiltins = map[string]bool{
	"clear": true, "compact": true, "context": true, "cost": true, "usage": true, "help": true, "status": true,
	"review": true, "security-review": true, "init": true, "todos": true, "export": true, "release-notes": true,
}

const commandRefused = "The page sends a skill, a plugin's or MCP server's command, and only the built-in commands that change nothing, such as /compact, /context or /clear. Type any other built-in command in Claude Code itself: docker compose exec -it agent claude attach <id>."

const maxCatalog = 400

var commandPattern = regexp.MustCompile(`^[a-z0-9][\w.:-]*$`)

var approvalWords = []string{"approval", "approve", "permission", "mode", "yolo", "auto", "sandbox", "trust", "allow", "bypass", "dangerous", "skip"}

func changesApprovals(name string) bool {
	for _, w := range approvalWords {
		if strings.Contains(name, w) {
			return true
		}
	}
	return false
}

type catalog struct {
	builtin map[string]bool
	custom  map[string]bool
}

func catalogOf(commands []any) catalog {
	c := catalog{builtin: map[string]bool{}, custom: map[string]bool{}}
	for i, raw := range commands {
		if i >= maxCatalog {
			break
		}
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name, ok := entry["name"].(string)
		if !ok || len(name) > 200 {
			continue
		}
		name = strings.ToLower(name)
		source, _ := entry["source"].(string)
		if source == "builtin" || (source == "agent" && changesApprovals(name)) {
			c.builtin[name] = true
		} else {
			c.custom[name] = true
		}
	}
	return c
}

func commandName(text string) string {
	if !strings.HasPrefix(text, "/") {
		return ""
	}
	rest := text[1:]
	if i := strings.IndexFunc(rest, unicode.IsSpace); i >= 0 {
		rest = rest[:i]
	}
	name := strings.ToLower(rest)
	if !commandPattern.MatchString(name) {
		return ""
	}
	return name
}

func refusedName(name string, known *catalog) bool {
	if pageBuiltins[name] {
		return false
	}
	if known != nil && known.builtin[name] {
		return true
	}
	if strings.Contains(name, ":") || strings.HasPrefix(name, "mcp__") || (known != nil && known.custom[name]) {
		return false
	}
	return true
}
