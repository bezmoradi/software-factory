package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	checkKeyVariable = "SF_DEV_CHECK_KEY"
	defaultSocket    = "/run/sf/plugin.sock"
	answersFolder    = "gate-answers"
	maxInput         = 1024 * 1024
	askFor           = 5 * time.Second
	downFor          = 10 * time.Second
	maxPathBytes     = 4096
	maxPathSteps     = 256
	maxLinkHops      = 40
	cloudReason      = "this runs on the vendor's servers, outside Software Factory"
	jsTrimmed        = "\t\n\v\f\r \u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000\ufeff"
	pathTooLong      = "Software Factory refused this tool call: the file's path is too long for Software Factory to check. Use a shorter path."
	pathUnplaced     = "Software Factory refused this tool call: it couldn't work out where the file is, so it couldn't check the write. Try again, or use the file's full path."
	linkUntold       = "Software Factory refused this tool call: the file's path goes through symbolic links that loop, chain more than 40 times or can't be read, so Software Factory can't tell where the write lands. Write to the file's real path."
)

var (
	answerID    = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)
	contentKeys = map[string]bool{"content": true, "new_string": true, "old_string": true, "edits": true, "new_source": true}
	remoteTools = map[string]bool{
		"RemoteTrigger": true, "PublishPlugin": true, "SendToProject": true, "SendToProjectContext": true, "SendFile": true, "SendUserFile": true, "SendFeedback": true, "DesignSync": true, "SendMessage": true,
		"Artifact": true, "ArtifactCheck": true, "ArtifactComments": true, "ArtifactData": true, "AppifactRepl": true, "ClaudeDesign": true, "ShareOnboardingGuide": true, "PushNotification": true,
		"memory_write": true, "request_computer": true,
	}
	shellTools = map[string]bool{"Bash": true, "Monitor": true, "PowerShell": true}
	editTools  = map[string]bool{"Write": true, "Edit": true, "MultiEdit": true, "NotebookEdit": true}
	forgeMCP   = regexp.MustCompile(`(?i)^mcp__.*(github|gitlab)`)
	pushTool   = regexp.MustCompile(`^mcp__[^_][\w.-]*__factory_push$`)
	stepSplit  = regexp.MustCompile(`[\n;&|]`)
	downUntil  time.Time
	topOf      struct{ cwd, top string }
)

const (
	lead       = `(?:(?:sudo|doas|env|command|exec|nohup)(?:\s+-\S+)*\s+)*`
	downloader = `(?:curl|wget|fetch|aria2c|irm|iwr|invoke-webrequest|invoke-restmethod)`
	runner     = `(?:sh|bash|zsh|dash|ksh|ash|fish|python[\d.]*|node|perl|ruby|php|iex|invoke-expression)`
	wordEnd    = "(?:[\\s;&|()<>'\"`,\\]}\\\\]|$)"
	claudeWord = "(?i)(?:^|[\\s;&|()'\"`={\\[,/\\\\])claude(?:-code/|(?:-code)?(?:@[^\\s/;&|()<>'\"`,\\]}\\\\]*)?(?:\\.[cm]?js)?" + wordEnd + ")"
)

func steps(patterns ...string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, len(patterns))
	for i, p := range patterns {
		out[i] = regexp.MustCompile(p)
	}
	return out
}

