package gate

import (
	"path"
	"regexp"
	"slices"
	"strings"
)

const CloudReason = "this runs on the vendor's servers, outside Software Factory"

const (
	downloader = `(?:curl|wget|fetch|aria2c|irm|iwr|invoke-webrequest|invoke-restmethod)`
	runner     = `(?:sh|bash|zsh|dash|ksh|ash|fish|python[\d.]*|node|perl|ruby|php|iex|invoke-expression)`
	lead       = `(?:(?:sudo|doas|env|command|exec|nohup)(?:[` + space + `]+-\S+)*[` + space + `]+)*`
	wordEnd    = `(?:[` + space + `;&|()<>'"` + "`" + `,\]}\\]|$)`
	claudeWord = `(?:^|[` + space + `;&|()'"` + "`" + `={\[,/\\])claude(?:-code/|(?:-code)?(?:@[^` + space + `/;&|()<>'"` + "`" + `,\]}\\]*)?(?:\.[cm]?js)?` + wordEnd + `)`
)

var (
	claudeNames      = set("claude", "claude-code")
	claudeCloudFlags = set("--remote", "--cloud", "--teleport", "--remote-control", "--rc")
	claudeCloudWords = set("remote-control", "rc")
	claudeCloudSlash = regexp.MustCompile(`^[` + space + `]*/(?:remote-control|rc|teleport)(?:[` + space + `]|$)|^[` + space + `]*/ultrareview\b(?s:.)*--post\b`)
	infoOnly         = set("--version", "-v", "-V", "--help", "-h")
	claudeTools      = set("mcp", "config", "doctor", "update", "upgrade", "install", "auth", "plugin", "plugins", "migrate-installer", "help")
	launchers        = set("npx", "bunx", "pnpx")
	applets          = set("busybox", "toybox")
	jsRuntimes       = map[string]map[string]bool{"node": nil, "nodejs": nil, "bun": set("run", "x"), "deno": set("run")}
	scriptValueOpts  = set("--log-io", "--log-in", "--log-out", "--log-timing", "--echo", "--logging-format", "--output-limit")
	scriptJSExt      = regexp.MustCompile(`(?i)\.[cm]?js$`)
	packageRunners   = map[string]map[string]bool{"npm": set("exec", "x"), "pnpm": set("exec", "dlx"), "yarn": set("exec", "dlx"), "bun": set("x")}
	dbClients        = set("psql", "mysql", "mariadb", "sqlite3", "sqlite", "sqlcmd", "mongosh", "mongo", "clickhouse-client", "clickhouse", "cockroach", "duckdb", "pgcli", "mycli", "litecli", "usql", "cqlsh")
	dbDestroys       = regexp.MustCompile(`(?i)\b(?:drop|truncate)\b|\.drop(?:database)?\(`)
	infraTools       = set("terraform", "tofu", "terragrunt")
	kubeTools        = set("kubectl", "oc")
	downloaders      = set("curl", "wget", "fetch", "aria2c", "http", "https", "xh", "xhs", "irm", "iwr", "invoke-webrequest", "invoke-restmethod")
	stdinRunners     = set("iex", "invoke-expression")
	downloadPipe     = regexp.MustCompile(`(?i)\b` + downloader + `\b(?s:.)*?\|&?[` + space + `]*` + lead + runner + `\b`)
	downloadRuns     = regexp.MustCompile(`(?i)\b(?:` + runner + `|eval|source)\b[^\n;|&]*?(?:<\(|\$\(|` + "`" + `)[` + space + `]*` + lead + downloader + `\b|(?:^|[` + space + `;&|])\.[` + space + `]+<\([` + space + `]*` + downloader + `\b`)
	looseCloud       = regexp.MustCompile(`(?i)` + claudeWord + `(?:(?:(?s:.)*?[` + space + `'"])?(?:--(?:remote|cloud|teleport|remote-control|rc)(?:=|` + wordEnd + `)|/?(?:remote-control|rc)` + wordEnd + `)|(?s:.)*?\bultrareview\b(?s:.)*?--post\b)`)
	looseUndone      = regexp.MustCompile(`(?i)\brm\b[^\n;&|]*[` + space + `]-(?:-recursive|[a-z]*r)|\bgit\b(?s:.)*?\breset\b(?s:.)*?--hard|\bgit\b(?s:.)*?\bclean\b(?s:.)*?[` + space + `]-(?:-force|[a-z]*f)|\bfind\b(?s:.)*?[` + space + `]-delete\b|\b(?:terraform|tofu|terragrunt)\b(?s:.)*?\bdestroy\b|\b(?:kubectl|oc)\b(?s:.)*?\bdelete\b|\b(?:drop|truncate|dropdb)\b|` + claudeWord)
)

type hazardScan struct {
	cloud     bool
	undone    bool
	commands  int
	recursion int
}

