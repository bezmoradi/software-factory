package gate

import (
	"regexp"
	"slices"
	"strings"
)

const (
	space    = `\s\x0b\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`
	lineChar = `[^\n\r\x{2028}\x{2029}]`
	before   = "(?:^|[" + space + ";&|(`$/'\"\\\\[,{])"
	after    = "['\")`\\]]?[" + space + "\\\\,](?s:.)*?"
)

var (
	looseGitPush = regexp.MustCompile(before + "git" + after + `\b(?:push|send-pack)\b`)
	looseGhPush  = regexp.MustCompile(before + "gh" + after + `\b(?:pr|repo)\b(?s:.)*?\b(?:create|new|merge|sync)\b`)
	looseGhPost  = regexp.MustCompile(before + "gh" + after + `\b(?:(?:pr|issue)\b(?s:.)*?\b(?:comment|review|edit|close|reopen|ready|lock|unlock|create|new|delete|transfer|pin)|api\b(?s:.)*?(?:-X|--method|-f|-F|--field|--raw-field|--input))\b`)
	looseHidden  = regexp.MustCompile("(?:^|[" + space + ";&|({!])[\"']?[$`](?s:.)*?\\b(?:push|send-pack)\\b")
	mcpForge     = regexp.MustCompile(`(?i)^mcp__.*(github|gitlab).*__`)
	mcpRead      = regexp.MustCompile(`(?i)__(get|list|search|download)_\w+$|_(read|get|list|search)$`)
	mcpPush      = regexp.MustCompile(`(?i)^mcp__.*(github|gitlab).*__(push_files|create_or_update_file|delete_file|create_branch|create_pull_request|merge_pull_request|update_pull_request_branch|create_merge_request|merge_merge_request|accept_merge_request)$`)
	pushWord     = regexp.MustCompile(`(?i)\b(?:push|send-pack)\b`)
	pushValue    = regexp.MustCompile(`(?i)^(push|send-pack)$`)
	unresolved   = regexp.MustCompile(`[\x00$]`)
	assignment   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	awkRuns      = regexp.MustCompile(`system[` + space + `]*\(|\|[` + space + `]*getline|\|&|\|[` + space + `]*"|print[^;]*[^|]\|[^|]`)
	runsProgram  = map[string]*regexp.Regexp{
		"printf": regexp.MustCompile(`^-v`),
		"rg":     regexp.MustCompile(`^--pre(=|$)`),
		"sed":    regexp.MustCompile(`(^|[;{}\n][` + space + `]*)\d*e([` + space + `]|;|}|$)|/[gpIiMm\d]*e[gpIiMmw\d]*([` + space + `]|;|}|$)`),
		"awk":    awkRuns, "gawk": awkRuns, "mawk": awkRuns, "nawk": awkRuns,
	}
	interpreters  = regexp.MustCompile(`^(python[\d.]*|pypy[\d.]*|node|nodejs|deno|bun|ruby|perl|php|lua|tclsh|osascript)$`)
	inlineCode    = regexp.MustCompile(`^(-[A-Za-z]*[cemr]|--eval|--print|--command)$`)
	shellCommandC = regexp.MustCompile(`^-[A-Za-z]*c[A-Za-z]*$`)
	shellStdin    = regexp.MustCompile(`^-[A-Za-z]*s[A-Za-z]*$`)
	optionLike    = regexp.MustCompile(`^[-+]`)
	gitFlags      = regexp.MustCompile(`^(-p|-P|--paginate|--no-pager|--bare|--no-replace-objects|--literal-pathspecs|--glob-pathspecs|--noglob-pathspecs|--icase-pathspecs|--no-optional-locks|--no-lazy-fetch|--no-advice|--exec-path(=` + lineChar + `*)?|--(git-dir|work-tree|namespace|super-prefix|attr-source)=` + lineChar + `*)$`)
	pagerFiles    = regexp.MustCompile(`^(-O|--open-files-in-pager)`)
	ghAPIWrite    = regexp.MustCompile(`(?i)(^|/)(pulls/[^/]+/merge|merges|merge-upstream|git/refs|git/tags|contents/|branches/[^/]+/rename|releases|dispatches|actions/(secrets|variables)|actions/workflows/[^/]+/(enable|disable)|actions/runs/[^/]+/(rerun|approve))`)
	ghAPIRepo     = regexp.MustCompile(`(?i)^/?repos/[^/]+/[^/]+/?$`)
	ghAPIGraphQL  = regexp.MustCompile(`\b(mergePullRequest|enablePullRequestAutoMerge|mergeBranch|createCommitOnBranch|createRef|updateRef|updateRefs|deleteRef)\b`)
	ghAPIShort    = regexp.MustCompile(`^-[A-Za-z]` + lineChar)
	graphWrite    = regexp.MustCompile(`(?i)\b(mutation|subscription)\b`)
	ghReads       = regexp.MustCompile(`(?i)^(view|list|ls|diff|checks|status)$`)
	repoWrites    = regexp.MustCompile(`^(sync|edit|rename|delete|archive|unarchive)$`)
	ghWrites      = map[string]*regexp.Regexp{
		"release":  regexp.MustCompile(`^(create|new|upload|edit|delete|delete-asset)$`),
		"workflow": regexp.MustCompile(`^(run|enable|disable)$`),
		"run":      regexp.MustCompile(`^(rerun|cancel|delete)$`),
		"secret":   regexp.MustCompile(`^(set|delete|remove)$`),
		"variable": regexp.MustCompile(`^(set|delete|remove)$`),
	}
	forgeAPI    = regexp.MustCompile(`(?i)^(?:https?://)?(api\.github\.com|uploads\.github\.com|api\.bitbucket\.org|gitlab\.com/api|codeberg\.org/api)(/|$)`)
	httpSends   = regexp.MustCompile(`^(-d|--data(-\w+)?|--json|-F|--form(-string)?|-T|--upload-file)$`)
	httpSendsAt = regexp.MustCompile(`^--(data|json|form|upload-file|post-data|post-file|body-data|body-file)(=|-)`)
	shortData   = regexp.MustCompile(`^-d` + lineChar)
	shortMethod = regexp.MustCompile(`^-X` + lineChar)
	shortConfig = regexp.MustCompile(`^-K` + lineChar)
	longMethod  = regexp.MustCompile(`^--(request|method)=`)
	urlScheme   = regexp.MustCompile(`(?i)^[a-z][a-z0-9+.-]*://`)
	httpieField = regexp.MustCompile(`^[^=:@]+(:=|==|=|@|:=@|=@)`)
	httpieQuery = regexp.MustCompile(`^[^=:@]+==`)
)