var (
	loosePush  = [][]*regexp.Regexp{steps(`\bgit\b`, `\b(push|send-pack)\b`), steps("(^|[\\s;&|(`$])(gh|glab)\\s")}
	looseCloud = [][]*regexp.Regexp{
		steps(claudeWord, `(?i)(?:^|[\s'"])(?:--(?:remote|cloud|teleport|remote-control|rc)(?:=|`+wordEnd+`)|/?(?:remote-control|rc)`+wordEnd+`)`),
		steps(claudeWord, `(?i)\bultrareview\b`, `(?i)--post\b`),
	}
	looseUndoneAll = [][]*regexp.Regexp{
		steps(`(?i)\bgit\b`, `(?i)\breset\b`, `(?i)--hard`),
		steps(`(?i)\bgit\b`, `(?i)\bclean\b`, `(?i)\s-(?:-force|[a-z]*f)`),
		steps(`(?i)\b(?:terraform|tofu|terragrunt)\b`, `(?i)\bdestroy\b`),
		steps(`(?i)\b(?:kubectl|oc)\b`, `(?i)\bdelete\b`),
		steps(`(?i)\b(?:drop|truncate|dropdb)\b`),
		steps(claudeWord),
		steps(`(?i)\b`+downloader+`\b`, `(?i)\|&?\s*`+lead+runner+`\b`),
		steps(`(?i)(?:^|[\s;&|])\.\s+<\(\s*` + downloader + `\b`),
	}
	looseUndoneStep = [][]*regexp.Regexp{
		steps(`(?i)\brm\b`, `(?i)\s-(?:-recursive|[a-z]*r)`),
		steps(`(?i)\bfind\b`, `(?i)\s-delete\b`),
		steps(`(?i)\b(?:`+runner+`|eval|source)\b`, "(?i)(?:<\\(|\\$\\(|`)\\s*"+lead+downloader+`\b`),
	}
)

type checked struct {
	kind         string
	waits        bool
	pushes       bool
	protected    string
	held         string
	cloud        string
	irreversible bool
	guarded      string
	engine       bool
}

type place struct {
	top   string
	paths []string
}

type asked struct {
	status int
	body   []byte
	source string
	found  *checked
}

func inOrder(text string, list []*regexp.Regexp) bool {
	left := text
	for _, step := range list {
		at := step.FindStringIndex(left)
		if at == nil {
			return false
		}
		left = left[at[1]:]
	}
	return true
}

func anyInOrder(text string, lists [][]*regexp.Regexp) bool {
	for _, l := range lists {
		if inOrder(text, l) {
			return true
		}
	}
	return false
}

func looseUndone(command string) bool {
	if anyInOrder(command, looseUndoneAll) {
		return true
	}
	for _, step := range stepSplit.Split(command, -1) {
		if anyInOrder(step, looseUndoneStep) {
			return true
		}
	}
	return false
}

func fallback(tool string, input map[string]any) checked {
	command := ""
	if c, ok := input["command"].(string); ok && shellTools[tool] {
		command = c
	}
	remote := remoteTools[tool] || ((tool == "Agent" || tool == "Task") && input["isolation"] == "remote") || (tool == "Projects" && input["method"] == "project_write")
	mcp := strings.HasPrefix(tool, "mcp__") && !pushTool.MatchString(tool)
	cloud := ""
	if anyInOrder(command, looseCloud) {
		cloud = cloudReason
	}
	return checked{kind: "unknown", waits: remote || mcp, pushes: anyInOrder(command, loosePush) || forgeMCP.MatchString(tool), cloud: cloud, irreversible: command != "" && looseUndone(command)}
}

func encoded(v any) []byte {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(v)
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}

func sized(input map[string]any) map[string]any {
	if len(encoded(input)) <= maxInput {
		return input
	}
	slim := map[string]any{}
	for k, v := range input {
		if !contentKeys[k] {
			slim[k] = v
		}
	}
	if len(encoded(slim)) <= maxInput {
		return slim
	}
	return nil
}

func jsLen(s string) int {
	return len(utf16.Encode([]rune(s)))
}

func optionalText(v map[string]any, key string, limit int) (string, bool) {
	raw, present := v[key]
	if !present || raw == nil {
		return "", true
	}
	s, ok := raw.(string)
	if !ok || jsLen(s) > limit {
		return "", false
	}
	return s, true
}

