package page

import (
	"context"
	"math"
	"net/http"
	"slices"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	limitsRoute = "/api/limits"
	limitsFor   = 2*time.Minute + 15*time.Second
)

func capsBody(body map[string]any) (session.Caps, bool) {
	m, okMoney := body["money"].(float64)
	minutes, okMinutes := integer(body["minutes"])
	turns, okTurns := integer(body["turns"])
	if !okMoney || !okMinutes || !okTurns || math.IsNaN(m) || math.IsInf(m, 0) {
		return session.Caps{}, false
	}
	if minutes < 0 || minutes > session.MaxCapMinutes || turns < 0 || turns > session.MaxCapTurns {
		return session.Caps{}, false
	}
	return session.CheckCaps(session.Caps{MoneyUSD: m, Minutes: int(minutes), Turns: int(turns)})
}

func (s *Server) knownProject(name string) bool {
	if s.cfg.Repos == nil || name == "" {
		return false
	}
	list, err := s.cfg.Repos.List()
	if err != nil {
		return false
	}
	return slices.ContainsFunc(list, func(r session.Repository) bool { return r.Name == name })
}

func (s *Server) limits(w http.ResponseWriter, r *http.Request, body map[string]any) {
	l := s.reg.Limits()
	op, okOp := body["op"].(string)
	if !okOp {
		refuse(w, http.StatusBadRequest, "op")
		return
	}
	sid, okSid := body["sid"].(string)
	okSid = okSid && sid != "" && len(sid) <= 80
	var err error
	switch op {
	case "read":
		if !keysWithin(body, "op") {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
	case "project":
		name, okName := body["name"].(string)
		c, okCaps := capsBody(body)
		if !keysWithin(body, "op", "name", "money", "minutes", "turns") || !okName || !okCaps || len(name) > 400 {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		if !s.knownProject(name) {
			send(w, http.StatusNotFound, problem{Error: "project", Message: "The engine has no project of that name."})
			return
		}
		err = l.SetProject(name, c)
	case "session":
		c, okCaps := capsBody(body)
		if !keysWithin(body, "op", "sid", "money", "minutes", "turns") || !okSid || !okCaps {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		err = l.SetSession(sid, c)
	case "raise":
		kind, okKind := body["kind"].(string)
		value, okValue := body["value"].(float64)
		if !keysWithin(body, "op", "sid", "kind", "value") || !okSid || !okKind || !session.ValidCapKind(kind) || !okValue || math.IsNaN(value) || math.IsInf(value, 0) {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		err = l.Raise(sid, kind, value)
	case "stop":
		if !keysWithin(body, "op", "sid") || !okSid {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		rc := http.NewResponseController(w)
		_ = rc.SetWriteDeadline(time.Now().Add(limitsFor + writeFor))
		ctx, cancel := context.WithTimeout(r.Context(), limitsFor)
		err = l.StopAtCap(ctx, sid)
		cancel()
	case "continue":
		side, okSide := body["side"].(string)
		kind, okKind := body["kind"].(string)
		_, hasSid := body["sid"]
		if !keysWithin(body, "op", "side", "kind", "sid") || !okSide || !okKind || len(side) > 40 || len(kind) > 60 || hasSid && !okSid {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		err = l.Continue(r.Context(), side, sid, kind)
	case "wake":
		on, okOn := body["on"].(bool)
		if !keysWithin(body, "op", "sid", "on") || !okSid || !okOn {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		err = l.WakeAtReset(sid, on)
	default:
		refuse(w, http.StatusBadRequest, "op")
		return
	}
	if op != "read" {
		shared.For(r.Context(), s.logger).Info("limits", "op", op, "ok", err == nil)
	}
	if err != nil {
		code, p := status(err)
		send(w, code, p)
		return
	}
	send(w, http.StatusOK, l.Frame())
}
