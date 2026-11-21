package page

import (
	"context"
	"math"
	"net/http"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	pullRoute          = "/api/pull-request"
	issuesRoute        = "/api/issues"
	findingIssuesRoute = "/api/finding-issues"
	pullFor            = 60 * time.Second
)

var pullRoutes = map[string]bool{pullRoute: true, issuesRoute: true, findingIssuesRoute: true}

type pullCheckJSON struct {
	Name   string `json:"name"`
	Result string `json:"result"`
	URL    string `json:"url"`
	Failed bool   `json:"failed"`
}

type pullCommentJSON struct {
	Kind     string `json:"kind"`
	Author   string `json:"author"`
	Body     string `json:"body"`
	Created  int64  `json:"created"`
	URL      string `json:"url"`
	State    string `json:"state,omitempty"`
	Path     string `json:"path,omitempty"`
	Line     int    `json:"line,omitempty"`
	Outdated bool   `json:"outdated,omitempty"`
}

type pullFollowJSON struct {
	Number     int               `json:"number"`
	URL        string            `json:"url"`
	State      string            `json:"state"`
	Draft      bool              `json:"draft"`
	Opened     bool              `json:"opened"`
	Head       string            `json:"head"`
	Conflict   bool              `json:"conflict"`
	Mergeable  string            `json:"mergeable"`
	MergeState string            `json:"mergeState"`
	Checks     []pullCheckJSON   `json:"checks"`
	Failing    int               `json:"failing"`
	Passing    int               `json:"passing"`
	Running    int               `json:"running"`
	ChecksNew  bool              `json:"checksNew"`
	Comments   []pullCommentJSON `json:"comments"`
	More       int               `json:"more"`
	Dropped    int               `json:"dropped"`
	Rounds     int               `json:"rounds"`
	Auto       bool              `json:"auto"`
	AutoStop   string            `json:"autoStop"`
	Polled     int64             `json:"polled"`
	Note       string            `json:"note"`
	Issue      int               `json:"issue"`
	IssueSent  bool              `json:"issueSent"`
	Sent       int64             `json:"sent"`
	Done       bool              `json:"done"`
}

func pullFollowOf(v *session.FollowView) *pullFollowJSON {
	if v == nil {
		return nil
	}
	out := &pullFollowJSON{Number: v.Number, URL: v.URL, State: v.State, Draft: v.Draft, Opened: v.Opened, Head: v.Head, Conflict: v.Conflict, Mergeable: v.Mergeable,
		MergeState: v.MergeState, Checks: make([]pullCheckJSON, 0, len(v.Checks)), Failing: v.Failing, Passing: v.Passing, Running: v.Running, ChecksNew: v.ChecksNew,
		Comments: make([]pullCommentJSON, 0, len(v.Comments)), More: v.More, Dropped: v.Dropped, Rounds: v.Rounds, Auto: v.Auto, AutoStop: v.AutoStop,
		Polled: millis(v.Polled), Note: v.Note, Issue: v.Issue, IssueSent: v.IssueSent, Sent: millis(v.Sent), Done: v.Done}
	for _, c := range v.Checks {
		out.Checks = append(out.Checks, pullCheckJSON{Name: c.Name, Result: c.Result, URL: c.URL, Failed: c.Failed})
	}
	for _, c := range v.Comments {
		out.Comments = append(out.Comments, pullCommentJSON{Kind: c.Kind, Author: c.Author, Body: c.Body, Created: millis(c.Created), URL: c.URL, State: c.State, Path: c.Path, Line: c.Line, Outdated: c.Outdated})
	}
	return out
}

func (s *Server) UsePulls(pulls session.PullsPage, issues session.IssuesPage, starter session.Starter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pulls, s.issues = pulls, issues
	if starter != nil {
		s.cfg.Starter = starter
	}
}

func (s *Server) pullParts() (session.PullsPage, session.IssuesPage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pulls, s.issues
}

func (s *Server) pullAPI(w http.ResponseWriter, r *http.Request, body map[string]any) {
	pulls, issues := s.pullParts()
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(pullFor + writeFor))
	ctx, cancel := context.WithTimeout(r.Context(), pullFor)
	defer cancel()
	switch r.URL.Path {
	case pullRoute:
		if pulls == nil {
			send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine has no git host API set up, so it follows no pull requests."})
			return
		}
		s.pullRequest(ctx, w, pulls, body)
	case issuesRoute:
		if issues == nil {
			send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine has no git host API set up, so it reads no issues."})
			return
		}
		s.issueList(ctx, w, issues, body)
	case findingIssuesRoute:
		if pulls == nil {
			send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine has no git host API set up, so it opens no issues."})
			return
		}
		s.findingIssues(ctx, w, pulls, body)
	default:
		refuse(w, http.StatusNotFound, "route")
	}
}

func wholeNumber(v any, low, high int) (int, bool) {
	f, ok := v.(float64)
	if !ok || f != math.Trunc(f) || f < float64(low) || f > float64(high) {
		return 0, false
	}
	return int(f), true
}