func Hazards(tool string, input map[string]any) (cloud, irreversible bool) {
	command, ok := ShellCommand(tool, input)
	if !ok {
		return false, false
	}
	h := &hazardScan{}
	h.scan(command, 0)
	return h.cloud, h.undone
}

func (h *hazardScan) loose(text string) {
	h.cloud = h.cloud || looseCloud.MatchString(text)
	h.undone = h.undone || looseUndone.MatchString(text) || downloadPipe.MatchString(text)
}

func (h *hazardScan) scan(command string, depth int) {
	if downloadRuns.MatchString(command) {
		h.undone = true
	}
	p := &parsed{}
	if len(command) > 100000 || depth > shellDepth {
		h.loose(command)
		return
	}
	if _, ok := shellParse(command, 0, 0, p); !ok {
		h.loose(command)
		return
	}
	for _, words := range p.cmds {
		h.command(words, command, depth, p)
	}
}

func (h *hazardScan) command(all []string, whole string, depth int, p *parsed) {
	h.commands++
	if depth > shellDepth || h.commands > 2000 {
		h.loose(strings.Join(all, " "))
		return
	}
	start := 0
	for start < len(all) && shellWords[all[start]] {
		start++
	}
	words := all[start:]
	k := slices.IndexFunc(words, func(w string) bool { return !assignment.MatchString(w) })
	if k < 0 {
		return
	}
	word, args := words[k], words[k+1:]
	if unresolved.MatchString(word) {
		h.cloud = h.cloud || claudeCloud(args)
		h.undone = h.undone || rmWide(args)
		return
	}
	name := nameOf(word)
	if at := strings.IndexByte(name, '@'); at > 0 {
		name = name[:at]
	}
	if h.agent(name, args) {
		return
	}
	switch {
	case name == "rm":
		h.undone = h.undone || rmWide(args)
	case name == "dropdb":
		h.undone = true
	case name == "git":
		h.undone = h.undone || gitUndone(args)
	case dbClients[name]:
		h.undone = h.undone || dbDestroys.MatchString(whole)
	case infraTools[name]:
		h.undone = h.undone || slices.ContainsFunc(args, func(a string) bool { return a == "destroy" || a == "-destroy" || a == "--destroy" })
	case kubeTools[name]:
		h.undone = h.undone || slices.Contains(args, "delete")
	case shells[name]:
		h.shell(args, depth, p)
	case name == "eval":
		h.scan(strings.Join(args, " "), depth+1)
	case interpreters.MatchString(name):
		if agent, rest := agentScript(name, args); agent != "" {
			h.agent(agent, rest)
		} else if slices.ContainsFunc(args, inlineCode.MatchString) {
			h.cloud = h.cloud || looseCloud.MatchString(strings.Join(args, " "))
		} else if slices.ContainsFunc(args, func(a string) bool { return stdinPaths[a] }) || allOptions(args) {
			h.undone = h.undone || downloaded(p)
		}
	case stdinRunners[name]:
		h.undone = h.undone || downloaded(p)
	case name == "command" && slices.ContainsFunc(args, func(a string) bool { return a == "-v" || a == "-V" }):
	case name == "script":
		if command, ok := scriptCommand(args); ok {
			h.scan(command, depth+1)
		}
		h.wrapped(args, whole, depth, p)
	case name == "find" || fromInput[name]:
		h.undone = h.undone || fedRemove(args) || (name == "find" && slices.Contains(args, "-delete"))
		h.wrapped(args, whole, depth, p)
	case wrappers[name] || launchers[name] || applets[name]:
		h.wrapped(args, whole, depth, p)
	case packageRunners[name] != nil:
		if at := slices.IndexFunc(args, func(a string) bool { return !optionLike.MatchString(a) }); at >= 0 && packageRunners[name][args[at]] {
			h.wrapped(args[at+1:], whole, depth, p)
		}
	}
}

func (h *hazardScan) agent(name string, args []string) bool {
	switch {
	case claudeNames[name]:
		if claudeCloud(args) {
			h.cloud = true
		} else if childSession(args, claudeTools) {
			h.undone = true
		}
	default:
		return false
	}
	return true
}

func agentScript(runtime string, args []string) (string, []string) {
	verbs, ok := jsRuntimes[runtime]
	if !ok {
		return "", nil
	}
	for j, a := range args {
		if optionLike.MatchString(a) || verbs[a] {
			continue
		}
		if agent := agentPath(a); agent != "" {
			return agent, args[j+1:]
		}
		if j == 0 || !optionLike.MatchString(args[j-1]) {
			return "", nil
		}
	}
	return "", nil
}

func agentPath(script string) string {
	lower := strings.ToLower(script)
	base := scriptJSExt.ReplaceAllString(nameOf(script), "")
	if at := strings.IndexByte(base, '@'); at > 0 {
		base = base[:at]
	}
	switch {
	case strings.Contains(lower, "claude-code/") || claudeNames[base]:
		return "claude"
	}
	return ""
}

