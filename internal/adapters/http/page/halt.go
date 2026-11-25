package page

import (
	"net/http"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const haltRoute = "/api/halt"

type haltJSON struct {
	AwayAll bool     `json:"awayAll"`
	Away    []string `json:"away"`
}

func haltOf(st session.HaltState) haltJSON {
	return haltJSON{AwayAll: st.AwayAll, Away: nonNil(st.Away)}
}

func (s *Server) halt(w http.ResponseWriter, body map[string]any) {
	h := s.reg.Halt()
	op, okOp := body["op"].(string)
	if !okOp {
		refuse(w, http.StatusBadRequest, "op", saidOp)
		return
	}
	var err error
	switch op {
	case "read":
		if !keysWithin(body, "op") {
			refuseFields(w)
			return
		}
	case "away":
		on, okOn := body["on"].(bool)
		raw, hasSid := body["sid"]
		sid, okSid := raw.(string)
		if !keysWithin(body, "op", "on", "sid") || !okOn || hasSid && (!okSid || sid == "" || len(sid) > 80) {
			refuseFields(w)
			return
		}
		err = h.SetAway(sid, on)
	default:
		refuse(w, http.StatusBadRequest, "op", saidOp)
		return
	}
	if op != "read" {
		s.logger.Info("halt", "op", op, "ok", err == nil)
	}
	if err != nil {
		code, p := status(err)
		send(w, code, p)
		return
	}
	send(w, http.StatusOK, haltOf(h.State()))
}
