package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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
	slowFor   = 45 * time.Second
	fakeModel = "sf-fake-model"
	fakeCost  = 0.0125
)

func main() {
	if authStatus(os.Args[1:]) {
		return
	}
	r, err := review(os.Args[1:])
	out := result{Type: "result", Subtype: "success", NumTurns: r.turns, Result: r.text, SessionID: "fake-reviewer", Cost: fakeCost,
		Denials: r.denials, ModelUsage: map[string]usage{fakeModel: {CostUSD: fakeCost}}}
	if out.Denials == nil {
		out.Denials = []denial{}
	}
	code := 0
	if err != nil {
		out.IsError, out.Result, out.NumTurns, code = true, "sf-fake-claude refused: "+err.Error(), 1, 1
	}
	data, _ := json.Marshal(out)
	fmt.Println(string(data))
	os.Exit(code)
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

func review(args []string) (reply, error) {
	if len(args) < 2 || !slices.Contains(args, "-p") {
		return reply{}, errors.New("not started headless with -p")
	}
	for flag, want := range map[string]string{"--setting-sources": "user", "--output-format": "json", "--permission-mode": "dontAsk"} {
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
	if named && !strings.HasPrefix(agent, "knackpack:") || !named && !strings.Contains(strings.ToLower(prompt), "manual test list") {
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
	if !named && has("TOKEN-IN-REVIEW") {
		return reply{text: "Manual test list (from the fake reviewer):\n1. Sign in with " + "sk-" + "ant-" + "api03-" + strings.Repeat("Fake0", 8) + ".", turns: 3}, nil
	}
	if !named {
		return reply{text: "Manual test list (from the fake reviewer):\n1. Open the page and start a session.\n2. Change a file, commit it and push.\n3. Check the push reached the git host.", turns: 3}, nil
	}
	return verdict(agent, prompt), nil
}

func has(name string) bool {
	_, err := os.Lstat(filepath.Join("head", name))
	return err == nil
}

func verdict(agent, prompt string) reply {
	r := reply{turns: 3}
	if has("SHALLOW-REVIEW") {
		r.turns = 1
	}
	if has("READ-OUTSIDE") {
		r.denials = []denial{{ToolName: "Read", ToolUseID: "fake", ToolInput: map[string]any{"file_path": "/home/reviewer/.claude/.credentials.json"}}}
	}
	report := "/tmp/review-report-" + strings.TrimPrefix(agent, "knackpack:") + "/report.md\n" + seen(prompt) + earlier(prompt)
	if has("SLOW-REVIEW") {
		time.Sleep(slowFor)
	}
	if has("TOKEN-IN-REVIEW") {
		token := "sk-" + "ant-" + "api03-" + strings.Repeat("Fake0", 8)
		r.text = report + "\n**Changes required:** head/TOKEN-IN-REVIEW asks the fake reviewer to quote " + token + " and block.\nknackpack-verdict/1: block"
		return r
	}
	if has("BLOCK-REVIEW") {
		r.text = report + "\n**Changes required:** head/BLOCK-REVIEW asks the fake reviewer to block.\nknackpack-verdict/1: block"
		return r
	}
	if has("SEVERE-PASS") {
		r.text = report + "\n### [High] head/SEVERE-PASS asks the fake reviewer to pass with a severe finding\n\n## Findings table\n\n| ID | Severity | Blocks | Title |\n|---|---|---|---|\n| F1 | High | yes | head/SEVERE-PASS |\n\nknackpack-verdict/1: pass"
		return r
	}
	pass := "No blocking issues found."
	if agent == "knackpack:slop-reviewer" {
		pass = "Nothing to remove in scope."
	}
	r.text = report + "\n" + pass + "\nknackpack-verdict/1: pass"
	return r
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
	flat := strings.Join(strings.Fields(prompt), " ")
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
	return "Prompt: " + flat + "\nchange.diff: " + strings.Join(files, "; ")
}
