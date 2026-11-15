package page

import (
	"context"
	"net/http"
	"regexp"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	allowRulesRoute  = "/api/allow-rules"
	agentReportRoute = "/api/agent-report"
	maxRulesRepo     = 100
	rulesFor         = 40 * time.Second
)

var rulesRepoName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

type ruleJSON struct {
	Box      string   `json:"box"`
	Rule     string   `json:"rule"`
	Place    string   `json:"place"`
	Worktree string   `json:"worktree,omitempty"`
	Path     string   `json:"path"`
	Wide     []string `json:"wide"`
}

type ruleFileJSON struct {
	Box      string `json:"box"`
	Place    string `json:"place"`
	Worktree string `json:"worktree,omitempty"`
	Path     string `json:"path"`
	Count    int    `json:"count"`
	Problem  string `json:"problem,omitempty"`
}

type allowRulesJSON struct {
	Repo  string         `json:"repo"`
	Idle  bool           `json:"idle"`
	Files []ruleFileJSON `json:"files"`
	Rules []ruleJSON     `json:"rules"`
}

func (s *Server) UseAgentReports(reports session.AgentReportPage, rules session.AllowRulesPage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reports, s.rules = reports, rules
}

func allowRulesOf(v session.AllowRulesView) allowRulesJSON {
	out := allowRulesJSON{Repo: v.Repo, Idle: v.Idle, Files: []ruleFileJSON{}, Rules: []ruleJSON{}}
	for _, f := range v.Files {
		out.Files = append(out.Files, ruleFileJSON{Box: f.Box, Place: string(f.Place), Worktree: f.Worktree, Path: f.Path, Count: len(f.Rules), Problem: f.Problem})
	}
	for _, r := range v.Rules {
		out.Rules = append(out.Rules, ruleJSON{Box: r.Box, Rule: r.Rule, Place: string(r.Place), Worktree: r.Worktree, Path: r.Path, Wide: nonNil(r.Wide)})
	}
	return out
}

