package page

import (
	"context"
	"net/http"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const commentsRoute = "/comments-request"

func (s *Server) commentsRequest(w http.ResponseWriter, r *http.Request) {
	body, bad := readObject(w, r, MaxAgentBody)
	if bad != 0 {
		refuse(w, bad, "body")
		return
	}
	if len(body) != 0 {
		refuse(w, http.StatusBadRequest, "input")
		return
	}
	sid, ok := s.forgeSession(w, r)
	if !ok {
		return
	}
	if s.cfg.Forge == nil {
		send(w, http.StatusOK, map[string]string{"result": session.ForgeRefused, "reason": "this engine has no git host API set up"})
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(session.ForgeFor + writeFor))
	answer := s.cfg.Forge.Comments(context.WithoutCancel(r.Context()), sid)
	s.logger.Info("review comments request", "side", "agent", "result", answer.Result)
	send(w, http.StatusOK, map[string]string{"result": answer.Result, "reason": answer.Reason})
}
