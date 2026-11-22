package page

import (
	"context"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

const (
	lessonsRoute     = "/api/lessons"
	reviewDepthRoute = "/api/review-depth"
	maxDepthAgent    = 120
	maxProjectName   = 400
	depthAlways      = "always"
	depthBig         = "big"
	depthWarn        = "warn"
	depthModeRule    = "Needs always, big or warn for each reviewer."
	depthTwiceRule   = "Needs true or false."
)

const (
	saidDepthSaved     = "Saved. The next review uses them."
	saidDepthUnchanged = "No change: these review settings are already saved."
)

var (
	lessonRoutes = map[string]bool{lessonsRoute: true, reviewDepthRoute: true}
	lessonLineID = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

func (s *Server) UseLessons(l session.LessonsPage) {
	s.mu.Lock()
	s.lessons = l
	s.mu.Unlock()
}

func (s *Server) UseReviewDepth(d session.ReviewDepthPage) {
	s.mu.Lock()
	s.depth = d
	s.mu.Unlock()
}

func (s *Server) lessonsAPI(w http.ResponseWriter, r *http.Request, body map[string]any) {
	op, okOp := body["op"].(string)
	if !okOp {
		refuse(w, http.StatusBadRequest, "op", saidOp)
		return
	}
	s.mu.Lock()
	lessons, depth := s.lessons, s.depth
	s.mu.Unlock()
	switch r.URL.Path {
	case lessonsRoute:
		if lessons == nil {
			send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine runs no reviewer containers, so it suggests no instruction lines."})
			return
		}
		s.lessonsOp(w, r, lessons, op, body)
	case reviewDepthRoute:
		if depth == nil {
			send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine keeps no project review settings."})
			return
		}
		s.depthOp(w, depth, op, body)
	default:
		refuse(w, http.StatusNotFound, "route", saidRoute)
	}
}

type lessonFindingJSON struct {
	ID      string `json:"id"`
	Agent   string `json:"agent"`
	Path    string `json:"path"`
	Title   string `json:"title"`
	Commits int    `json:"commits"`
	Offered bool   `json:"offered"`
}

type lessonLineJSON struct {
	ID      string   `json:"id"`
	Text    string   `json:"text"`
	From    []string `json:"from"`
	State   string   `json:"state"`
	Session string   `json:"session,omitempty"`
	At      int64    `json:"at"`
}

type lessonRunJSON struct {
	State   string   `json:"state"`
	Reason  string   `json:"reason,omitempty"`
	CostUSD float64  `json:"costUsd,omitempty"`
	At      int64    `json:"at"`
	Given   int      `json:"given"`
	Notes   []string `json:"notes"`
}

type lessonSessionJSON struct {
	SID     string `json:"sid"`
	Name    string `json:"name"`
	Running bool   `json:"running"`
}

type lessonsJSON struct {
	Project  string              `json:"project"`
	After    int                 `json:"after"`
	Findings []lessonFindingJSON `json:"findings"`
	Lines    []lessonLineJSON    `json:"lines"`
	Run      *lessonRunJSON      `json:"run,omitempty"`
	Sessions []lessonSessionJSON `json:"sessions"`
	Offered  int                 `json:"offered"`
}

func lessonsOf(v session.LessonsView) lessonsJSON {
	out := lessonsJSON{Project: v.Project, After: v.After, Findings: []lessonFindingJSON{}, Lines: []lessonLineJSON{}, Sessions: []lessonSessionJSON{}, Offered: len(v.Offered)}
	offered := map[string]bool{}
	for _, id := range v.Offered {
		offered[id] = true
	}
	for _, f := range v.Findings {
		out.Findings = append(out.Findings, lessonFindingJSON{ID: f.ID, Agent: f.Agent, Path: f.Path, Title: f.Title, Commits: len(f.Commits), Offered: offered[f.ID]})
	}
	for _, x := range v.Lines {
		out.Lines = append(out.Lines, lessonLineJSON{ID: x.ID, Text: x.Text, From: nonNil(x.From), State: x.State, Session: x.Session, At: millis(x.At)})
	}
	if v.Run != nil {
		out.Run = &lessonRunJSON{State: v.Run.State, Reason: v.Run.Reason, CostUSD: v.Run.CostUSD, At: millis(v.Run.At), Given: v.Run.Given, Notes: nonNil(v.Run.Notes)}
	}
	for _, x := range v.Sessions {
		out.Sessions = append(out.Sessions, lessonSessionJSON{SID: x.SID, Name: x.Name, Running: x.Running})
	}
	return out
}

func (s *Server) lessonsOp(w http.ResponseWriter, r *http.Request, lessons session.LessonsPage, op string, body map[string]any) {
	name, okName := body["name"].(string)
	if !okName || name == "" || len(name) > maxProjectName {
		refuseFields(w)
		return
	}
	id, okID := body["id"].(string)
	okID = okID && lessonLineID.MatchString(id)
	var err error
	switch op {
	case "read":
		if !keysWithin(body, "op", "name") {
			refuseFields(w)
			return
		}
	case "suggest":
		consent, consented := consentOf(body)
		if !keysWithin(body, "op", "name", "costOk") || !consented {
			refuseFields(w)
			return
		}
		if !s.knownProject(name) {
			send(w, http.StatusNotFound, problem{Error: "project", Message: "The engine has no project of that name."})
			return
		}
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(manageFor + writeFor))
		ctx, cancel := context.WithTimeout(r.Context(), manageFor)
		defer cancel()
		err = lessons.Suggest(ctx, name, consent.CostOK)
		shared.For(r.Context(), s.logger).Info("lessons", "op", op, "ok", err == nil)
		reply(w, err, http.StatusAccepted)
		return
	case "approve":
		sid, okSID := viewSession(body)
		okSID = okSID || body["sid"] == ""
		if !keysWithin(body, "op", "name", "id", "sid") || !okID || !okSID {
			refuseFields(w)
			return
		}
		err = lessons.Approve(name, id, sid)
	case "decline":
		if !keysWithin(body, "op", "name", "id") || !okID {
			refuseFields(w)
			return
		}
		err = lessons.Decline(name, id)
	default:
		send(w, http.StatusBadRequest, problem{Error: "op", Message: "The engine reads, suggests, approves and declines instruction lines only."})
		return
	}
	if op != "read" {
		shared.For(r.Context(), s.logger).Info("lessons", "op", op, "ok", err == nil)
	}
	if err != nil {
		code, p := status(err)
		send(w, code, p)
		return
	}
	if !s.knownProject(name) {
		send(w, http.StatusNotFound, problem{Error: "project", Message: "The engine has no project of that name."})
		return
	}
	v, err := lessons.Lessons(name)
	if err != nil {
		code, p := status(err)
		send(w, code, p)
		return
	}
	send(w, http.StatusOK, lessonsOf(v))
}

type depthJSON struct {
	savedSaid
	Reviewers    map[string]string `json:"reviewers"`
	MaxLines     int               `json:"maxLines"`
	Twice        bool              `json:"twice"`
	Type         string            `json:"type"`
	RunsTotal    int               `json:"runsTotal"`
	AboveRuns    int               `json:"aboveRuns"`
	AdvisoryRuns int               `json:"advisoryRuns"`
	Available    []string          `json:"available"`
	Required     []string          `json:"required"`
}

func depthOfReviewers(v any) (workflow.Depth, string, bool) {
	raw, ok := v.(map[string]any)
	if !ok {
		return workflow.Depth{}, depthModeRule, false
	}
	if len(raw) > workflow.MaxAdded {
		return workflow.Depth{}, workflow.DepthCountRule, false
	}
	var d workflow.Depth
	for _, a := range slices.Sorted(maps.Keys(raw)) {
		if len(a) > maxDepthAgent || !workflow.AgentName(a) {
			return workflow.Depth{}, workflow.DepthNameRule, false
		}
		switch raw[a] {
		case depthAlways:
			d.Always = append(d.Always, a)
		case depthBig:
			d.Extra = append(d.Extra, a)
		case depthWarn:
			d.Advisory = append(d.Advisory, a)
		default:
			return workflow.Depth{}, depthModeRule, false
		}
	}
	return d, "", true
}

func reviewersOf(d workflow.Depth) map[string]string {
	out := map[string]string{}
	for _, list := range []struct {
		mode   string
		agents []string
	}{{depthAlways, d.Always}, {depthBig, d.Extra}, {depthWarn, d.Advisory}} {
		for _, a := range list.agents {
			out[a] = list.mode
		}
	}
	return out
}

func typedCount(v any) (int64, bool) {
	typed, ok := v.(string)
	if !ok {
		return integer(v)
	}
	typed = strings.TrimSpace(typed)
	if typed == "" {
		return 0, true
	}
	n, err := strconv.ParseInt(typed, 10, 64)
	return n, err == nil
}

func refuseDepth(w http.ResponseWriter, field, message string) {
	send(w, http.StatusBadRequest, problem{Error: "depth", Field: field, Message: message})
}

func (s *Server) depthOp(w http.ResponseWriter, depth session.ReviewDepthPage, op string, body map[string]any) {
	path, okPath := body["path"].(string)
	if !okPath || path == "" || len(path) > session.MaxCwd {
		refuseFields(w)
		return
	}
	var said savedSaid
	switch op {
	case "read":
		if !keysWithin(body, "op", "path") {
			refuseFields(w)
			return
		}
	case "set":
		if !keysWithin(body, "op", "path", "reviewers", "maxLines", "twice") {
			refuseFields(w)
			return
		}
		d, why, okReviewers := depthOfReviewers(body["reviewers"])
		lines, okLines := typedCount(body["maxLines"])
		twice, okTwice := body["twice"].(bool)
		switch {
		case !okReviewers:
			refuseDepth(w, "reviewers", why)
			return
		case !okLines || lines < 0 || lines > workflow.MaxDepthSize:
			refuseDepth(w, "maxLines", workflow.DepthSizeRule)
			return
		case !okTwice:
			refuseDepth(w, "twice", depthTwiceRule)
			return
		}
		d.MaxLines, d.Twice = int(lines), twice
		changed, err := depth.SetReviewDepth(path, d)
		s.logger.Info("project setting", "op", "review-depth", "ok", err == nil, "changed", changed)
		if err != nil {
			code, p := status(err)
			send(w, code, p)
			return
		}
		said = saveSaid(changed, saidDepthSaved, saidDepthUnchanged)
	default:
		send(w, http.StatusBadRequest, problem{Error: "op", Message: "The engine reads and sets a project's review settings only."})
		return
	}
	v, err := depth.ReviewDepth(path)
	if err != nil {
		code, p := status(err)
		send(w, code, p)
		return
	}
	send(w, http.StatusOK, depthJSON{savedSaid: said, Reviewers: reviewersOf(v.Depth), MaxLines: v.Depth.MaxLines, Twice: v.Depth.Twice, Type: v.Type, RunsTotal: v.Runs, AboveRuns: v.AboveRuns,
		AdvisoryRuns: v.Advisory, Available: nonNil(v.Available), Required: nonNil(v.Required)})
}