func checkedOf(v map[string]any) *checked {
	if v == nil {
		return nil
	}
	kind, ok := v["kind"].(string)
	if !ok || jsLen(kind) > 40 {
		return nil
	}
	waits, ok1 := v["waits"].(bool)
	pushes, ok2 := v["pushes"].(bool)
	if !ok1 || !ok2 {
		return nil
	}
	protected, ok1 := optionalText(v, "protected", 500)
	held, ok2 := optionalText(v, "held", 2000)
	cloud, ok3 := optionalText(v, "cloud", 500)
	if !ok1 || !ok2 || !ok3 {
		return nil
	}
	irreversible := false
	if raw, present := v["irreversible"]; present {
		b, ok := raw.(bool)
		if !ok {
			return nil
		}
		irreversible = b
	}
	guarded, ok := optionalText(v, "guarded", 500)
	if !ok {
		return nil
	}
	return &checked{kind: kind, waits: waits, pushes: pushes, protected: protected, held: held, cloud: cloud, irreversible: irreversible, guarded: guarded, engine: true}
}

func parsed(body []byte) map[string]any {
	var v any
	if json.Unmarshal(body, &v) != nil {
		return nil
	}
	m, _ := v.(map[string]any)
	return m
}

func mcpEnv(name string) string {
	for _, s := range stdio {
		if s.Name != "software-factory" {
			continue
		}
		for _, e := range s.Env {
			if e.Name == name {
				return e.Value
			}
		}
	}
	return ""
}

func socketPath() string {
	for _, given := range []string{os.Getenv("SF_PLUGIN_SOCKET"), mcpEnv("SF_PLUGIN_SOCKET")} {
		if strings.HasPrefix(given, "/") && len(given) <= 103 && !strings.Contains(given, "\x00") {
			return given
		}
	}
	return defaultSocket
}

func realOf(p string) string {
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return ""
	}
	return strings.TrimRight(real, "/")
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func worktreeTop() string {
	here := strings.TrimRight(cwd, "/")
	if topOf.cwd == here && topOf.top != "" {
		return topOf.top
	}
	dir, top := here, ""
	for i := 0; i < 64 && dir != ""; i++ {
		if exists(dir + "/.git") {
			if top = realOf(dir); top == "" {
				top = dir
			}
			break
		}
		up := dir[:max(strings.LastIndex(dir, "/"), 0)]
		if up == dir {
			break
		}
		dir = up
	}
	if top == "" {
		if top = realOf(here); top == "" {
			top = here
		}
	}
	topOf.cwd, topOf.top = here, top
	return top
}

func tooLong(p string) bool {
	return len(p) > maxPathBytes
}

func partsOf(p string) []string {
	var parts []string
	for part := range strings.SplitSeq(p, "/") {
		if part != "" && part != "." {
			parts = append(parts, part)
		}
	}
	return parts
}

func linkOf(p string) string {
	target, err := os.Readlink(p)
	if err != nil {
		return ""
	}
	return target
}

func parentOf(p string) string {
	return p[:max(strings.LastIndex(p, "/"), 0)]
}

func depthOf(p string) int {
	return strings.Count(p, "/")
}

func walked(steps []string) (resolved, held string) {
	left := slices.Clone(steps)
	cur, goneAt, hops := "", 0, 0
	for len(left) > 0 {
		part := left[0]
		left = left[1:]
		if part == ".." {
			cur = parentOf(cur)
			if goneAt > 0 && depthOf(cur) < goneAt {
				goneAt = 0
			}
			continue
		}
		cur += "/" + part
		if goneAt > 0 {
			continue
		}
		if real := realOf(cur); real != "" {
			cur = real
			continue
		}
		info, err := os.Lstat(cur)
		found := err == nil
		if found && info.Mode()&os.ModeSymlink == 0 {
			goneAt = depthOf(cur)
			continue
		}
		target := linkOf(cur)
		if target == "" {
			if found {
				return "", linkUntold
			}
			goneAt = depthOf(cur)
			continue
		}
		if hops++; hops > maxLinkHops || !utf8.ValidString(target) || strings.ContainsRune(target, utf8.RuneError) {
			return "", linkUntold
		}
		if strings.HasPrefix(target, "/") {
			cur = ""
		} else {
			cur = parentOf(cur)
		}
		left = append(partsOf(target), left...)
	}
	resolved = cur
	if resolved == "" {
		resolved = "/"
	}
	if tooLong(resolved) {
		return "", pathTooLong
	}
	return resolved, ""
}