func scriptCommand(args []string) (string, bool) {
	for j := 0; j < len(args); j++ {
		a := args[j]
		if v, ok := strings.CutPrefix(a, "--command="); ok {
			return v, true
		}
		switch {
		case a == "--":
			return "", false
		case a == "--command":
			if j+1 < len(args) {
				return args[j+1], true
			}
			return "", false
		case strings.HasPrefix(a, "--"):
			if scriptValueOpts[a] {
				j++
			}
			continue
		case !strings.HasPrefix(a, "-") || a == "-":
			continue
		}
		for k := 1; k < len(a); k++ {
			if a[k] == 'c' {
				if v := strings.TrimPrefix(a[k+1:], "="); v != "" {
					return v, true
				}
				if j+1 < len(args) {
					return args[j+1], true
				}
				return "", false
			}
			if strings.IndexByte("TBIOEmo", a[k]) >= 0 {
				if k+1 == len(a) {
					j++
				}
				break
			}
		}
	}
	return "", false
}

func (h *hazardScan) wrapped(args []string, whole string, depth int, p *parsed) {
	for j, a := range args {
		if optionLike.MatchString(a) {
			continue
		}
		if h.recursion++; h.recursion > 64 {
			h.loose(strings.Join(args[j:], " "))
			return
		}
		h.command(args[j:], whole, depth+1, p)
	}
}

func (h *hazardScan) shell(args []string, depth int, p *parsed) {
	if j, ok := shellScriptAt(args); ok {
		if j < len(args) {
			h.scan(args[j], depth+1)
		}
		return
	}
	if !readsInput(args) {
		return
	}
	h.undone = h.undone || downloaded(p)
	if p.fedHazard {
		return
	}
	p.fedHazard = true
	for _, b := range p.bodies {
		h.scan(b, depth+1)
	}
	for _, w := range p.cmds {
		if len(w) > 1 && readers[nameOf(w[0])] {
			h.scan(strings.Join(w[1:], " "), depth+1)
			h.scan(printed(w[1:]), depth+1)
		}
	}
}

func downloaded(p *parsed) bool {
	return once(&p.downloads, func() bool {
		return slices.ContainsFunc(p.cmds, func(w []string) bool {
			return slices.ContainsFunc(w, func(a string) bool { return downloaders[nameOf(a)] })
		})
	})
}

func claudeCloud(args []string) bool {
	for _, a := range args {
		flag := a
		if eq := strings.IndexByte(a, '='); eq > 0 && strings.HasPrefix(a, "--") {
			flag = a[:eq]
		}
		if claudeCloudFlags[flag] || claudeCloudWords[a] || claudeCloudSlash.MatchString(a) {
			return true
		}
	}
	return slices.Contains(args, "ultrareview") && slices.ContainsFunc(args, func(a string) bool { return a == "--post" || strings.HasPrefix(a, "--post=") })
}

func childSession(args []string, tools map[string]bool) bool {
	if slices.ContainsFunc(args, func(a string) bool { return infoOnly[a] }) {
		return false
	}
	at := slices.IndexFunc(args, func(a string) bool { return !optionLike.MatchString(a) })
	if at < 0 || !tools[args[at]] {
		return true
	}
	return args[at] == "mcp" && slices.Contains(args[at+1:], "serve")
}

func fedRemove(args []string) bool {
	for j, a := range args {
		if nameOf(a) != "rm" {
			continue
		}
		if recursive, _ := rmFlags(args[j+1:]); recursive {
			return true
		}
	}
	return false
}

func rmWide(args []string) bool {
	recursive, targets := rmFlags(args)
	return recursive && slices.ContainsFunc(targets, wideTarget)
}

func rmFlags(args []string) (recursive bool, targets []string) {
	options := true
	for _, a := range args {
		switch {
		case options && a == "--":
			options = false
		case options && strings.HasPrefix(a, "-") && a != "-":
			if a == "--recursive" || (!strings.HasPrefix(a, "--") && strings.ContainsAny(a[1:], "rR")) {
				recursive = true
			}
		default:
			targets = append(targets, a)
		}
	}
	return recursive, targets
}

func wideTarget(t string) bool {
	if unresolved.MatchString(t) || strings.HasPrefix(t, "~") || strings.ContainsAny(t, "*?[") {
		return true
	}
	clean := path.Clean(t)
	return clean == "/" || clean == "." || slices.Contains(strings.Split(t, "/"), "..")
}

func gitUndone(args []string) bool {
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		if gitValueOptions[args[i]] {
			i += 2
		} else {
			i++
		}
	}
	if i >= len(args) {
		return false
	}
	rest := args[i+1:]
	switch strings.ToLower(args[i]) {
	case "reset":
		return slices.Contains(rest, "--hard")
	case "clean":
		return slices.ContainsFunc(rest, func(a string) bool {
			return a == "--force" || (strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "f"))
		})
	}
	return false
}
