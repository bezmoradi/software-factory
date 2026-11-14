package state

import (
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/review"
	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	checksFile      = "reviewer-checks.json"
	maxChecks       = 200
	maxCheckReason  = 1000
	maxCheckVerdict = 200
	maxCheckReply   = 4000
)

var (
	reviewerName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:._-]{0,199}$`)
	checkResults = []review.CheckResult{review.CheckRunning, review.CheckPassed, review.CheckFailure, review.CheckWarned, review.CheckUnsettled}
	checkStates  = []review.State{review.Running, review.Pass, review.Block, review.Unknown, review.Failed}
	checkBy      = []string{session.ByUpdate, session.ByPage}
	checkRoles   = append([]review.Role{review.NoRole}, review.Roles...)
)

type Checks struct {
	Dir string
}

var _ session.ReviewerChecks = Checks{}

type checkRunJSON struct {
	State   string `json:"state,omitempty"`
	Verdict string `json:"verdict,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Reply   string `json:"reply,omitempty"`
}

type checkJSON struct {
	Agent      string       `json:"agent"`
	Role       string       `json:"role"`
	ClaudeCode string       `json:"claudeCode"`
	Plugin     string       `json:"plugin,omitempty"`
	Models     []string     `json:"models,omitempty"`
	Model      string       `json:"model,omitempty"`
	Effort     string       `json:"effort,omitempty"`
	Result     string       `json:"result"`
	Defect     checkRunJSON `json:"defect"`
	Clean      checkRunJSON `json:"clean"`
	Reason     string       `json:"reason,omitempty"`
	By         string       `json:"by"`
	StartedAt  time.Time    `json:"startedAt"`
	EndedAt    time.Time    `json:"endedAt,omitzero"`
	CostUSD    float64      `json:"costUsd,omitempty"`
}

type checksJSON struct {
	Version int         `json:"version"`
	Checks  []checkJSON `json:"checks"`
}

func (s Checks) Load() ([]session.ReviewerCheck, error) {
	data, err := read(s.Dir, checksFile)
	if err != nil || data == nil {
		return []session.ReviewerCheck{}, err
	}
	var f checksJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 {
		return nil, errors.New("reviewer-checks.json isn't a reviewer check record this engine reads")
	}
	out := make([]session.ReviewerCheck, 0, len(f.Checks))
	for _, c := range f.Checks {
		if len(out) >= maxChecks {
			break
		}
		v, ok := checkOf(c)
		if ok {
			out = append(out, v)
		}
	}
	return out, nil
}

func checkOf(c checkJSON) (session.ReviewerCheck, bool) {
	result, by := review.CheckResult(c.Result), c.By
	if !reviewerName.MatchString(c.Agent) || !slices.Contains(checkRoles, review.Role(c.Role)) || !versionText.MatchString(c.ClaudeCode) ||
		!slices.Contains(checkResults, result) || !slices.Contains(checkBy, by) || c.StartedAt.IsZero() || !session.ValidReviewChoice(c.Model, c.Effort) {
		return session.ReviewerCheck{}, false
	}
	plugin := c.Plugin
	if !commitID.MatchString(plugin) && !session.AgentFileID.MatchString(plugin) {
		plugin = ""
	}
	return session.ReviewerCheck{Agent: c.Agent, Role: review.Role(c.Role), Result: result, By: by, StartedAt: c.StartedAt, EndedAt: c.EndedAt,
		Versions: review.Versions{ClaudeCode: c.ClaudeCode, Plugin: plugin, Models: matchingAll(c.Models, modelName, session.MaxCheckModels), Model: c.Model, Effort: c.Effort},
		Defect:   runOfCheck(c.Defect), Clean: runOfCheck(c.Clean), Reason: cleanReason(c.Reason, maxCheckReason), CostUSD: max(c.CostUSD, 0)}, true
}

func runOfCheck(r checkRunJSON) session.CheckRunResult {
	state := review.State(r.State)
	if !slices.Contains(checkStates, state) {
		state = ""
	}
	reply := r.Reply
	if len(reply) > maxCheckReply || !utf8.ValidString(reply) {
		reply = ""
	}
	return session.CheckRunResult{State: state, Verdict: cleanReason(r.Verdict, maxCheckVerdict), Reason: cleanReason(r.Reason, maxCheckReason), Reply: reply}
}

func checkRunOf(r session.CheckRunResult) checkRunJSON {
	return checkRunJSON{State: string(r.State), Verdict: r.Verdict, Reason: r.Reason, Reply: r.Reply}
}

func cleanReason(s string, n int) string {
	if cleanText(s, n) {
		return s
	}
	return ""
}

func (s Checks) Save(list []session.ReviewerCheck) error {
	f := checksJSON{Version: 1, Checks: make([]checkJSON, 0, len(list))}
	for _, c := range list {
		f.Checks = append(f.Checks, checkJSON{Agent: c.Agent, Role: string(c.Role), ClaudeCode: c.Versions.ClaudeCode, Plugin: c.Versions.Plugin, Models: c.Versions.Models, Model: c.Versions.Model, Effort: c.Versions.Effort,
			Result: string(c.Result), Defect: checkRunOf(c.Defect), Clean: checkRunOf(c.Clean), Reason: c.Reason, By: c.By,
			StartedAt: c.StartedAt.UTC(), EndedAt: c.EndedAt.UTC(), CostUSD: c.CostUSD})
	}
	return write(s.Dir, checksFile, f)
}