func placed(p string) ([]string, string) {
	spellings := []string{p}
	if trimmed := strings.Trim(p, jsTrimmed); trimmed != "" && trimmed != p {
		spellings = append(spellings, trimmed)
	}
	var forms []string
	for _, spelling := range spellings {
		place, held := placedAs(spelling)
		if held != "" {
			return nil, held
		}
		for _, x := range place {
			if !slices.Contains(forms, x) {
				forms = append(forms, x)
			}
		}
	}
	return forms, ""
}

func placedAs(p string) ([]string, string) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home := os.Getenv("HOME")
		if !strings.HasPrefix(home, "/") {
			return nil, pathUnplaced
		}
		p = home + p[1:]
	}
	here := strings.TrimRight(cwd, "/")
	full := p
	if !strings.HasPrefix(p, "/") {
		full = here + "/" + p
	}
	steps := partsOf(full)
	if len(steps) > maxPathSteps {
		return nil, pathTooLong
	}
	var parts []string
	for _, part := range steps {
		if part == ".." {
			if len(parts) > 0 {
				parts = parts[:len(parts)-1]
			}
		} else {
			parts = append(parts, part)
		}
	}
	plain := "/" + strings.Join(parts, "/")
	if tooLong(plain) {
		return nil, pathTooLong
	}
	real, held := walked(steps)
	if held != "" {
		return nil, held
	}
	folded, held := walked(parts)
	if held != "" {
		return nil, held
	}
	return []string{plain, real, folded}, ""
}

func placeOf(input map[string]any) (*place, string) {
	top := worktreeTop()
	paths := []string{}
	for _, key := range []string{"file_path", "notebook_path"} {
		p, ok := input[key].(string)
		if !ok || p == "" {
			continue
		}
		if tooLong(p) {
			return nil, pathTooLong
		}
		forms, held := placed(p)
		if held != "" {
			return nil, held
		}
		for _, x := range forms {
			if !slices.Contains(paths, x) {
				paths = append(paths, x)
			}
		}
	}
	if tooLong(top) {
		if len(paths) > 0 {
			return nil, pathTooLong
		}
		return &place{paths: paths}, ""
	}
	return &place{top: top, paths: paths}, ""
}

func engineClient() *http.Client {
	socket := socketPath()
	return &http.Client{Timeout: askFor, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}
}

func askEngine(key string, body []byte) asked {
	req, err := http.NewRequest(http.MethodPost, "http://engine/check", bytes.NewReader(body))
	if err != nil {
		return asked{source: "fallback after no answer"}
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-sf-conn", key)
	res, err := engineClient().Do(req)
	if err != nil {
		downUntil = time.Now().Add(downFor)
		return asked{source: "fallback after no answer"}
	}
	defer func() { _ = res.Body.Close() }()
	got, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		downUntil = time.Now().Add(downFor)
		return asked{status: res.StatusCode, body: got, source: "fallback after no answer"}
	}
	a := asked{status: res.StatusCode, body: got}
	if res.StatusCode == http.StatusOK {
		a.found = checkedOf(parsed(got))
	}
	if a.found == nil {
		a.source = "fallback after status " + strconv.Itoa(res.StatusCode)
	} else {
		a.source = "engine " + strconv.Itoa(res.StatusCode)
	}
	return a
}