var (
	readers         = set("cat", "head", "tail", "wc", "ls", "echo", "printf", "cd", "pwd", "grep", "egrep", "fgrep", "rg", "cut", "tr", "nl", "diff", "cmp", "comm", "basename", "dirname", "true", "false", "sed", "awk", "gawk", "mawk", "nawk", "jq", "sort", "uniq", "column", "paste", "fold", "od", "xxd", "strings", "less", "more")
	stdinPaths      = set("-", "/dev/stdin", "/dev/fd/0", "/proc/self/fd/0")
	gitSafe         = set("add", "commit", "status", "log", "show", "diff", "grep", "stash", "branch", "checkout", "switch", "restore", "reset", "tag", "remote", "config", "rev-parse", "ls-files", "mv", "rm", "blame", "describe", "reflog", "init", "notes", "shortlog", "cat-file", "ls-tree", "merge-base", "rev-list", "show-ref", "symbolic-ref", "for-each-ref", "merge", "cherry-pick", "revert", "help", "version")
	gitValueOptions = set("-C", "-c", "--git-dir", "--work-tree", "--namespace", "--super-prefix", "--config-env", "--attr-source")
	shells          = set("sh", "bash", "zsh", "dash", "ksh", "fish")
	shellValueOpts  = set("-o", "+o", "-O", "+O", "--rcfile", "--init-file", "--init-command", "-C")
	shellWords      = set("{", "}", "!", "if", "then", "elif", "else", "fi", "do", "done", "while", "until", "esac", "coproc")
	wrappers        = set("exec", "command", "builtin", "env", "sudo", "doas", "su", "nohup", "nice", "ionice", "time", "timeout", "xargs", "watch", "parallel", "find", "ssh", "script", "stdbuf", "unbuffer", "caffeinate", "chronic", "flock", "setsid", "strace", "arch", "taskset")
	fromInput       = set("xargs", "parallel")
	ghAPIValueOpts  = set("-X", "--method", "-f", "--raw-field", "-F", "--field", "-H", "--header", "--input", "-q", "--jq", "-t", "--template", "--cache", "-p", "--preview", "--hostname")
	ghFieldOpts     = set("-f", "--raw-field", "-F", "--field")
)

