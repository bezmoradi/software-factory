package npm

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

const (
	maxShellScan = 64 << 20
	jsSpace      = "\\t\\n\\v\\f\\r \\x{00a0}\\x{1680}\\x{2000}-\\x{200a}\\x{2028}\\x{2029}\\x{202f}\\x{205f}\\x{3000}\\x{feff}"
	skillName    = `knackpack:[a-z0-9][a-z0-9-]{0,63}`
)

var (
	promptDirs    = []string{"skills", "commands", "agents"}
	shellFence    = []byte("```!")
	shellBang     = []byte("!$")
	argumentRef   = regexp.MustCompile(`(?i)\$(?:arguments|[0-9])`)
	fenceDollar   = []byte("```$")
	devModsDir    = []byte("${CLAUDE_DEV_MODS_DIR}")
	innermost     = regexp.MustCompile(`\$\{[^${}\n]*\}`)
	yamlTag       = regexp.MustCompile(`(?m)(?:^|[?:\-\[{,])[ \t]*!`)
	unsafeName    = regexp.MustCompile("[!`$" + jsSpace + "]")
	matterOpen    = regexp.MustCompile("^---[" + jsSpace + "]*$")
	keyEscape     = regexp.MustCompile(`\\[xuU]`)
	skillsFlow    = regexp.MustCompile(`^skills:[ \t]*\[[ \t]*(?:` + skillName + `[ \t]*(?:,[ \t]*` + skillName + `[ \t]*)*)?\][ \t]*$`)
	skillsScalar  = regexp.MustCompile(`^skills:[ \t]*` + skillName + `[ \t]*$`)
	skillsBlock   = regexp.MustCompile(`^skills:[ \t]*$`)
	skillsItem    = regexp.MustCompile(`^[ \t]+-[ \t]+` + skillName + `[ \t]*$`)
	topLevelKey   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*[ \t]*:`)
	promptReason  = "the engine doesn't install a knackpack using argument placeholders or inline shell in its skills or commands, which Claude Code runs as it loads them, in the trusted reviewer container too when an agent preloads one"
	preloadReason = "which this engine can't read as preloading only knackpack's own skills (knackpack:<name>); the engine doesn't install a knackpack whose agents may preload other skills or commands"
)

func bangBeforeBacktick(data []byte) bool {
	for i := 0; i+1 < len(data); i++ {
		if data[i] == '!' && data[i+1] == '`' && (i == 0 || data[i-1] != '`') {
			return true
		}
	}
	return false
}

func promptProblem(data []byte) string {
	if m := argumentRef.Find(data); m != nil {
		return "the argument placeholder " + shown(string(m))
	}
	if matter, ok := frontMatterOf(data); ok && (keyEscape.MatchString(matter) || yamlTag.MatchString(matter) || strings.Contains(squeezed(matter), "arguments")) {
		return "front matter that may declare arguments"
	}
	switch {
	case bytes.Contains(data, devModsDir):
		return "the placeholder ${CLAUDE_DEV_MODS_DIR}"
	case bytes.Contains(data, shellBang):
		return "!$"
	case bangBeforeBacktick(data):
		return "! before a backtick"
	case bytes.Contains(data, fenceDollar):
		return "a ``` fence followed by $"
	case bytes.Contains(data, shellFence) || bytes.Contains(withoutPlaceholders(data), shellFence):
		return "a ```! fence"
	}
	return ""
}

func withoutPlaceholders(data []byte) []byte {
	for {
		next := innermost.ReplaceAll(data, nil)
		if bytes.Equal(next, data) {
			return bytes.ReplaceAll(next, []byte("$"), nil)
		}
		data = next
	}
}

func frontMatterOf(data []byte) (string, bool) {
	text := strings.TrimPrefix(strings.ReplaceAll(string(data), "\r\n", "\n"), "\ufeff")
	first, rest, _ := strings.Cut(text, "\n")
	if !matterOpen.MatchString(first) {
		return "", false
	}
	if before, _, ok := strings.Cut(rest, "---"); ok {
		return before, true
	}
	return rest, true
}

