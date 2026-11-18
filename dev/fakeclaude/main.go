package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

type denial struct {
	ToolName  string         `json:"tool_name"`
	ToolUseID string         `json:"tool_use_id"`
	ToolInput map[string]any `json:"tool_input"`
}

type usage struct {
	CostUSD float64 `json:"costUSD"`
}

type result struct {
	Type       string           `json:"type"`
	Subtype    string           `json:"subtype"`
	IsError    bool             `json:"is_error"`
	Duration   int              `json:"duration_ms"`
	NumTurns   int              `json:"num_turns"`
	Result     string           `json:"result"`
	SessionID  string           `json:"session_id"`
	Cost       float64          `json:"total_cost_usd"`
	Denials    []denial         `json:"permission_denials"`
	ModelUsage map[string]usage `json:"modelUsage"`
}

type reply struct {
	text    string
	turns   int
	denials []denial
}

var (
	keyLike     = regexp.MustCompile(`(?i)(API_?KEY|TOKEN|SECRET|PASSWORD|CREDENTIAL|AUTH)`)
	earlierFile = regexp.MustCompile(`earlier/[a-z0-9-]{1,100}\.md`)
)

const (
	slowFor        = 45 * time.Second
	slowMost       = 3600
	fakeModel      = "sf-fake-model"
	fakeSmallModel = "sf-fake-small-model"
	fakeCost       = 0.0125
	fakeSmallCost  = 0.0025
)

func main() {
	if authStatus(os.Args[1:]) || version(os.Args[1:]) {
		return
	}
	st := newStream()
	r, err := review(os.Args[1:], st)
	out := result{Type: "result", Subtype: "success", NumTurns: r.turns, Result: r.text, SessionID: "fake-reviewer", Cost: fakeCost,
		Denials: r.denials, ModelUsage: map[string]usage{modelOf(os.Args[1:]): {CostUSD: fakeCost}}}
	if err == nil && has("TWO-MODELS") {
		out.ModelUsage[fakeSmallModel] = usage{CostUSD: fakeSmallCost}
		out.Cost += fakeSmallCost
	}
	if out.Denials == nil {
		out.Denials = []denial{}
	}
	code := 0
	if err != nil {
		out.IsError, out.Result, out.NumTurns, code = true, "sf-fake-claude refused: "+err.Error(), 1, 1
	}
	if err == nil {
		st.say(r.text)
	}
	st.line(out)
	os.Exit(code)
}

func modelOf(args []string) string {
	if m, ok := value(args, "--model"); ok && m != "" {
		return m
	}
	return fakeModel
}

func given(args []string) string {
	m, _ := value(args, "--model")
	e, _ := value(args, "--effort")
	if m == "" {
		m = "none"
	}
	if e == "" {
		e = "none"
	}
	return "\nGiven --model " + m + " and --effort " + e
}

func value(args []string, flag string) (string, bool) {
	for i, a := range args {
		if v, ok := strings.CutPrefix(a, flag+"="); ok {
			return v, true
		}
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

func review(args []string, st *stream) (reply, error) {
	if len(args) < 2 || !slices.Contains(args, "-p") {
		return reply{}, errors.New("not started headless with -p")
	}
	if !slices.Contains(args, "--verbose") {
		return reply{}, errors.New("--output-format stream-json without --verbose")
	}
	for flag, want := range map[string]string{"--setting-sources": "user", "--output-format": "stream-json", "--permission-mode": "dontAsk"} {
		if v, _ := value(args, flag); v != want {
			return reply{}, fmt.Errorf("%s isn't %s", flag, want)
		}
	}
	for _, bad := range []string{"--dangerously-skip-permissions", "--allow-dangerously-skip-permissions", "bypassPermissions", "auto"} {
		if slices.Contains(args, bad) {
			return reply{}, fmt.Errorf("started with %s", bad)
		}
	}
	if err := confined(args); err != nil {
		return reply{}, err
	}
	agent, named := value(args, "--agent")
	prompt := args[len(args)-1]
	userAgent := ""
	if named && !strings.HasPrefix(agent, "knackpack:") {
		found, err := userAgentFile(agent)
		if err != nil {
			return reply{}, err
		}
		userAgent = found
	}
	aside := asideOf(prompt)
	if !named && aside == "" && !strings.Contains(strings.ToLower(prompt), "manual test list") {
		return reply{}, errors.New("no knackpack agent named")
	}
	plugin, _ := value(args, "--plugin-dir")
	if info, err := os.Stat(filepath.Join(plugin, ".claude-plugin", "plugin.json")); err != nil || !info.Mode().IsRegular() {
		return reply{}, errors.New("--plugin-dir doesn't name an installed plugin")
	}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if keyLike.MatchString(name) {
			return reply{}, fmt.Errorf("the variable %s reached the reviewer", name)
		}
	}
	for _, d := range []string{"base", "head"} {
		if info, err := os.Lstat(d); err != nil || !info.IsDir() {
			return reply{}, fmt.Errorf("the folder has no %s/", d)
		}
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			return reply{}, fmt.Errorf("%s/ has a .git", d)
		}
	}
	if _, err := os.Lstat(".git"); err == nil {
		return reply{}, errors.New("the folder has a .git")
	}
	st.init(args)
	st.readAll()
	if !named && has("TOKEN-IN-REVIEW") {
		return reply{text: "Manual test list (from the fake reviewer):\n1. Sign in with " + "sk-" + "ant-" + "api03-" + strings.Repeat("Fake0", 8) + ".", turns: 3}, nil
	}
	if !named && aside != "" {
		return asideReply(aside, prompt), nil
	}
	if !named {
		return reply{text: "Manual test list (from the fake reviewer):\n1. Open the page and start a session on the demo project.\n   Expected: the session shows as Running with its branch.\n   Input: none\n" +
			"2. Change a file, commit it and press Push.\n   Expected: the push card shows the commit and both reviews.\n   Input: the line `hello from the walkthrough` added to README.md\n" +
			"3. Check the push reached the git host.\n   Expected: the branch's commit on the git host is the session's latest commit.\n   Input: none", turns: 3}, nil
	}
	r := verdict(agent, prompt, st)
	r.text = findingLines(r.text)
	r.text = strings.Replace(r.text, "\n", given(args)+"\n", 1)
	if userAgent != "" {
		r.text = strings.Replace(r.text, "knackpack-verdict/1: ", "User agent file: "+userAgent+"\nVerdict: ", 1)
	}
	return r, nil
}