func set(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, s := range items {
		m[s] = true
	}
	return m
}

func ShellCommand(tool string, input map[string]any) (string, bool) {
	if Kind(tool, input) != ShellTool {
		return "", false
	}
	c, ok := input["command"].(string)
	return c, ok
}

func PushShaped(tool string, input map[string]any, review bool) bool {
	if mcpPush.MatchString(tool) || (review && mcpForge.MatchString(tool) && !mcpRead.MatchString(tool)) {
		return true
	}
	command, ok := ShellCommand(tool, input)
	return ok && scan{review: review}.pushes(command, 0)
}

type scan struct{ review bool }

func (s scan) looseGh(t string) bool {
	return looseGhPush.MatchString(t) || (s.review && looseGhPost.MatchString(t))
}

func (s scan) loose(t string) bool {
	return looseGitPush.MatchString(t) || s.looseGh(t)
}

func (s scan) pushes(command string, depth int) bool {
	p := &parsed{}
	if len(command) > 100000 || depth > shellDepth {
		return s.loose(command) || looseHidden.MatchString(command)
	}
	if _, ok := shellParse(command, 0, 0, p); !ok {
		return s.loose(command) || looseHidden.MatchString(command)
	}
	for _, words := range p.cmds {
		if s.pushingCommand(words, depth, p) {
			return true
		}
	}
	return false
}

func once(cache **bool, f func() bool) bool {
	if *cache == nil {
		v := f()
		*cache = &v
	}
	return **cache
}

func isRunner(a string) bool {
	n := nameOf(a)
	return n == "git" || n == "gh" || n == "eval" || shells[n]
}

func (s scan) pushingCommand(all []string, depth int, p *parsed) bool {
	start := 0
	for start < len(all) && shellWords[all[start]] {
		start++
	}
	words := all[start:]
	k := slices.IndexFunc(words, func(w string) bool { return !assignment.MatchString(w) })
	assigned := words
	if k >= 0 {
		assigned = words[:k]
	}
	for _, w := range assigned {
		v := w[strings.IndexByte(w, '=')+1:]
		if pushValue.MatchString(strings.TrimSpace(v)) || s.loose(v) {
			return true
		}
	}
	if k < 0 {
		return false
	}
	word := words[k]
	args := words[k+1:]
	text := strings.Join(args, " ")
	loose := s.loose(text)
	if unresolved.MatchString(word) {
		return loose || pushWord.MatchString(text)
	}
	cmd := nameOf(word)
	switch {
	case cmd == "git":
		return gitPushes(args)
	case cmd == "gh":
		return s.ghPushes(args)
	case cmd == "curl" || cmd == "wget":
		return httpWrites(args)
	case cmd == "http" || cmd == "https" || cmd == "xh" || cmd == "xhs":
		return httpieWrites(args)
	case readers[cmd]:
		re, runs := runsProgram[cmd]
		return runs && slices.ContainsFunc(args, re.MatchString) && loose
	case shells[cmd]:
		return s.shellPushes(args, text, loose, depth, p)
	case interpreters.MatchString(cmd):
		if slices.ContainsFunc(args, inlineCode.MatchString) || !(slices.ContainsFunc(args, func(a string) bool { return stdinPaths[a] }) || allOptions(args)) {
			return loose
		}
		return once(&p.code, func() bool {
			if slices.ContainsFunc(p.bodies, s.loose) {
				return true
			}
			return slices.ContainsFunc(p.cmds, func(w []string) bool { return s.loose(strings.Join(w[1:], " ")) || s.loose(printed(w[1:])) })
		})
	case cmd == "eval":
		return s.pushes(text, depth+1)
	case wrappers[cmd]:
		if loose || pushWord.MatchString(text) {
			return true
		}
		if fromInput[cmd] && slices.ContainsFunc(args, func(a string) bool { return unresolved.MatchString(a) || isRunner(a) }) && once(&p.said, func() bool {
			return slices.ContainsFunc(p.cmds, func(w []string) bool {
				return len(w) > 0 && readers[nameOf(w[0])] && slices.ContainsFunc(w[1:], pushWord.MatchString)
			})
		}) {
			return true
		}
		var runs []int
		for j, a := range args {
			if isRunner(a) {
				runs = append(runs, j)
			}
		}
		if len(runs) > 16 || (len(runs) > 0 && depth >= shellDepth) {
			return true
		}
		return slices.ContainsFunc(runs, func(j int) bool { return s.pushingCommand(args[j:], depth+1, p) })
	}
	return loose
}