func preloadsOthers(data []byte) bool {
	matter, ok := frontMatterOf(data)
	if !ok {
		return false
	}
	if keyEscape.MatchString(matter) || yamlTag.MatchString(matter) {
		return true
	}
	lines := strings.Split(matter, "\n")
	accepted := 0
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		bare := strings.TrimLeft(line, " \t")
		switch {
		case strings.HasPrefix(bare, "?"):
			return true
		case bare != "" && bare == line && !strings.HasPrefix(bare, "#") && !strings.HasPrefix(bare, "- ") && !topLevelKey.MatchString(line):
			return true
		}
		if !strings.Contains(strings.ToLower(line), "skills") {
			continue
		}
		accepted++
		switch {
		case skillsFlow.MatchString(line), skillsScalar.MatchString(line):
		case skillsBlock.MatchString(line):
			for i+1 < len(lines) && skillsItem.MatchString(lines[i+1]) {
				i++
			}
			if i+1 < len(lines) && strings.TrimSpace(lines[i+1]) != "" && !topLevelKey.MatchString(lines[i+1]) {
				return true
			}
			if i+1 < len(lines) && strings.TrimSpace(lines[i+1]) == "" && i+2 < len(lines) {
				return true
			}
		default:
			return true
		}
	}
	return strings.Count(squeezed(matter), "skills") > accepted
}

func squeezed(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || r == '\\' || r == '"' || r == '\'' || r == '\ufeff' {
			return -1
		}
		return unicode.ToLower(r)
	}, s)
}

func shellRefused(dir string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return errors.New("the plugin's tree couldn't be opened to check its skills, commands and agents for inline shell")
	}
	defer func() { _ = root.Close() }()
	budget := int64(maxShellScan)
	check := func(rel string, agent bool) error {
		if unsafeName.MatchString(rel) {
			return fmt.Errorf("the commit's %s has !, a backtick, $ or white space in its path, which Claude Code puts into a skill's text as ${CLAUDE_SKILL_DIR}, where it could complete inline shell; %s", shown(rel), "the engine doesn't install a knackpack with such names in its skills, commands or agents")
		}
		info, err := root.Lstat(rel)
		switch {
		case err != nil:
			return fmt.Errorf("%s couldn't be read to check it for inline shell", rel)
		case info.Mode()&fs.ModeSymlink != 0:
			return fmt.Errorf("the commit's %s is a symbolic link, which Claude Code would follow to load it as a skill, command or agent, so the engine can't check it for inline shell", rel)
		case !info.Mode().IsRegular():
			return nil
		case info.Size() > budget:
			return errors.New("the commit's skills, commands and agents are too large to check for inline shell")
		}
		budget -= info.Size()
		data, err := root.ReadFile(rel)
		if err != nil {
			return fmt.Errorf("%s couldn't be read to check it for inline shell", rel)
		}
		if !agent {
			if what := promptProblem(data); what != "" {
				return fmt.Errorf("the commit's %s holds %s; %s", rel, what, promptReason)
			}
			return nil
		}
		if preloadsOthers(data) {
			return fmt.Errorf("the commit's agent %s has front matter %s", rel, preloadReason)
		}
		return nil
	}
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return errors.New("the plugin's top folder couldn't be read to check its skills, commands and agents for inline shell")
	}
	for _, e := range entries {
		lower := strings.ToLower(e.Name())
		switch {
		case lower == "skill.md":
			if err := check(e.Name(), false); err != nil {
				return err
			}
		case slices.Contains(promptDirs, lower):
			agent := lower == "agents"
			if e.Type()&fs.ModeSymlink != 0 || !e.IsDir() {
				if err := check(e.Name(), agent); err != nil {
					return err
				}
				continue
			}
			walkErr := fs.WalkDir(root.FS(), e.Name(), func(rel string, d fs.DirEntry, err error) error {
				if err != nil {
					return fmt.Errorf("%s couldn't be read to check it for inline shell", rel)
				}
				if d.IsDir() {
					return nil
				}
				return check(rel, agent)
			})
			if walkErr != nil {
				return walkErr
			}
		}
	}
	return nil
}