func userAgentFile(agent string) (string, error) {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(os.Getenv("HOME"), ".claude")
	}
	path := filepath.Join(dir, "agents", "sf-"+agent+".md")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("--agent %s names no knackpack agent and no file sf-%s.md in the user agents folder", agent, agent)
	}
	data, err := os.ReadFile(path)
	if err != nil || !slices.Contains(strings.Split(string(data), "\n"), "name: "+agent) {
		return "", fmt.Errorf("the user agents folder's sf-%s.md doesn't name %s", agent, agent)
	}
	return path, nil
}

func has(name string) bool {
	_, err := os.Lstat(filepath.Join("head", name))
	return err == nil
}

func slowness() time.Duration {
	data, err := os.ReadFile(filepath.Join("head", "SLOW-REVIEW"))
	if err != nil {
		return slowFor
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || n < 1 || n > slowMost {
		return slowFor
	}
	return time.Duration(n) * time.Second
}

func verdict(agent, prompt string, st *stream) reply {
	r := reply{turns: 3}
	if has("SHALLOW-REVIEW") {
		r.turns = 1
	}
	if has("READ-OUTSIDE") {
		r.denials = []denial{{ToolName: "Read", ToolUseID: "fake", ToolInput: map[string]any{"file_path": "/home/reviewer/.claude/.credentials.json"}}}
	}
	report := "/tmp/review-report-" + strings.TrimPrefix(agent, "knackpack:") + "/report.md\n" + seen(prompt) + earlier(prompt) + longReport()
	if has("SLOW-REVIEW") {
		st.slow(slowness())
	}
	if switched(passAllFile) {
		r.text = report + "\n$HOME/" + passAllFile + " asks the fake reviewer to pass everything.\nNo blocking issues found.\nknackpack-verdict/1: pass"
		return r
	}
	if has("TOKEN-IN-REVIEW") {
		token := "sk-" + "ant-" + "api03-" + strings.Repeat("Fake0", 8)
		r.text = report + "\n**Changes required:** head/TOKEN-IN-REVIEW asks the fake reviewer to quote " + token + " and block.\nknackpack-verdict/1: block"
		return r
	}
	if n := blockRun(); n > 0 && n == runOf(prompt) {
		r.text = report + "\n**Changes required:** head/BLOCK-RUN asks the fake reviewer to block on run " + strconv.Itoa(n) + ".\n\n### [High] head/BLOCK-RUN asks for a block on one run\n\n**Location:** `BLOCK-RUN:1`\n\nThe file names the run that blocks.\nknackpack-verdict/1: block"
		return r
	}
	if has("BLOCK-FINDING") {
		r.text = report + "\n**Changes required:** head/BLOCK-FINDING asks the fake reviewer to block with one finding.\n\n### [High] head/BLOCK-FINDING asks for a repeated blocking finding\n\n**Location:** `README.md:1`\n\nThe same finding comes back on every commit that carries the file.\nknackpack-verdict/1: block"
		return r
	}
	if has("BLOCK-REVIEW") {
		r.text = report + "\n**Changes required:** head/BLOCK-REVIEW asks the fake reviewer to block.\nknackpack-verdict/1: block"
		return r
	}
	if plantedDefect() && agent == slopReviewer {
		r.text = report + "\n**Verdict: SIMPLER CHANGE AVAILABLE**\n### [Medium] change.diff adds a line that is only `" + plantedLine + "`\n" + plantedFindings + "\nknackpack-verdict/1: pass"
		return r
	}
	if plantedDefect() {
		r.text = report + "\n**Changes required:** change.diff adds a line that is only `" + plantedLine + "`.\nknackpack-verdict/1: block"
		return r
	}
	if has("SEVERE-PASS") {
		r.text = report + "\n### [High] head/SEVERE-PASS asks the fake reviewer to pass with a severe finding\n\n## Findings table\n\n| ID | Severity | Blocks | Title |\n|---|---|---|---|\n| F1 | High | yes | head/SEVERE-PASS |\n" + findingsLine() + "\nknackpack-verdict/1: pass"
		return r
	}
	if has("LOW-FINDINGS") && agent == "knackpack:adversarial-reviewer" {
		r.text = report + "\nNo blocking issues found.\n\n### [Low] head/LOW-FINDINGS asks for a first low finding\n\n**Location:** `README.md:1`\n\nThe first line could say more.\n\n### [Low] head/LOW-FINDINGS asks for a second low finding\n\n**Location:** `README.md:2`\n\nThe second line could be shorter.\n\nknackpack-findings/1: critical=0 high=0 medium=0 low=2 blocking=0\nknackpack-verdict/1: pass"
		return r
	}
	if has("CITE-CVES") {
		r.text = report + "\nNo blocking issues found. head/CITE-CVES asks the fake reviewer to cite " + realCVE + " and " + madeCVE + "." + findingsLine() + "\nknackpack-verdict/1: pass"
		return r
	}
	pass := "No blocking issues found."
	if agent == slopReviewer {
		pass = "Nothing to remove in scope."
	}
	r.text = report + "\n" + pass + findingsLine() + "\nknackpack-verdict/1: pass"
	return r
}

func findingsLine() string {
	data, err := os.ReadFile(filepath.Join("head", "FINDINGS-LINE"))
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(string(data), "\n")
	return "\n" + strings.TrimSpace(line)
}

func earlier(prompt string) string {
	var out strings.Builder
	for _, name := range earlierFile.FindAllString(prompt, 4) {
		state := "absent"
		if info, err := os.Lstat(name); err == nil && info.Mode().IsRegular() {
			state = "present"
		}
		out.WriteString("\n" + name + ": " + state)
	}
	return out.String()
}

func seen(prompt string) string {
	whole := strings.Join(strings.Fields(prompt), " ")
	flat := whole
	if r := []rune(flat); len(r) > 1000 {
		flat = string(r[:500]) + " … " + string(r[len(r)-500:])
	}
	var files []string
	if data, err := os.ReadFile("change.diff"); err == nil {
		for l := range strings.SplitSeq(string(data), "\n") {
			if f, ok := strings.CutPrefix(l, "diff --git "); ok && len(files) < 20 {
				files = append(files, f)
			}
		}
	}
	out := "Prompt: " + flat + "\nchange.diff: " + strings.Join(files, "; ")
	if i := strings.Index(whole, " The team file "); i >= 0 {
		rest := whole[i:]
		if j := strings.Index(rest, " Check what the agent says"); j >= 0 {
			rest = rest[:j]
		}
		out += "\nTeam rules given:" + rest
	}
	return out
}

func asideOf(prompt string) string {
	switch {
	case strings.HasPrefix(prompt, "Explain the change in this folder"):
		return "explain"
	case strings.HasPrefix(prompt, "Review the implementation plan in plan.md"):
		return "plan"
	case strings.HasPrefix(prompt, "Suggest instruction-file lines"):
		return "lessons"
	}
	return ""
}

func asideReply(kind, prompt string) reply {
	if kind == "lessons" {
		return lessonsReply()
	}
	if has("TOKEN-IN-REVIEW") {
		return reply{text: "## From the fake reviewer\n\nSign in with " + "sk-" + "ant-" + "api03-" + strings.Repeat("Fake0", 8) + ".", turns: 2}
	}
	if kind == "explain" {
		return reply{text: "## What this change does (from the fake reviewer)\n\n" + seen(prompt) + "\n\n- **Read first:** `change.diff`\n- **Could break:** nothing the fake reviewer can tell.", turns: 2}
	}
	plan, err := os.ReadFile("plan.md")
	first := "plan.md is missing"
	if err == nil {
		lines := strings.Split(string(plan), "\n")
		first = fmt.Sprintf("plan.md has %d lines; its first line: %s", len(lines), lines[0])
	}
	return reply{text: "## Findings on the plan (from the fake reviewer)\n\n1. **Step 1** looks fine.\n2. " + first + "\n\n" + seen(prompt), turns: 2}
}
