package page

import (
	"context"
	"net/http"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	pushRecordRoute = "/api/push-record"
	pushRecordFor   = 10 * time.Second
)

func (s *Server) UsePushRecords(p session.PushRecordPage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pushRecords = p
}

func (s *Server) pushRecord(w http.ResponseWriter, r *http.Request, body map[string]any) {
	s.mu.Lock()
	records := s.pushRecords
	s.mu.Unlock()
	sid, okSID := viewSession(body)
	commit, okCommit := body["commit"].(string)
	if !keysWithin(body, "sid", "commit") || !okSID || !okCommit || !flagCommit.MatchString(commit) {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	if records == nil {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine has no audit file or sessions, so it keeps no push records."})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), pushRecordFor)
	defer cancel()
	got, err := records.PushRecord(ctx, sid, commit)
	if err != nil {
		code, p := status(err)
		send(w, code, p)
		return
	}
	shared.For(r.Context(), s.logger).Info("push record", "event", "built", "bytes", len(got.Text))
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Content-Disposition", `attachment; filename="`+got.Name+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(got.Text)
}