func checkTool(tool string, input map[string]any) (checked, asked) {
	pl, held := placeOf(input)
	if pl == nil {
		v := fallback(tool, input)
		v.held = held
		source := "fallback without asking: path too long"
		switch held {
		case linkUntold:
			source = "fallback without asking: where the links lead can't be told"
		case pathUnplaced:
			source = "fallback without asking: where the file is can't be told"
		}
		return v, asked{source: source}
	}
	key := mcpEnv(checkKeyVariable)
	sent := sized(input)
	switch {
	case sent == nil:
		return fallback(tool, input), asked{source: "fallback without asking: input too large"}
	case key == "":
		return fallback(tool, input), asked{source: "fallback without asking: no key"}
	case time.Now().Before(downUntil):
		return fallback(tool, input), asked{source: "fallback without asking: engine down"}
	}
	body := encoded(struct {
		Tool  string         `json:"tool"`
		Input map[string]any `json:"input"`
		Top   string         `json:"top"`
		Paths []string       `json:"paths"`
	}{tool, sent, pl.top, pl.paths})
	a := askEngine(key, body)
	if a.found == nil {
		return fallback(tool, input), a
	}
	return *a.found, a
}

func refusalOf(v checked) string {
	switch {
	case v.pushes && v.engine:
		return "Software Factory refused this command: it pushes to the git host or changes a pull request there"
	case v.pushes:
		return "Software Factory couldn't reach its engine to check this command, and the command may push to the git host or change a pull request there, so it refused it"
	case v.cloud != "":
		return "Software Factory refused this command: " + v.cloud
	case v.protected != "":
		return "Software Factory doesn't let Claude write this file: " + v.protected
	}
	return v.held
}

func inside(top, p string) bool {
	return top != "" && strings.HasPrefix(p, top+"/")
}

func act(tool string, input map[string]any, top string) string {
	if !editTools[tool] {
		return "allowed"
	}
	file, _ := input["file_path"].(string)
	if tool == "NotebookEdit" {
		file, _ = input["notebook_path"].(string)
	}
	if file == "" {
		return "allowed, not written: no path"
	}
	forms, held := placed(file)
	if held != "" || slices.ContainsFunc(forms, func(f string) bool { return !inside(top, f) }) {
		return "allowed, not written: outside the worktree"
	}
	plain := forms[0]
	if tool == "Write" {
		content, _ := input["content"].(string)
		if os.MkdirAll(filepath.Dir(plain), 0o755) != nil || os.WriteFile(plain, []byte(content), 0o644) != nil {
			return "allowed, write failed"
		}
		return "allowed, wrote"
	}
	f, err := os.OpenFile(plain, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return "allowed, write failed"
	}
	_, err = f.WriteString("\n")
	if errors.Join(err, f.Close()) != nil {
		return "allowed, write failed"
	}
	return "allowed, wrote"
}

func toolKind(tool string) string {
	switch {
	case editTools[tool]:
		return "edit"
	case shellTools[tool]:
		return "execute"
	}
	return "other"
}

func record(id string, a asked, outcome string) {
	dir := filepath.Join(cwd, answersFolder)
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, id+".json"), a.body, 0o644)
	_ = os.WriteFile(filepath.Join(dir, id+".txt"), []byte(a.source+": "+outcome+"\n"), 0o644)
}

func tryTool(callID, id, tool string, input map[string]any) {
	title := tool
	if f, ok := input["file_path"].(string); ok {
		title += " " + f
	} else if c, ok := input["command"].(string); ok {
		title += " " + c
	}
	report := func(status, suffix string) {
		update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": callID, "title": title + suffix, "kind": toolKind(tool), "status": status, "rawInput": input})
	}
	if !answerID.MatchString(id) || tool == "" {
		report("failed", ": the script line needs an answer id of a-z, 0-9, _ and -, and a tool")
		return
	}
	v, a := checkTool(tool, input)
	if why := refusalOf(v); why != "" {
		record(id, a, "refused")
		report("failed", ": "+why)
		return
	}
	if v.waits || v.irreversible || v.guarded != "" {
		record(id, a, "asked")
		report("pending", "")
		report("failed", ": asked the developer, which the fake agent doesn't wait for")
		return
	}
	outcome := act(tool, input, worktreeTop())
	record(id, a, outcome)
	status := "completed"
	if outcome == "allowed, write failed" || outcome == "allowed, not written: outside the worktree" {
		status = "failed"
	}
	report(status, "")
}

