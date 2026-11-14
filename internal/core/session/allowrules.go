package session

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

type RulePlace string

const (
	RuleInClone    RulePlace = "clone"
	RuleInWorktree RulePlace = "worktree"
	RuleInUser     RulePlace = "user"
)

const (
	MaxRuleFiles         = 64
	MaxRulesPerFile      = 500
	MaxRuleBytes         = 4096
	MaxRuleFileBytes     = 1 << 20
	MaxRuleWorktrees     = 200
	maxRulePath          = 400
	maxRuleProblem       = 300
	AuditRemoveAllowRule = "remove-allow-rule"
	allowRulesFor        = 30 * time.Second
)

var RuleWorktreeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type SettingsFile struct {
	Box      string
	Place    RulePlace
	Worktree string
	Path     string
	Rules    []string
	Problem  string
}

type AllowRule struct {
	Box      string
	Rule     string
	Place    RulePlace
	Worktree string
	Path     string
	Wide     []string
}

type AllowRulesView struct {
	Repo  string
	Idle  bool
	Files []SettingsFile
	Rules []AllowRule
}

type AllowRulesHost interface {
	ReadAllowRules(ctx context.Context, repo string) ([]SettingsFile, error)
	RemoveAllowRule(ctx context.Context, repo string, place RulePlace, worktree, rule string) error
}

type AllowRulesPage interface {
	AllowRules(ctx context.Context, repo string) (AllowRulesView, error)
	RemoveAllowRule(ctx context.Context, repo, box string, place RulePlace, worktree, rule string) error
}

type RuleHost struct {
	Box  string
	Name string
	Host AllowRulesHost
}

type AllowRules struct {
	Hosts func(ctx context.Context, repo string) []RuleHost
	Repos Repositories
	Audit Audit
	Now   func() time.Time
}

var _ AllowRulesPage = (*AllowRules)(nil)

var errRuleShape = errors.New("the file isn't the settings shape Claude Code writes")

func ruleShape(why string) error {
	return &ruleShapeError{why: why}
}

type ruleShapeError struct{ why string }

func (e *ruleShapeError) Error() string { return e.why }

func RuleProblem(err error) string {
	if e, ok := errors.AsType[*ruleShapeError](err); ok {
		return e.why
	}
	return errRuleShape.Error()
}

type allowScan struct {
	rules      []string
	spans      [][2]int
	openEnd    int
	closeStart int
	found      bool
}

func skipGap(data []byte, at int) int {
	for at < len(data) {
		switch data[at] {
		case ' ', '\t', '\n', '\r', ',', ':':
			at++
		default:
			return at
		}
	}
	return at
}

func skipValue(dec *json.Decoder) error {
	var raw json.RawMessage
	return dec.Decode(&raw)
}

func expectDelim(dec *json.Decoder, want json.Delim) (bool, error) {
	tok, err := dec.Token()
	if err != nil {
		return false, ruleShape("the file isn't valid JSON")
	}
	d, ok := tok.(json.Delim)
	return ok && d == want, nil
}

func scanAllow(data []byte) (allowScan, error) {
	var out allowScan
	if len(data) > MaxRuleFileBytes {
		return out, ruleShape("the file is over 1 MiB")
	}
	if !utf8.Valid(data) {
		return out, ruleShape("the file isn't UTF-8 text")
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return out, nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if ok, err := expectDelim(dec, '{'); err != nil || !ok {
		return out, ruleShape("the file isn't a JSON object")
	}
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return out, ruleShape("the file isn't valid JSON")
		}
		key, _ := tok.(string)
		if seen[key] {
			return out, ruleShape("the file names " + key + " twice")
		}
		seen[key] = true
		if key != "permissions" {
			if skipValue(dec) != nil {
				return out, ruleShape("the file isn't valid JSON")
			}
			continue
		}
		if ok, err := expectDelim(dec, '{'); err != nil || !ok {
			return out, ruleShape("permissions isn't an object")
		}
		if err := scanPermissions(dec, data, &out); err != nil {
			return out, err
		}
	}
	if _, err := dec.Token(); err != nil {
		return out, ruleShape("the file isn't valid JSON")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return out, ruleShape("the file holds more than one JSON value")
	}
	return out, nil
}

