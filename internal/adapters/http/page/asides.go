package page

import (
	"context"
	"net/http"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	planReviewRoute = "/api/plan-review"
	explainRoute    = "/api/explain"
)

var asideRoutes = map[string]bool{planReviewRoute: true, explainRoute: true}

type asideJSON struct {
	Commit   string  `json:"commit"`
	Branch   string  `json:"branch"`
	State    string  `json:"state"`
	Text     string  `json:"text"`
	Reason   string  `json:"reason"`
	CostUSD  float64 `json:"costUsd,omitempty"`
	At       int64   `json:"at"`
	PlanHash string  `json:"planHash,omitempty"`
	PlanPath string  `json:"planPath,omitempty"`
}

func asideOf(v *session.AsideView) *asideJSON {
	if v == nil {
		return nil
	}
	return &asideJSON{Commit: v.Commit, Branch: v.Branch, State: v.State, Text: v.Text, Reason: v.Reason, CostUSD: v.CostUSD, At: millis(v.At), PlanHash: v.PlanHash, PlanPath: v.PlanPath}
}

func (s *Server) aside(w http.ResponseWriter, r *http.Request, body map[string]any) {
	starter, ok := s.cfg.Reviews.(session.AsideStarter)
	if s.cfg.Reviews == nil || !ok {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine runs no reviewer containers, so it can't review a plan or explain a change."})
		return
	}
	sid, okSID := body["sid"].(string)
	consent, consented := consentOf(body)
	if !okSID || !consented || sid == "" || len(sid) > 80 {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	hash := ""
	if r.URL.Path == planReviewRoute {
		h, okHash := body["hash"].(string)
		if !keysWithin(body, "sid", "hash", "costOk") || !okHash || !hash256.MatchString(h) {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		hash = h
	} else if !keysWithin(body, "sid", "costOk") {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(manageFor + writeFor))
	ctx, cancel := context.WithTimeout(r.Context(), manageFor)
	defer cancel()
	var err error
	if r.URL.Path == planReviewRoute {
		err = starter.PlanReview(ctx, sid, hash, consent.CostOK)
		shared.For(r.Context(), s.logger).Info("plan review", "ok", err == nil)
	} else {
		err = starter.Explain(ctx, sid, consent.CostOK)
		shared.For(r.Context(), s.logger).Info("explain change", "ok", err == nil)
	}
	reply(w, err, http.StatusAccepted)
}
