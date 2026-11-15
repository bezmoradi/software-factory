package page

import (
	"context"
	"net/http"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	reviewerChecksRoute = "/api/reviewer-checks"
	maxCheckAgent       = 200
)

type checkRunJSON struct {
	State   string `json:"state"`
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
	Reply   string `json:"reply"`
}

type reviewerCheckJSON struct {
	Result     string       `json:"result"`
	Reason     string       `json:"reason"`
	ClaudeCode string       `json:"claudeCode"`
	Plugin     string       `json:"plugin"`
	Models     []string     `json:"models"`
	Model      string       `json:"model"`
	Effort     string       `json:"effort"`
	Defect     checkRunJSON `json:"defect"`
	Clean      checkRunJSON `json:"clean"`
	By         string       `json:"by"`
	StartedAt  int64        `json:"startedAt"`
	EndedAt    int64        `json:"endedAt"`
	CostUSD    float64      `json:"costUsd"`
}

type roleCheckJSON struct {
	Agent    string             `json:"agent"`
	Role     string             `json:"role"`
	RoleName string             `json:"roleName"`
	Running  bool               `json:"running"`
	Check    *reviewerCheckJSON `json:"check"`
}

type reviewerChecksJSON struct {
	ClaudeCode string          `json:"claudeCode"`
	Plugin     string          `json:"plugin"`
	Minimum    minimumJSON     `json:"minimum"`
	Roles      []roleCheckJSON `json:"roles"`
}

type minimumJSON struct {
	ClaudeCode string `json:"claudeCode"`
	Plugin     string `json:"plugin"`
}

func reviewerChecksOf(v session.ReviewerChecksView) reviewerChecksJSON {
	out := reviewerChecksJSON{ClaudeCode: v.ClaudeCode, Plugin: v.Plugin, Minimum: minimumJSON{ClaudeCode: v.Minimum.ClaudeCode, Plugin: v.Minimum.Plugin}, Roles: []roleCheckJSON{}}
	for _, r := range v.Roles {
		rc := roleCheckJSON{Agent: r.Agent, Role: string(r.Role), RoleName: r.Role.Name(), Running: r.Running}
		if c := r.Check; c != nil {
			rc.Check = &reviewerCheckJSON{Result: string(c.Result), Reason: c.Reason, ClaudeCode: c.Versions.ClaudeCode, Plugin: c.Versions.Plugin, Models: nonNil(c.Versions.Models), Model: c.Versions.Model, Effort: c.Versions.Effort,
				Defect: checkRunOf(c.Defect), Clean: checkRunOf(c.Clean), By: c.By, StartedAt: millis(c.StartedAt), EndedAt: millis(c.EndedAt), CostUSD: c.CostUSD}
		}
		out.Roles = append(out.Roles, rc)
	}
	return out
}

func checkRunOf(r session.CheckRunResult) checkRunJSON {
	return checkRunJSON{State: string(r.State), Verdict: r.Verdict, Reason: r.Reason, Reply: r.Reply}
}

func (s *Server) reviewerChecks(w http.ResponseWriter, r *http.Request, body map[string]any) {
	if s.cfg.ReviewerChecks == nil {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine has no reviewer container, so it runs no reviewer checks."})
		return
	}
	op, ok := body["op"].(string)
	if !ok {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	switch op {
	case "list":
		if !keysWithin(body, "op") {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
	case "check":
		agent, okAgent := body["agent"].(string)
		consent, okConsent := consentOf(body)
		if !keysWithin(body, "op", "agent", "costOk") || !okAgent || !okConsent || len(agent) > maxCheckAgent || !agentName.MatchString(agent) {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(manageFor + writeFor))
		ctx, cancel := context.WithTimeout(r.Context(), manageFor)
		defer cancel()
		err := s.cfg.ReviewerChecks.CheckNow(ctx, agent, consent.CostOK)
		shared.For(r.Context(), s.logger).Info("reviewer check", "ok", err == nil)
		if err != nil {
			code, p := status(err)
			send(w, code, p)
			return
		}
	default:
		send(w, http.StatusBadRequest, problem{Error: "op", Message: "The engine lists reviewer checks and runs one when you press Check now, nothing else."})
		return
	}
	v, err := s.cfg.ReviewerChecks.ReviewerChecks()
	if err != nil {
		code, p := status(err)
		send(w, code, p)
		return
	}
	code := http.StatusOK
	if op == "check" {
		code = http.StatusAccepted
	}
	send(w, code, reviewerChecksOf(v))
}