func scanPermissions(dec *json.Decoder, data []byte, out *allowScan) error {
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return ruleShape("the file isn't valid JSON")
		}
		key, _ := tok.(string)
		if seen[key] {
			return ruleShape("permissions names " + key + " twice")
		}
		seen[key] = true
		if key != "allow" {
			if skipValue(dec) != nil {
				return ruleShape("the file isn't valid JSON")
			}
			continue
		}
		if ok, err := expectDelim(dec, '['); err != nil || !ok {
			return ruleShape("permissions.allow isn't a list")
		}
		out.found = true
		out.openEnd = int(dec.InputOffset())
		for dec.More() {
			before := int(dec.InputOffset())
			tok, err := dec.Token()
			if err != nil {
				return ruleShape("the file isn't valid JSON")
			}
			rule, ok := tok.(string)
			if !ok {
				return ruleShape("an entry in permissions.allow isn't a string")
			}
			start, end := skipGap(data, before), int(dec.InputOffset())
			if start >= end || data[start] != '"' {
				return ruleShape("permissions.allow couldn't be read")
			}
			if len(out.rules) >= MaxRulesPerFile {
				return ruleShape("permissions.allow has more than 500 rules")
			}
			out.rules = append(out.rules, rule)
			out.spans = append(out.spans, [2]int{start, end})
		}
		if _, err := dec.Token(); err != nil {
			return ruleShape("the file isn't valid JSON")
		}
		out.closeStart = int(dec.InputOffset()) - 1
		if out.closeStart < out.openEnd || data[out.closeStart] != ']' {
			return ruleShape("permissions.allow couldn't be read")
		}
	}
	if _, err := dec.Token(); err != nil {
		return ruleShape("the file isn't valid JSON")
	}
	return nil
}

func AllowRulesIn(data []byte) ([]string, error) {
	s, err := scanAllow(data)
	if err != nil {
		return nil, err
	}
	return s.rules, nil
}

func RemoveAllowRule(data []byte, rule string) ([]byte, error) {
	first, err := scanAllow(data)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(first.rules, rule) {
		return nil, ruleShape("the rule is no longer in the file")
	}
	next := slices.Clone(data)
	for range MaxRulesPerFile {
		s, err := scanAllow(next)
		if err != nil {
			return nil, err
		}
		i := slices.Index(s.rules, rule)
		if i < 0 {
			break
		}
		var from, to int
		switch {
		case len(s.rules) == 1:
			from, to = s.openEnd, s.closeStart
		case i < len(s.rules)-1:
			from, to = s.spans[i][0], s.spans[i+1][0]
		default:
			from, to = s.spans[i-1][1], s.spans[i][1]
		}
		next = append(next[:from:from], next[to:]...)
	}
	if err := sameExceptRule(data, next, rule); err != nil {
		return nil, err
	}
	return next, nil
}

func sameExceptRule(before, after []byte, rule string) error {
	var a, b any
	if json.Unmarshal(before, &a) != nil || json.Unmarshal(after, &b) != nil {
		return ruleShape("the file couldn't be written back as it was")
	}
	top, ok := a.(map[string]any)
	if !ok {
		return ruleShape("the file isn't a JSON object")
	}
	perms, ok := top["permissions"].(map[string]any)
	if !ok {
		return ruleShape("permissions isn't an object")
	}
	list, ok := perms["allow"].([]any)
	if !ok {
		return ruleShape("permissions.allow isn't a list")
	}
	kept := []any{}
	for _, v := range list {
		if v != rule {
			kept = append(kept, v)
		}
	}
	perms["allow"] = kept
	if !reflect.DeepEqual(a, b) {
		return ruleShape("the file couldn't be written back as it was")
	}
	return nil
}

var (
	networkTools     = []string{"curl", "wget", "nc", "ncat", "netcat", "socat", "telnet", "ssh", "scp", "sftp", "rsync", "ftp", "tftp", "aria2c", "http", "https", "xh"}
	interpreters     = []string{"python", "python2", "python3", "node", "nodejs", "deno", "bun", "ruby", "perl", "php", "lua", "Rscript", "sh", "bash", "zsh", "dash", "ksh", "fish", "pwsh", "powershell", "osascript", "awk", "gawk", "npx", "bunx", "pnpx", "uvx", "pipx", "env", "xargs", "sudo", "su", "eval", "exec", "nohup", "timeout", "nice", "command", "busybox", "docker"}
	bashTools        = []string{"Bash", "PowerShell"}
	ruleCommandSplit = regexp.MustCompile(`&&|\|\||[;|&\n]|\$\(|` + "`")
	versioned        = regexp.MustCompile(`^(python|ruby|perl|php|node|lua)[0-9.]+$`)
)

