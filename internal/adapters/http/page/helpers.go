package page

import (
	"context"
	"net/http"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	helperCopyRoute   = "/helper-copy"
	helperRemoveRoute = "/helper-copy/remove"
	maxHelperBody     = 8 << 10
	maxHelperField    = 4096
	helperFor         = 4 * time.Minute
)

func (s *Server) helperField(w http.ResponseWriter, r *http.Request, key string) (string, string, bool) {
	sid, err := s.reg.Checking(connSecret(r))
	if err != nil {
		s.agentError(w, r, err)
		return "", "", false
	}
	body, bad := readObject(w, r, maxHelperBody)
	if bad != 0 {
		agentRefuse(w, bad, "body")
		return "", "", false
	}
	value, ok := body[key].(string)
	if !keysWithin(body, key) || !ok || len(value) > maxHelperField {
		send(w, http.StatusBadRequest, problem{Error: "input", Message: "Software Factory takes only the helper copy's " + key + " here."})
		return "", "", false
	}
	if s.work == nil {
		send(w, http.StatusConflict, problem{Error: "no-helper", Message: "This engine makes no helper copies."})
		return "", "", false
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(helperFor + writeFor))
	return sid, value, true
}

func (s *Server) helperCopy(w http.ResponseWriter, r *http.Request) {
	sid, name, ok := s.helperField(w, r, "name")
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), helperFor)
	defer cancel()
	path, err := s.work.AddHelper(ctx, sid, ConnBox(r.Context()), name)
	if err != nil {
		s.agentError(w, r, err)
		return
	}
	shared.For(r.Context(), s.logger).Info("helper copy request", "side", "agent", "result", "added")
	send(w, http.StatusOK, map[string]string{"path": path})
}

func (s *Server) helperRemove(w http.ResponseWriter, r *http.Request) {
	sid, path, ok := s.helperField(w, r, "path")
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), helperFor)
	defer cancel()
	if err := s.work.RemoveHelper(ctx, sid, ConnBox(r.Context()), path); err != nil {
		s.agentError(w, r, err)
		return
	}
	shared.For(r.Context(), s.logger).Info("helper copy request", "side", "agent", "result", "removed")
	send(w, http.StatusOK, map[string]bool{"ok": true})
}
