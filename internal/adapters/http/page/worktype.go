package page

import (
	"net/http"

	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const workTypeRoute = "/work-type-request"

type typeCardJSON struct {
	Text     string `json:"text"`
	Suggests string `json:"suggests,omitempty"`
	Type     string `json:"type,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Use      string `json:"use,omitempty"`
}

func typeCardOf(c *session.TypeCard) *typeCardJSON {
	if c == nil {
		return nil
	}
	return &typeCardJSON{Text: c.Text, Suggests: c.Suggests, Type: c.Type, Reason: c.Reason, Use: c.Use}
}

func (s *Server) workTypeRequest(w http.ResponseWriter, r *http.Request) {
	body, bad := readObject(w, r, MaxAgentBody)
	if bad != 0 {
		agentRefuse(w, bad, "body")
		return
	}
	id, okType := body["type"].(string)
	reason, okReason := body["reason"].(string)
	if !keysWithin(body, "type", "reason") || !okType || !okReason || len(id) > 4*session.MaxTypeID || len(reason) > 4*session.MaxTypeReason {
		agentRefuse(w, http.StatusBadRequest, "input")
		return
	}
	sid, ok := s.forgeSession(w, r)
	if !ok {
		return
	}
	if s.cfg.Work == nil {
		send(w, http.StatusOK, map[string]string{"result": session.WorkTypeRefused, "reason": "this engine has no workflow, so it takes no work type"})
		return
	}
	answer := s.cfg.Work.SuggestType(sid, id, reason)
	shared.For(r.Context(), s.logger).Info("work type request", "side", "agent", "result", answer.Result)
	send(w, http.StatusOK, map[string]string{"result": answer.Result, "reason": answer.Reason})
}