func ruleParts(rule string) (string, string, bool) {
	tool, inner, ok := strings.Cut(rule, "(")
	if !ok {
		return strings.TrimSpace(rule), "", false
	}
	return strings.TrimSpace(tool), strings.TrimSuffix(inner, ")"), true
}

func ruleCommandName(word string) string {
	word = strings.Trim(word, `"'`)
	name := path.Base(word)
	if versioned.MatchString(name) {
		name = versioned.FindStringSubmatch(name)[1]
	}
	return name
}

func WideReasons(rule string) []string {
	tool, spec, has := ruleParts(rule)
	var out []string
	add := func(s string) {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	if !has || strings.TrimSpace(spec) == "" {
		if slices.Contains(bashTools, tool) {
			return []string{"any " + tool + " command"}
		}
		if server, rest, ok := strings.Cut(strings.TrimPrefix(tool, "mcp__"), "__"); strings.HasPrefix(tool, "mcp__") {
			if !ok || rest == "" || strings.Contains(rest, "*") {
				return []string{"every tool of the MCP server " + server}
			}
			return nil
		}
		if strings.Contains(tool, "*") {
			return []string{"a wildcard, so it allows more than one tool"}
		}
		return []string{"any use of " + tool}
	}
	wild := strings.ContainsAny(spec, "*?")
	if !slices.Contains(bashTools, tool) {
		if wild {
			add("a wildcard, so it allows more than one call")
		}
		return out
	}
	bare := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(spec), ":*"))
	if bare == "" || bare == "*" || bare == "**" {
		return []string{"any " + tool + " command"}
	}
	for part := range strings.SplitSeq(ruleCommandSplit.ReplaceAllString(spec, "\x00"), "\x00") {
		words := strings.Fields(part)
		first := true
		for _, w := range words {
			if first && strings.Contains(w, "=") && !strings.HasPrefix(w, "-") {
				continue
			}
			word := strings.TrimSuffix(w, ":*")
			name, said := ruleCommandName(word), path.Base(strings.Trim(word, `"'`))
			if slices.Contains(networkTools, name) {
				add("network tool " + said)
			}
			if first && wild && slices.Contains(interpreters, name) {
				add(said + " runs whatever code or command it's given")
			}
			first = false
		}
	}
	if wild {
		add("a wildcard, so it allows more than one command")
	}
	return out
}

func cleanRuleText(s string, limit int) string {
	s = strings.ToValidUTF8(s, "")
	if len(s) > limit {
		s = strings.ToValidUTF8(s[:limit], "")
	}
	return s
}

func ValidRuleText(rule string) bool {
	return rule != "" && len(rule) <= MaxRuleBytes && utf8.ValidString(rule) && !strings.ContainsRune(rule, 0)
}

func (a *AllowRules) repo(name string) (Repository, error) {
	if a.Hosts == nil || a.Repos == nil {
		return Repository{}, explained(conflict("no agent-host"), "This engine has no agent-host, so it can't read the agents' saved rules.")
	}
	repos, err := a.Repos.List()
	if err != nil {
		return Repository{}, explained(conflict("workspace"), "The engine couldn't read the workspace's repositories.")
	}
	i := slices.IndexFunc(repos, func(r Repository) bool { return r.Name == name })
	if i < 0 {
		return Repository{}, explained(notFound("project"), "That project is no longer in the list.")
	}
	return repos[i], nil
}

func checkRuleFiles(files []SettingsFile) ([]SettingsFile, error) {
	if len(files) > MaxRuleFiles {
		return nil, explained(conflict("agent-host"), "agent-host answered more settings files than the engine reads.")
	}
	out := make([]SettingsFile, 0, len(files))
	seen := map[string]bool{}
	for _, f := range files {
		switch f.Place {
		case RuleInClone, RuleInUser:
			if f.Worktree != "" {
				return nil, explained(conflict("agent-host"), "agent-host answered a settings file the engine doesn't read.")
			}
		case RuleInWorktree:
			if !RuleWorktreeName.MatchString(f.Worktree) {
				return nil, explained(conflict("agent-host"), "agent-host answered a worktree name the engine refuses.")
			}
		default:
			return nil, explained(conflict("agent-host"), "agent-host answered a settings file the engine doesn't read.")
		}
		key := string(f.Place) + "\x00" + f.Worktree
		if seen[key] || len(f.Rules) > MaxRulesPerFile || len(f.Path) > maxRulePath || !utf8.ValidString(f.Path) {
			return nil, explained(conflict("agent-host"), "agent-host answered a settings file the engine refuses.")
		}
		seen[key] = true
		rules := make([]string, 0, len(f.Rules))
		for _, r := range f.Rules {
			if !ValidRuleText(r) {
				return nil, explained(conflict("agent-host"), "agent-host answered a rule the engine refuses.")
			}
			if !slices.Contains(rules, r) {
				rules = append(rules, r)
			}
		}
		out = append(out, SettingsFile{Box: f.Box, Place: f.Place, Worktree: f.Worktree, Path: f.Path, Rules: rules, Problem: cleanRuleText(f.Problem, maxRuleProblem)})
	}
	return out, nil
}

