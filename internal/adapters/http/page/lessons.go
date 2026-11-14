package page

import (
	"context"
	"net/http"
	"regexp"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/review"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

const (
	lessonsRoute     = "/api/lessons"
	reviewDepthRoute = "/api/review-depth"
	maxDepthAgent    = 120
	maxProjectName   = 400
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
		refuse(w, http.StatusBadRequest, "op")
		return
	}
	s.mu.Lock()
	lessons, depth := s.lessons, s.depth
	s.mu.Unlock()
	switch r.URL.Path {
	case lessonsRoute:
		if lessons == nil {
			send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine has no reviewer container, so it suggests no instruction lines."})
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
		refuse(w, http.StatusNotFound, "route")
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
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	id, okID := body["id"].(string)
	okID = okID && lessonLineID.MatchString(id)
	var err error
	switch op {
	case "read":
		if !keysWithin(body, "op", "name") {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
	case "suggest":
		consent, consented := consentOf(body)
		if !keysWithin(body, "op", "name", "costOk") || !consented {
			refuse(w, http.StatusBadRequest, "fields")
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
		s.logger.Info("lessons", "op", op, "ok", err == nil)
		reply(w, err, http.StatusAccepted)
		return
	case "approve":
		sid, okSID := viewSession(body)
		if !keysWithin(body, "op", "name", "id", "sid") || !okID || !okSID {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		err = lessons.Approve(name, id, sid)
	case "decline":
		if !keysWithin(body, "op", "name", "id") || !okID {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		err = lessons.Decline(name, id)
	default:
		send(w, http.StatusBadRequest, problem{Error: "op", Message: "The engine reads, suggests, approves and declines instruction lines only."})
		return
	}
	if op != "read" {
		s.logger.Info("lessons", "op", op, "ok", err == nil)
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
	Advisory     []string       `json:"advisory"`
	Runs         map[string]int `json:"runs"`
	MaxFiles     int            `json:"maxFiles"`
	MaxLines     int            `json:"maxLines"`
	Extra        []string       `json:"extra"`
	ExtraRuns    int            `json:"extraRuns"`
	Agents       []string       `json:"agents"`
	Type         string         `json:"type"`
	RunsTotal    int            `json:"runsTotal"`
	AboveRuns    int            `json:"aboveRuns"`
	AdvisoryRuns int            `json:"advisoryRuns"`
	MaxRuns      int            `json:"maxRuns"`
}

func agentList(v any, limit int) ([]string, bool) {
	raw, ok := v.([]any)
	if !ok || len(raw) > limit {
		return nil, false
	}
	out := make([]string, 0, len(raw))
	for _, x := range raw {
		a, ok := x.(string)
		if !ok || len(a) > maxDepthAgent || !workflow.AgentName(a) {
			return nil, false
		}
		out = append(out, a)
	}
	return out, true
}

func (s *Server) depthOp(w http.ResponseWriter, depth session.ReviewDepthPage, op string, body map[string]any) {
	path, okPath := body["path"].(string)
	if !okPath || path == "" || len(path) > session.MaxCwd {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	switch op {
	case "read":
		if !keysWithin(body, "op", "path") {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
	case "set":
		if !keysWithin(body, "op", "path", "advisory", "runs", "maxFiles", "maxLines", "extra", "extraRuns") {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		advisory, okAdvisory := agentList(body["advisory"], workflow.MaxAdvisory)
		extra, okExtra := agentList(body["extra"], workflow.MaxDeeper)
		files, okFiles := integer(body["maxFiles"])
		lines, okLines := integer(body["maxLines"])
		extraRuns, okExtraRuns := integer(body["extraRuns"])
		rawRuns, okRuns := body["runs"].(map[string]any)
		if !okAdvisory || !okExtra || !okFiles || !okLines || !okExtraRuns || !okRuns || len(rawRuns) > 20 ||
			files < 0 || lines < 0 || files > workflow.MaxDepthSize || lines > workflow.MaxDepthSize || extraRuns < 0 || extraRuns > review.MaxRuns {
			refuse(w, http.StatusBadRequest, "depth")
			return
		}
		runs := map[string]int{}
		for a, v := range rawRuns {
			n, ok := integer(v)
			if !ok || n < 1 || n > review.MaxRuns || len(a) > maxDepthAgent || !workflow.AgentName(a) {
				refuse(w, http.StatusBadRequest, "depth")
				return
			}
			runs[a] = int(n)
		}
		err := depth.SetReviewDepth(path, workflow.Depth{Advisory: advisory, Runs: runs, MaxFiles: int(files), MaxLines: int(lines), Extra: extra, ExtraRuns: int(extraRuns)})
		s.logger.Info("project setting", "op", "review-depth", "ok", err == nil)
		if err != nil {
			code, p := status(err)
			send(w, code, p)
			return
		}
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
	runs := v.Depth.Runs
	if runs == nil {
		runs = map[string]int{}
	}
	send(w, http.StatusOK, depthJSON{Advisory: nonNil(v.Depth.Advisory), Runs: runs, MaxFiles: v.Depth.MaxFiles, MaxLines: v.Depth.MaxLines, Extra: nonNil(v.Depth.Extra),
		ExtraRuns: v.Depth.ExtraRuns, Agents: nonNil(v.Agents), Type: v.Type, RunsTotal: v.Runs, AboveRuns: v.AboveRuns, AdvisoryRuns: v.Advisory, MaxRuns: review.MaxRuns})
}