func allOptions(args []string) bool {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			return false
		}
	}
	return true
}

func (s scan) shellPushes(args []string, text string, loose bool, depth int, p *parsed) bool {
	if at := slices.IndexFunc(args, shellCommandC.MatchString); at >= 0 {
		j := at + 1
		for j < len(args) && optionLike.MatchString(args[j]) {
			if args[j] == "--" {
				j++
				break
			}
			if shellValueOpts[args[j]] {
				j += 2
			} else {
				j++
			}
		}
		if j < len(args) {
			return s.pushes(args[j], depth+1)
		}
		return loose
	}
	if !readsInput(args) {
		return loose || pushWord.MatchString(text)
	}
	return once(&p.fed, func() bool {
		if depth >= shellDepth {
			return true
		}
		if slices.ContainsFunc(p.bodies, func(b string) bool { return pushWord.MatchString(b) || s.pushes(b, depth+1) }) {
			return true
		}
		return slices.ContainsFunc(p.cmds, func(w []string) bool {
			rest := w[1:]
			return slices.ContainsFunc(rest, pushWord.MatchString) || pushWord.MatchString(printed(rest)) || s.pushes(strings.Join(rest, " "), depth+1) || s.pushes(printed(rest), depth+1)
		})
	})
}

func readsInput(args []string) bool {
	for j := 0; j < len(args); j++ {
		a := args[j]
		if shellStdin.MatchString(a) || stdinPaths[a] {
			return true
		}
		if a == "--" {
			return j+1 >= len(args) || stdinPaths[args[j+1]]
		}
		if shellValueOpts[a] {
			j++
		} else if !optionLike.MatchString(a) {
			return false
		}
	}
	return true
}

func gitPushes(args []string) bool {
	i := 0
	unknown := false
	var config []string
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		a := args[i]
		if gitValueOptions[a] {
			if a == "-c" || a == "--config-env" {
				v := ""
				if i+1 < len(args) {
					v = args[i+1]
				}
				config = append(config, v)
			}
			i += 2
			continue
		}
		if v, ok := strings.CutPrefix(a, "--config-env="); ok {
			config = append(config, v)
		} else if !gitFlags.MatchString(a) {
			unknown = true
		}
		i++
	}
	sub := ""
	var rest []string
	if i < len(args) {
		sub = strings.ToLower(args[i])
		rest = args[i+1:]
	}
	if slices.ContainsFunc(config, func(v string) bool { return pushWord.MatchString(v) || unresolved.MatchString(v) }) || unresolved.MatchString(sub) || sub == "push" || sub == "send-pack" {
		return true
	}
	if unknown {
		return slices.ContainsFunc(args, pushWord.MatchString)
	}
	if sub == "" || (gitSafe[sub] && !slices.ContainsFunc(rest, pagerFiles.MatchString)) {
		return false
	}
	return slices.ContainsFunc(rest, pushWord.MatchString)
}

func (s scan) ghPushes(args []string) bool {
	if len(args) == 0 || args[0] == "" {
		return false
	}
	sub := strings.ToLower(args[0])
	text := "gh " + strings.Join(args, " ")
	if strings.HasPrefix(sub, "-") || unresolved.MatchString(sub) {
		return unresolved.MatchString(sub) || s.looseGh(text)
	}
	if sub == "api" {
		return s.ghAPIWrites(args[1:])
	}
	if s.review && (sub == "pr" || sub == "issue") && ghPosts(args[1:]) {
		return true
	}
	writes, known := ghWrites[sub]
	if sub != "pr" && sub != "repo" && !known {
		return false
	}
	verb, option := "", false
	for j := 1; j < len(args); j++ {
		a := args[j]
		if a == "-R" || a == "--repo" {
			j++
			continue
		}
		if strings.HasPrefix(a, "--repo=") {
			continue
		}
		option = strings.HasPrefix(a, "-")
		verb = strings.ToLower(a)
		break
	}
	switch {
	case option:
		return s.looseGh(text)
	case unresolved.MatchString(verb):
		return true
	case sub == "pr":
		return verb == "create" || verb == "new" || verb == "merge"
	case known:
		return writes.MatchString(verb)
	}
	return repoWrites.MatchString(verb) || ((verb == "create" || verb == "new") && slices.ContainsFunc(args, func(a string) bool { return a == "--push" || strings.HasPrefix(a, "--push=") }))
}