func (a *AllowRules) AllowRules(ctx context.Context, name string) (AllowRulesView, error) {
	repo, err := a.repo(name)
	if err != nil {
		return AllowRulesView{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, allowRulesFor)
	defer cancel()
	hosts := a.Hosts(ctx, repo.Name)
	v := AllowRulesView{Repo: repo.Name, Idle: len(hosts) == 0, Files: []SettingsFile{}, Rules: []AllowRule{}}
	for _, h := range hosts {
		files, err := h.Host.ReadAllowRules(ctx, repo.Name)
		if err != nil {
			return AllowRulesView{}, explained(conflict("agent-host"), "agent-host in "+h.Name+"'s container couldn't read the saved rules: "+hostMessage(err))
		}
		files, err = checkRuleFiles(files)
		if err != nil {
			return AllowRulesView{}, err
		}
		for _, f := range files {
			f.Box = h.Box
			v.Files = append(v.Files, f)
			for _, r := range f.Rules {
				v.Rules = append(v.Rules, AllowRule{Box: h.Box, Rule: r, Place: f.Place, Worktree: f.Worktree, Path: f.Path, Wide: WideReasons(r)})
			}
		}
	}
	return v, nil
}

func (a *AllowRules) RemoveAllowRule(ctx context.Context, name, box string, place RulePlace, worktree, rule string) error {
	repo, err := a.repo(name)
	if err != nil {
		return err
	}
	hosts := a.Hosts(ctx, repo.Name)
	i := slices.IndexFunc(hosts, func(h RuleHost) bool { return h.Box == box })
	if !ValidBox(box) || i < 0 {
		return explained(notFound("box"), "No running session of this project has that container.")
	}
	switch {
	case !ValidRuleText(rule):
		return explained(invalid("rule"), "That isn't a rule the engine reads.")
	case (place == RuleInClone || place == RuleInUser) && worktree == "":
	case place == RuleInWorktree && RuleWorktreeName.MatchString(worktree):
	default:
		return explained(invalid("place"), "That isn't a settings file the engine lists.")
	}
	ctx, cancel := context.WithTimeout(ctx, allowRulesFor)
	defer cancel()
	err = hosts[i].Host.RemoveAllowRule(ctx, repo.Name, place, worktree, rule)
	result := "removed"
	if err != nil {
		result = "refused"
	}
	if a.Audit != nil {
		now := time.Now
		if a.Now != nil {
			now = a.Now
		}
		sum := sha256.Sum256([]byte(rule))
		where := box + ":" + string(place)
		if worktree != "" {
			where += ":" + worktree
		}
		_ = a.Audit.Append(AuditEntry{Time: now().UTC(), Repository: repo.Name, By: "page", Kind: AuditRemoveAllowRule, Result: result, Path: where, Hash: hex.EncodeToString(sum[:]), Missing: []string{}})
	}
	if err != nil {
		return explained(conflict("not removed"), "The rule wasn't removed: "+hostMessage(err))
	}
	return nil
}

func (d *Dispatcher) RuleHosts(ctx context.Context, repo string) []RuleHost {
	d.mu.Lock()
	type owner struct{ box, name string }
	var owners []owner
	for _, r := range d.runs {
		if r.repo == repo && ValidBox(r.box) && !slices.ContainsFunc(owners, func(o owner) bool { return o.box == r.box }) {
			owners = append(owners, owner{box: r.box, name: runName(r)})
		}
	}
	d.mu.Unlock()
	slices.SortFunc(owners, func(a, b owner) int { return strings.Compare(a.box, b.box) })
	var out []RuleHost
	for _, o := range owners {
		if h, ok := d.hostFor(o.box).(AllowRulesHost); ok && d.liveBox(o.box) && d.ConfirmBox(ctx, o.box) == nil {
			out = append(out, RuleHost{Box: o.box, Name: o.name, Host: h})
		}
	}
	return out
}