func tryLine(callID, key, value string) bool {
	id, rest, _ := strings.Cut(value, " ")
	rest = strings.TrimSpace(rest)
	switch key {
	case "try":
		tryTool(callID, id, "Write", map[string]any{"file_path": rest, "content": "ok\n"})
	case "try-run":
		tryTool(callID, id, "Bash", map[string]any{"command": rest})
	case "try-tool":
		tool, raw, _ := strings.Cut(rest, " ")
		input := map[string]any{}
		if strings.TrimSpace(raw) != "" && json.Unmarshal([]byte(raw), &input) != nil {
			tool = ""
		}
		tryTool(callID, id, tool, input)
	default:
		return false
	}
	return true
}

func keyAt(callID, value string) {
	fields := strings.SplitN(value, " ", 5)
	report := func(status string) {
		update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": callID, "title": "key-at " + value, "kind": "other", "status": status, "rawInput": map[string]any{}})
	}
	if len(fields) < 4 || !answerID.MatchString(fields[0]) || (fields[1] != "conn" && fields[1] != "push") || !strings.HasPrefix(fields[3], "/") {
		report("failed")
		return
	}
	id, header, method, route := fields[0], "x-sf-conn", fields[2], fields[3]
	if fields[1] == "push" {
		header = "x-sf-push"
	}
	var body io.Reader
	if len(fields) == 5 {
		body = strings.NewReader(fields[4])
	}
	req, err := http.NewRequest(method, "http://engine"+route, body)
	if err != nil {
		report("failed")
		return
	}
	if body != nil {
		req.Header.Set("content-type", "application/json")
	}
	key := mcpEnv(checkKeyVariable)
	if key == "" {
		record(id, asked{source: "no key"}, "nothing sent")
		report("failed")
		return
	}
	req.Header.Set(header, key)
	a := asked{source: "no answer"}
	if res, err := engineClient().Do(req); err == nil {
		got, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
		_ = res.Body.Close()
		a = asked{status: res.StatusCode, body: got, source: "status " + strconv.Itoa(res.StatusCode)}
	}
	record(id, a, "sent the check-only key as "+header)
	report("completed")
}

func askTool(callID, value string) {
	id, rest, _ := strings.Cut(value, " ")
	tool, raw, _ := strings.Cut(strings.TrimSpace(rest), " ")
	args := map[string]any{}
	status := "failed"
	if answerID.MatchString(id) && slices.Contains(tools, tool) && json.Unmarshal([]byte(raw), &args) == nil {
		m, problem := callToolReply(tool, args)
		a := asked{source: problem}
		if problem == "" {
			body, _ := json.Marshal(m["result"])
			a = asked{body: append(body, '\n'), source: "mcp"}
			status = "completed"
		}
		record(id, a, "called "+tool)
	}
	update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": callID, "title": "ask-tool " + value, "kind": "other", "status": status, "rawInput": args})
}

func placeLine(callID, key, value string) bool {
	switch key {
	case "ask-tool":
		askTool(callID, value)
	case "key-at":
		keyAt(callID, value)
	case "mkdir":
		dir := filepath.Join(cwd, filepath.Clean("/"+value))
		status := "completed"
		if os.MkdirAll(dir, 0o755) != nil {
			status = "failed"
		}
		update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": callID, "title": "mkdir " + value, "kind": "execute", "status": status, "rawInput": map[string]any{"command": "mkdir -p " + value}})
	case "link":
		name, target, _ := strings.Cut(value, "->")
		name, target = strings.TrimSpace(name), strings.TrimSpace(target)
		at := filepath.Join(cwd, filepath.Clean("/"+name))
		status := "completed"
		if name == "" || target == "" || os.Symlink(target, at) != nil {
			status = "failed"
		}
		update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": callID, "title": "ln -s " + target + " " + name, "kind": "execute", "status": status, "rawInput": map[string]any{"command": "ln -s " + target + " " + name}})
	default:
		return false
	}
	return true
}
