package page

import (
	"net/http"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const haltRoute = "/api/halt"

type haltJSON struct {
	Mode    string   `json:"mode"`
	Since   int64    `json:"since"`
	AwayAll bool     `json:"awayAll"`
	Away    []string `json:"away"`
}

func haltOf(st session.HaltState) haltJSON {
	return haltJSON{Mode: st.Mode, Since: millis(st.Since), AwayAll: st.AwayAll, Away: nonNil(st.Away)}
}

func (s *Server) halt(w http.ResponseWriter, body map[string]any) {
	h := s.reg.Halt()
	op, okOp := body["op"].(string)
	if !okOp {
		refuse(w, http.StatusBadRequest, "op")
		return
	}
	var err error
	switch op {
	case "read", "stop", "pause", "release":
		if !keysWithin(body, "op") {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		switch op {
		case "stop":
			err = h.StopAll()
		case "pause":
			err = h.PauseAll()
		case "release":
			err = h.Release()
		}
	case "away":
		on, okOn := body["on"].(bool)
		raw, hasSid := body["sid"]
		sid, okSid := raw.(string)
		if !keysWithin(body, "op", "on", "sid") || !okOn || hasSid && (!okSid || sid == "" || len(sid) > 80) {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		err = h.SetAway(sid, on)
	default:
		refuse(w, http.StatusBadRequest, "op")
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
