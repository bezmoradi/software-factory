package page

import (
	"context"
	"net/http"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const testListRoute = "/api/test-list"

type testListJSON struct {
	Commit  string  `json:"commit"`
	Branch  string  `json:"branch"`
	State   string  `json:"state"`
	Text    string  `json:"text"`
	Reason  string  `json:"reason"`
	CostUSD float64 `json:"costUsd,omitempty"`
	At      int64   `json:"at"`

	Steps []walkStepJSON `json:"steps,omitempty"`
}

func testListOf(v *session.TestListView) *testListJSON {
	if v == nil {
		return nil
	}
	out := &testListJSON{Commit: v.Commit, Branch: v.Branch, State: v.State, Text: v.Text, Reason: v.Reason, CostUSD: v.CostUSD, At: millis(v.At)}
	out.Steps = stepsOf(v.Steps)
	return out
}

func (s *Server) testList(w http.ResponseWriter, r *http.Request, body map[string]any) {
	if s.cfg.Reviews == nil {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine runs no reviewer containers, so it can't write a manual test list."})
		return
	}
	sid, ok := body["sid"].(string)
	consent, consented := consentOf(body)
	if !keysWithin(body, "sid", "costOk") || !ok || !consented || sid == "" || len(sid) > 80 {
		refuseFields(w)
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(manageFor + writeFor))
	ctx, cancel := context.WithTimeout(r.Context(), manageFor)
	defer cancel()
	err := s.cfg.Reviews.TestList(ctx, sid, consent.CostOK)
	shared.For(r.Context(), s.logger).Info("test list", "ok", err == nil)
	reply(w, err, http.StatusAccepted)
}
