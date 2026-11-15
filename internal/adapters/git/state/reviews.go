package state

import (
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/review"
	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	reviewsFile    = "reviews.json"
	reviewsVersion = 2
)

type Reviews struct {
	Dir string
}

var _ session.ReviewRecords = Reviews{}

var approvalDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

var citedID = regexp.MustCompile(`^(CVE-\d{4}-\d{4,7}|GHSA(-[23456789cfghjmpqrvwx]{4}){3})$`)

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
	Shaped       string    `json:"shaped,omitempty"`
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
	RunModel     string    `json:"runModel,omitempty"`
	RunEffort    string    `json:"runEffort,omitempty"`
	Turns        int       `json:"turns,omitempty"`
	Denied       []string  `json:"denied,omitempty"`
	Failure      string    `json:"failure,omitempty"`
	Attempt      int       `json:"attempt,omitempty"`
	AutoAttempts int       `json:"autoAttempts,omitempty"`
	Run          int       `json:"run,omitempty"`
	Advisory     bool      `json:"advisory,omitempty"`
	Cited        []string  `json:"cited,omitempty"`
	NotInOSV     []string  `json:"notInOsv,omitempty"`
	CitedMore    int       `json:"citedMore,omitempty"`
	CitedFailed  string    `json:"citedFailed,omitempty"`
	Approved     string    `json:"approved,omitempty"`
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
	if err := json.Unmarshal(data, &f); err != nil || f.Version < 1 || f.Version > reviewsVersion {
		return nil, errors.New("reviews.json isn't a review record this engine reads")
	}
	out := make([]session.ReviewEntry, 0, len(f.Reviews))
	for _, r := range f.Reviews {
		if !session.ValidReviewChoice(r.RunModel, r.RunEffort) {
			continue
		}
		if f.Version == 1 {
			r.Run, r.Advisory = 0, false
		}
		if r.Run < 0 || r.Run > review.MaxRuns {
			continue
		}
		out = append(out, session.ReviewEntry{
			Key:  review.Key{Commit: r.Commit, Branch: r.Branch, Agent: r.Agent}.AtRun(r.Run),
			Role: review.Role(r.Role),
			Record: review.Record{Provider: r.Provider, State: review.State(r.State), Verdict: r.Verdict,
				Channel: review.Channel(r.Channel), Override: r.Override, Shaped: r.Shaped, Advisory: r.Advisory},
			Cited: citedOf(r),
			Base:  r.Base, Since: r.Since, Covers: r.Covers, Message: r.Message, Reason: r.Reason, CostUSD: max(r.CostUSD, 0), StartedAt: r.StartedAt, EndedAt: r.EndedAt,
			Clipped: r.Clipped, Auto: r.Auto, PluginCommit: matching(r.PluginCommit, commitID), ClaudeCode: matching(r.ClaudeCode, versionText),
			Models: matchingAll(r.Models, modelName, maxModels), RunModel: r.RunModel, RunEffort: r.RunEffort, Turns: inRange(r.Turns, 0, maxTurns), Denied: matchingAll(r.Denied, toolName, maxDenied),
			Failure: matching(r.Failure, failureKind), Attempt: max(inRange(r.Attempt, 1, maxAttempt), 1), AutoAttempts: inRange(r.AutoAttempts, 0, maxAttempt),
			Approved: matching(r.Approved, approvalDigest),
		})
	}
	return out, nil
}

func (s Reviews) Save(list []session.ReviewEntry) error {
	f := reviewsJSON{Version: reviewsVersion, Reviews: make([]reviewJSON, 0, len(list))}
	for _, e := range list {
		run := 0
		if e.Key.Number() > 1 {
			run = e.Key.Number()
		}
		f.Reviews = append(f.Reviews, reviewJSON{
			Run: run, Advisory: e.Record.Advisory, Cited: e.Cited.Looked, NotInOSV: e.Cited.Missing, CitedMore: e.Cited.More, CitedFailed: e.Cited.Failed,
			Commit: e.Key.Commit, Branch: e.Key.Branch, Agent: e.Key.Agent, Role: string(e.Role),
			Provider: e.Record.Provider, State: string(e.Record.State), Verdict: e.Record.Verdict,
			Channel: string(e.Record.Channel), Override: e.Record.Override, Shaped: e.Record.Shaped,
			Base: e.Base, Since: e.Since, Covers: e.Covers, Message: e.Message, Reason: e.Reason, CostUSD: e.CostUSD, StartedAt: e.StartedAt.UTC(), EndedAt: e.EndedAt.UTC(),
			Clipped: e.Clipped, Auto: e.Auto, PluginCommit: e.PluginCommit, ClaudeCode: e.ClaudeCode, Models: e.Models, RunModel: e.RunModel, RunEffort: e.RunEffort, Turns: e.Turns, Denied: e.Denied,
			Failure: e.Failure, Attempt: laterAttempt(e.Attempt), AutoAttempts: e.AutoAttempts, Approved: e.Approved,
		})
	}
	gone := reviewsGone(s.Dir, list)
	for {
		data, err := encode(f)
		if err != nil {
			return err
		}
		if len(data) <= maxFile || len(f.Reviews) == 0 {
			noteDropped(s.Dir, reviewsFile, gone+len(list)-len(f.Reviews))
			return writeBytes(s.Dir, reviewsFile, data)
		}
		drop := max(1, len(f.Reviews)*(len(data)-maxFile)/len(data)+1)
		f.Reviews = f.Reviews[min(drop, len(f.Reviews)):]
	}
}

func citedOf(r reviewJSON) session.CitedIDs {
	out := session.CitedIDs{Looked: matchingAll(r.Cited, citedID, session.MaxCitedIDs), More: inRange(r.CitedMore, 0, 10000)}
	for _, id := range matchingAll(r.NotInOSV, citedID, session.MaxCitedIDs) {
		if slices.Contains(out.Looked, id) {
			out.Missing = append(out.Missing, id)
		}
	}
	if cleanText(r.CitedFailed, 200) {
		out.Failed = r.CitedFailed
	}
	if len(out.Looked) == 0 {
		return session.CitedIDs{}
	}
	return out
}

func laterAttempt(n int) int {
	if n > 1 {
		return n
	}
	return 0
}