func (s *Server) allowRules(w http.ResponseWriter, r *http.Request, body map[string]any) {
	s.mu.Lock()
	rules := s.rules
	s.mu.Unlock()
	if rules == nil {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine has no agent-host, so it lists no saved rules."})
		return
	}
	op, okOp := body["op"].(string)
	repo, okRepo := body["repo"].(string)
	if !okOp || !okRepo || len(repo) > maxRulesRepo || !rulesRepoName.MatchString(repo) {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), rulesFor)
	defer cancel()
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(rulesFor + writeFor))
	switch op {
	case "list":
		if !keysWithin(body, "op", "repo") {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
	case "remove":
		place, okPlace := body["place"].(string)
		rule, okRule := body["rule"].(string)
		box, okBox := body["box"].(string)
		worktree := ""
		if v, has := body["worktree"]; has {
			t, ok := v.(string)
			if !ok {
				refuse(w, http.StatusBadRequest, "fields")
				return
			}
			worktree = t
		}
		if !keysWithin(body, "op", "repo", "box", "place", "worktree", "rule") || !okPlace || !okRule || !okBox || !session.ValidBox(box) || !session.ValidRuleText(rule) || len(worktree) > 64 {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		switch session.RulePlace(place) {
		case session.RuleInClone, session.RuleInUser, session.RuleInWorktree:
		default:
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		err := rules.RemoveAllowRule(ctx, repo, box, session.RulePlace(place), worktree, rule)
		shared.For(r.Context(), s.logger).Info("allow rule remove", "ok", err == nil, "place", place)
		if err != nil {
			code, p := status(err)
			send(w, code, p)
			return
		}
	default:
		send(w, http.StatusBadRequest, problem{Error: "op", Message: "The engine lists the saved allow rules and removes one when you press Remove, nothing else."})
		return
	}
	v, err := rules.AllowRules(ctx, repo)
	if err != nil {
		code, p := status(err)
		send(w, code, p)
		return
	}
	send(w, http.StatusOK, allowRulesOf(v))
}

type homeReportJSON struct {
	Problem  string         `json:"problem,omitempty"`
	Sessions []homeViewJSON `json:"sessions"`
}

type homeViewJSON struct {
	SID      string   `json:"sid"`
	Session  string   `json:"session"`
	Checked  int64    `json:"checkedAt"`
	Since    int64    `json:"sinceAt"`
	Changed  []string `json:"changed"`
	More     int      `json:"more"`
	First    bool     `json:"first"`
	Dropped  bool     `json:"dropped"`
	Running  bool     `json:"running"`
	Problem  string   `json:"problem,omitempty"`
	Watching int      `json:"watching"`
}

type pinJSON struct {
	Agent      string `json:"agent"`
	Name       string `json:"name"`
	Package    string `json:"package"`
	Version    string `json:"version"`
	State      string `json:"state"`
	Repository string `json:"repository,omitempty"`
	Workflow   string `json:"workflow,omitempty"`
	Ref        string `json:"ref,omitempty"`
	Commit     string `json:"commit,omitempty"`
	LoggedAt   int64  `json:"loggedAt"`
	Note       string `json:"note,omitempty"`
}

type pluginPinJSON struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

type advisoryJSON struct {
	Label   string   `json:"label"`
	ID      string   `json:"id"`
	Summary string   `json:"summary"`
	Aliases []string `json:"aliases"`
	Link    string   `json:"link"`
}

type advisoriesJSON struct {
	Checked int64          `json:"checkedAt"`
	Off     bool           `json:"off"`
	Problem string         `json:"problem,omitempty"`
	Found   []advisoryJSON `json:"found"`
	More    int            `json:"more"`
	Sent    []string       `json:"sent"`
}

type agentReportJSON struct {
	Settings   homeReportJSON  `json:"settings"`
	Pins       []pinJSON       `json:"pins"`
	Plugins    []pluginPinJSON `json:"plugins"`
	Advisories advisoriesJSON  `json:"advisories"`
}

func agentReportOf(v session.AgentReport) agentReportJSON {
	out := agentReportJSON{
		Settings: homeReportJSON{Problem: v.Home.Problem, Sessions: []homeViewJSON{}},
		Pins:     []pinJSON{}, Plugins: []pluginPinJSON{},
		Advisories: advisoriesJSON{Checked: millis(v.Advisories.Checked), Off: v.Advisories.Off, Problem: v.Advisories.Problem, Found: []advisoryJSON{}, More: v.Advisories.More, Sent: nonNil(v.Advisories.Sent)},
	}
	for _, h := range v.Home.Sessions {
		out.Settings.Sessions = append(out.Settings.Sessions, homeViewJSON{SID: h.SID, Session: h.Session, Checked: millis(h.Checked), Since: millis(h.Since), Changed: nonNil(h.Changed), More: h.More, First: h.First, Dropped: h.Dropped, Running: h.Running, Problem: h.Problem, Watching: h.Watching})
	}
	for _, p := range v.Pins {
		out.Pins = append(out.Pins, pinJSON{Agent: p.Agent, Name: p.Name, Package: p.Package, Version: p.Version, State: p.State, Repository: p.Repository, Workflow: p.Workflow, Ref: p.Ref, Commit: p.Commit, LoggedAt: millis(p.LoggedAt), Note: p.Note})
	}
	for _, p := range v.Plugins {
		out.Plugins = append(out.Plugins, pluginPinJSON{ID: p.ID, Name: p.Name, Version: p.Version, Commit: p.Commit})
	}
	for _, a := range v.Advisories.Found {
		out.Advisories.Found = append(out.Advisories.Found, advisoryJSON{Label: a.Label, ID: a.ID, Summary: a.Summary, Aliases: nonNil(a.Aliases), Link: a.Link})
	}
	return out
}

func (s *Server) agentReport(w http.ResponseWriter, body map[string]any) {
	s.mu.Lock()
	reports := s.reports
	s.mu.Unlock()
	if op, ok := body["op"].(string); !ok || op != "read" || !keysWithin(body, "op") {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	if reports == nil {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine keeps no report on the agents."})
		return
	}
	send(w, http.StatusOK, agentReportOf(reports.AgentReport()))
}