func ghPosts(args []string) bool {
	for j := 0; j < len(args); j++ {
		a := args[j]
		if a == "-R" || a == "--repo" {
			j++
			continue
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		return unresolved.MatchString(a) || !ghReads.MatchString(a)
	}
	return false
}

func (s scan) ghAPIWrites(args []string) bool {
	method := ""
	body := false
	query, queried := "", false
	endpoint, named := "", false
	for j := 0; j < len(args); j++ {
		a := args[j]
		eq := -1
		if strings.HasPrefix(a, "--") {
			eq = strings.IndexByte(a, '=')
		}
		name, joined, hasJoined := a, "", false
		switch {
		case eq > 0:
			name, joined, hasJoined = a[:eq], a[eq+1:], true
		case ghAPIShort.MatchString(a):
			name, joined, hasJoined = a[:2], a[2:], true
		}
		if ghAPIValueOpts[name] {
			value := joined
			if !hasJoined {
				j++
				if j < len(args) {
					value = args[j]
				}
			}
			if name == "-X" || name == "--method" {
				method = strings.ToUpper(value)
			}
			if ghFieldOpts[name] || name == "--input" {
				body = true
			}
			switch {
			case name == "--input" || ((name == "-F" || name == "--field") && strings.HasPrefix(value, "query=@")):
				query, queried = "", true
			case ghFieldOpts[name] && strings.HasPrefix(value, "query="):
				if queried {
					query = ""
				} else {
					query = value[6:]
				}
				queried = true
			}
			continue
		}
		if !strings.HasPrefix(a, "-") && !named {
			endpoint, named = a, true
		}
	}
	if unresolved.MatchString(method) {
		return true
	}
	effective := method
	if effective == "" {
		effective = "GET"
		if body {
			effective = "POST"
		}
	}
	if effective == "GET" {
		return false
	}
	graphql := named && strings.EqualFold(endpoint, "graphql")
	if s.review {
		readOnly := graphql && (method == "" || method == "POST") && query != "" && !unresolved.MatchString(query) && !graphWrite.MatchString(query)
		return !readOnly
	}
	if !named || unresolved.MatchString(endpoint) {
		return true
	}
	if graphql {
		return slices.ContainsFunc(args, func(a string) bool {
			return ghAPIGraphQL.MatchString(a) || unresolved.MatchString(a) || a == "--input" || strings.HasPrefix(a, "--input=")
		})
	}
	return ghAPIWrite.MatchString(endpoint) || ghAPIRepo.MatchString(endpoint)
}

func httpieWrites(args []string) bool {
	var words []string
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			words = append(words, a)
		}
	}
	at := slices.IndexFunc(words, func(a string) bool { return forgeAPI.MatchString(a) || unresolved.MatchString(a) })
	if at < 0 {
		return false
	}
	method := ""
	if at > 0 {
		method = strings.ToUpper(words[at-1])
	}
	if unresolved.MatchString(method) {
		return true
	}
	if method != "" {
		return method != "GET" && method != "HEAD" && method != "OPTIONS"
	}
	return slices.ContainsFunc(words[at+1:], func(w string) bool { return httpieField.MatchString(w) && !httpieQuery.MatchString(w) })
}

func httpWrites(args []string) bool {
	config := slices.ContainsFunc(args, func(a string) bool {
		return a == "-K" || a == "--config" || shortConfig.MatchString(a) || strings.HasPrefix(a, "--config=")
	})
	if config && !slices.ContainsFunc(args, urlScheme.MatchString) {
		return true
	}
	if !slices.ContainsFunc(args, func(a string) bool { return forgeAPI.MatchString(a) || unresolved.MatchString(a) }) {
		return false
	}
	if config {
		return true
	}
	method := ""
	for j := 0; j < len(args); j++ {
		a := args[j]
		switch {
		case a == "-X" || a == "--request" || a == "--method":
			j++
			method = ""
			if j < len(args) {
				method = strings.ToUpper(args[j])
			}
		case shortMethod.MatchString(a):
			method = strings.ToUpper(a[2:])
		case longMethod.MatchString(a):
			method = strings.ToUpper(a[strings.IndexByte(a, '=')+1:])
		case httpSends.MatchString(a) || httpSendsAt.MatchString(a) || shortData.MatchString(a):
			if method == "" {
				method = "POST"
			}
		}
	}
	return method != "" && method != "GET" && method != "HEAD"
}
