package state

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/review"
	"github.com/bezmoradi/software-factory/internal/core/session"
)

const reviewsFile = "reviews.json"

type Reviews struct {
	Dir string
}

var _ session.ReviewRecords = Reviews{}

type reviewJSON struct {
	Commit       string    `json:"commit"`
	Branch       string    `json:"branch"`
	Agent        string    `json:"agent"`
	Role         string    `json:"role"`
	Provider     string    `json:"provider"`
	State        string    `json:"state"`
	Verdict      string    `json:"verdict,omitempty"`
	Channel      string    `json:"channel,omitempty"`
	Override     string    `json:"override,omitempty"`
	Base         string    `json:"base,omitempty"`
	Since        string    `json:"since,omitempty"`
	Covers       string    `json:"covers,omitempty"`
	Message      string    `json:"message,omitempty"`
	Reason       string    `json:"reason,omitempty"`
	CostUSD      float64   `json:"costUsd,omitempty"`
	StartedAt    time.Time `json:"startedAt"`
	EndedAt      time.Time `json:"endedAt,omitzero"`
	Clipped      bool      `json:"clipped,omitempty"`
	Auto         bool      `json:"auto,omitempty"`
	PluginCommit string    `json:"pluginCommit,omitempty"`
	ClaudeCode   string    `json:"claudeCode,omitempty"`
	Models       []string  `json:"models,omitempty"`
	Turns        int       `json:"turns,omitempty"`
	Denied       []string  `json:"denied,omitempty"`
	Failure      string    `json:"failure,omitempty"`
	Attempt      int       `json:"attempt,omitempty"`
}

type reviewsJSON struct {
	Version int          `json:"version"`
	Reviews []reviewJSON `json:"reviews"`
}

func (s Reviews) Load() ([]session.ReviewEntry, error) {
	data, err := read(s.Dir, reviewsFile)
	if err != nil || data == nil {
		return []session.ReviewEntry{}, err
	}
	var f reviewsJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 {
		return nil, errors.New("reviews.json isn't a review record this engine reads")
	}
	out := make([]session.ReviewEntry, 0, len(f.Reviews))
	for _, r := range f.Reviews {
		out = append(out, session.ReviewEntry{
			Key:  review.Key{Commit: r.Commit, Branch: r.Branch, Agent: r.Agent},
			Role: review.Role(r.Role),
			Record: review.Record{Provider: r.Provider, State: review.State(r.State), Verdict: r.Verdict,
				Channel: review.Channel(r.Channel), Override: r.Override},
			Base: r.Base, Since: r.Since, Covers: r.Covers, Message: r.Message, Reason: r.Reason, CostUSD: max(r.CostUSD, 0), StartedAt: r.StartedAt, EndedAt: r.EndedAt,
			Clipped: r.Clipped, Auto: r.Auto, PluginCommit: matching(r.PluginCommit, commitID), ClaudeCode: matching(r.ClaudeCode, versionText),
			Models: matchingAll(r.Models, modelName, maxModels), Turns: inRange(r.Turns, 0, maxTurns), Denied: matchingAll(r.Denied, toolName, maxDenied),
			Failure: matching(r.Failure, failureKind), Attempt: max(inRange(r.Attempt, 1, maxAttempt), 1),
		})
	}
	return out, nil
}

func (s Reviews) Save(list []session.ReviewEntry) error {
	f := reviewsJSON{Version: 1, Reviews: make([]reviewJSON, 0, len(list))}
	for _, e := range list {
		f.Reviews = append(f.Reviews, reviewJSON{
			Commit: e.Key.Commit, Branch: e.Key.Branch, Agent: e.Key.Agent, Role: string(e.Role),
			Provider: e.Record.Provider, State: string(e.Record.State), Verdict: e.Record.Verdict,
			Channel: string(e.Record.Channel), Override: e.Record.Override,
			Base: e.Base, Since: e.Since, Covers: e.Covers, Message: e.Message, Reason: e.Reason, CostUSD: e.CostUSD, StartedAt: e.StartedAt.UTC(), EndedAt: e.EndedAt.UTC(),
			Clipped: e.Clipped, Auto: e.Auto, PluginCommit: e.PluginCommit, ClaudeCode: e.ClaudeCode, Models: e.Models, Turns: e.Turns, Denied: e.Denied,
			Failure: e.Failure, Attempt: laterAttempt(e.Attempt),
		})
	}
	for {
		data, err := encode(f)
		if err != nil {
			return err
		}
		if len(data) <= maxFile || len(f.Reviews) == 0 {
			return writeBytes(s.Dir, reviewsFile, data)
		}
		drop := max(1, len(f.Reviews)*(len(data)-maxFile)/len(data)+1)
		f.Reviews = f.Reviews[min(drop, len(f.Reviews)):]
	}
}

func laterAttempt(n int) int {
	if n > 1 {
		return n
	}
	return 0
}
