package page

import (
	"context"
	"net/http"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	diagnosticsRoute = "/api/diagnostics"
	diagnosticsFor   = 10 * time.Second
)

func (s *Server) diagnostics(w http.ResponseWriter, r *http.Request, body map[string]any) {
	if len(body) != 0 {
		refuseFields(w)
		return
	}
	if s.cfg.Diagnostics == nil {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine can't build a diagnostics file."})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), diagnosticsFor)
	defer cancel()
	text, err := s.cfg.Diagnostics.Diagnostics(ctx)
	if err != nil {
		code, p := status(err)
		send(w, code, p)
		return
	}
	shared.For(r.Context(), s.logger).Info("diagnostics", "event", "built", "bytes", len(text))
	send(w, http.StatusOK, map[string]string{"text": text})
}
