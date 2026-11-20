package page

import (
	"net/http"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const metricsRoute = "/api/metrics"

type roleMetricsJSON struct {
	Role          string `json:"role"`
	Reviews       int    `json:"reviews"`
	Passed        int    `json:"passed"`
	Blocked       int    `json:"blocked"`
	NoVerdict     int    `json:"noVerdict"`
	Failed        int    `json:"failed"`
	Overridden    int    `json:"overridden"`
	AfterCI       int    `json:"afterCi"`
	AfterComments int    `json:"afterComments"`
}

type metricPullJSON struct {
	Number int    `json:"number"`
	State  string `json:"state"`
	At     int64  `json:"at"`
}

type metricsJSON struct {
	Repo              string            `json:"repo"`
	Now               int64             `json:"now"`
	AuditSince        int64             `json:"auditSince"`
	AuditLines        int               `json:"auditLines"`
	AuditCut          bool              `json:"auditCut"`
	Pushes            int               `json:"pushes"`
	Timed             int               `json:"timed"`
	StartToPushMedian int64             `json:"startToPushMedian"`
	StartToPushMean   int64             `json:"startToPushMean"`
	StartToPushLong   int64             `json:"startToPushLongest"`
	Rounds            int               `json:"rounds"`
	Roles             []roleMetricsJSON `json:"roles"`
	Allows            int               `json:"allows"`
	Denied            int               `json:"denied"`
	Overrides         int               `json:"overrides"`
	AfterCI           int               `json:"afterCi"`
	AfterComments     int               `json:"afterComments"`
	ReviewsSince      int64             `json:"reviewsSince"`
	ReviewRuns        int               `json:"reviewRuns"`
	ReviewCost        float64           `json:"reviewCostUsd"`
	ImportedRuns      int               `json:"importedReviewRuns"`
	ImportedCost      float64           `json:"importedReviewCostUsd"`
	AgentSessions     int               `json:"agentSessions"`
	AgentCost         float64           `json:"agentCostUsd"`
	PullsSince        int64             `json:"pullsSince"`
	PullsOpened       int               `json:"pullsOpened"`
	PullsMerged       int               `json:"pullsMerged"`
	PullsClosed       int               `json:"pullsClosed"`
	Pulls             []metricPullJSON  `json:"pulls"`
	Problems          []string          `json:"problems"`
}

func metricsOf(v session.MetricsView) metricsJSON {
	out := metricsJSON{Repo: v.Repo, Now: millis(v.Now), AuditSince: millis(v.AuditSince), AuditLines: v.AuditLines, AuditCut: v.AuditCut, Pushes: v.Pushes, Timed: v.Timed,
		StartToPushMedian: v.StartToPushMedian.Milliseconds(), StartToPushMean: v.StartToPushMean.Milliseconds(), StartToPushLong: v.StartToPushLong.Milliseconds(),
		Rounds: v.Rounds, Roles: make([]roleMetricsJSON, 0, len(v.Roles)), Allows: v.Allows, Denied: v.Denied, Overrides: v.Overrides, AfterCI: v.AfterCI, AfterComments: v.AfterComments,
		ReviewsSince: millis(v.ReviewsSince), ReviewRuns: v.ReviewRuns, ReviewCost: v.ReviewCost, ImportedRuns: v.ImportedRuns, ImportedCost: v.ImportedCost, AgentSessions: v.AgentSessions, AgentCost: v.AgentCost,
		PullsSince: millis(v.PullsSince), PullsOpened: v.PullsOpened, PullsMerged: v.PullsMerged, PullsClosed: v.PullsClosed, Pulls: make([]metricPullJSON, 0, len(v.Pulls)), Problems: nonNil(v.Problems)}
	for _, r := range v.Roles {
		out.Roles = append(out.Roles, roleMetricsJSON{Role: r.Role, Reviews: r.Reviews, Passed: r.Passed, Blocked: r.Blocked, NoVerdict: r.NoVerdict, Failed: r.Failed, Overridden: r.Overridden,
			AfterCI: r.AfterCI, AfterComments: r.AfterComments})
	}
	for _, p := range v.Pulls {
		out.Pulls = append(out.Pulls, metricPullJSON{Number: p.Number, State: p.State, At: millis(p.At)})
	}
	return out
}

func (s *Server) UseMetrics(m session.MetricsPage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metrics = m
}

func (s *Server) projectMetrics(w http.ResponseWriter, body map[string]any) {
	s.mu.Lock()
	m := s.metrics
	s.mu.Unlock()
	if m == nil {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine has no repositories, so it counts nothing."})
		return
	}
	op, okOp := body["op"].(string)
	repo, okRepo := body["repo"].(string)
	if !okOp || !okRepo || len(repo) > maxRulesRepo || !rulesRepoName.MatchString(repo) || !keysWithin(body, "op", "repo") {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	if op != "read" {
		send(w, http.StatusBadRequest, problem{Error: "op", Message: "The engine reads a project's numbers, nothing else."})
		return
	}
	v, err := m.Metrics(repo)
	if err != nil {
		code, p := status(err)
		send(w, code, p)
		return
	}
	send(w, http.StatusOK, metricsOf(v))
}