func (s *Server) pullRequest(ctx context.Context, w http.ResponseWriter, pulls session.PullsPage, body map[string]any) {
	op, okOp := body["op"].(string)
	sid, okSID := viewSession(body)
	if !okOp || !okSID {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	switch op {
	case "send", "open", "ready", "settings":
		if !keysWithin(body, "op", "sid") {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
	case "set":
		if !keysWithin(body, "op", "sid", "bots", "auto", "assign") {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
	default:
		send(w, http.StatusBadRequest, problem{Error: "op", Message: "The engine sends what's new on a pull request, opens one, marks its own ready for review, and reads or saves the project's pull request settings, nothing else."})
		return
	}
	var err error
	switch op {
	case "send":
		err = pulls.Send(sid)
		s.logger.Info("pull follow send", "ok", err == nil)
	case "open":
		err = pulls.OpenPull(ctx, sid)
		s.logger.Info("pull open", "ok", err == nil)
	case "ready":
		err = pulls.Ready(ctx, sid)
		s.logger.Info("pull ready", "ok", err == nil)
	case "settings":
		project, set, err := pulls.Settings(sid)
		if err != nil {
			code, p := status(err)
			send(w, code, p)
			return
		}
		send(w, http.StatusOK, map[string]any{"project": project, "bots": nonNil(set.Bots), "auto": set.Auto, "assign": set.Assign})
		return
	case "set":
		list, okList := body["bots"].([]any)
		auto, okAuto := body["auto"].(bool)
		assign, okAssign := body["assign"].(bool)
		if !okList || !okAuto || !okAssign {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		bots := make([]string, 0, len(list))
		for _, v := range list {
			b, ok := v.(string)
			if !ok {
				refuse(w, http.StatusBadRequest, "fields")
				return
			}
			bots = append(bots, b)
		}
		err = pulls.SetSettings(sid, session.FollowSettings{Bots: bots, Auto: auto, Assign: assign})
		s.logger.Info("pull settings", "ok", err == nil)
	}
	reply(w, err, http.StatusOK)
}

type issueJSON struct {
	Number  int      `json:"number"`
	Title   string   `json:"title"`
	Body    string   `json:"body,omitempty"`
	URL     string   `json:"url"`
	Author  string   `json:"author"`
	Labels  []string `json:"labels"`
	Created int64    `json:"created"`
	Open    bool     `json:"open"`
}

func issueOf(i session.IssueFound) issueJSON {
	return issueJSON{Number: i.Number, Title: i.Title, Body: i.Body, URL: i.URL, Author: i.Author, Labels: nonNil(i.Labels), Created: millis(i.Created), Open: i.Open}
}

func (s *Server) issueList(ctx context.Context, w http.ResponseWriter, issues session.IssuesPage, body map[string]any) {
	op, okOp := body["op"].(string)
	path, okPath := body["path"].(string)
	if !okOp || !okPath || path == "" || len(path) > session.MaxCwd {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	switch op {
	case "list":
		if !keysWithin(body, "op", "path", "label", "page") {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		label := ""
		if v, has := body["label"]; has {
			l, ok := v.(string)
			if !ok {
				refuse(w, http.StatusBadRequest, "fields")
				return
			}
			label = l
		}
		page := 1
		if v, has := body["page"]; has {
			n, ok := wholeNumber(v, 1, session.MaxIssuePage)
			if !ok {
				refuse(w, http.StatusBadRequest, "fields")
				return
			}
			page = n
		}
		v, err := issues.List(ctx, path, label, page)
		if err != nil {
			code, p := status(err)
			send(w, code, p)
			return
		}
		list := make([]issueJSON, 0, len(v.Issues))
		for _, i := range v.Issues {
			list = append(list, issueOf(i))
		}
		send(w, http.StatusOK, map[string]any{"repo": v.Repo, "label": v.Label, "page": v.Page, "more": v.More, "issues": list})
	case "read":
		number, okNumber := wholeNumber(body["number"], 1, session.MaxIssueNumber)
		if !keysWithin(body, "op", "path", "number") || !okNumber {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		v, err := issues.Read(ctx, path, number)
		if err != nil {
			code, p := status(err)
			send(w, code, p)
			return
		}
		send(w, http.StatusOK, map[string]any{"repo": v.Repo, "refused": v.Refused, "clipped": v.Clipped, "hash": v.Hash, "issue": issueOf(v.Issue)})
	default:
		send(w, http.StatusBadRequest, problem{Error: "op", Message: "The engine lists a project's open issues and reads one, nothing else."})
	}
}

type findingJSON struct {
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Location string `json:"location"`
	Text     string `json:"text"`
	Reviewer string `json:"reviewer"`
	Agent    string `json:"agent"`
}

func (s *Server) findingIssues(ctx context.Context, w http.ResponseWriter, pulls session.PullsPage, body map[string]any) {
	op, okOp := body["op"].(string)
	sid, okSID := viewSession(body)
	if !okOp || !okSID {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	switch op {
	case "read":
		if !keysWithin(body, "op", "sid") {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		v, err := pulls.Findings(sid)
		if err != nil {
			code, p := status(err)
			send(w, code, p)
			return
		}
		items := make([]findingJSON, 0, len(v.Items))
		for _, it := range v.Items {
			items = append(items, findingJSON{Severity: it.Severity, Title: it.Title, Location: it.Location, Text: it.Text, Reviewer: it.Reviewer, Agent: it.Agent})
		}
		send(w, http.StatusOK, map[string]any{"commit": v.Commit, "branch": v.Branch, "held": v.Held, "opened": v.Opened, "items": items})
	case "open":
		commit, okCommit := body["commit"].(string)
		if !keysWithin(body, "op", "sid", "commit") || !okCommit || !commitHex.MatchString(commit) {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		made, err := pulls.OpenFindings(ctx, sid, commit)
		s.logger.Info("finding issues", "made", made, "ok", err == nil)
		if err != nil {
			code, p := status(err)
			send(w, code, p)
			return
		}
		send(w, http.StatusOK, map[string]any{"ok": true, "made": made})
	default:
		send(w, http.StatusBadRequest, problem{Error: "op", Message: "The engine lists a passing review's findings that didn't block and offers an issue card for each, nothing else."})
	}
}
