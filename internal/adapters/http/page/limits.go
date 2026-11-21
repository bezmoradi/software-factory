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

type capsProblem struct {
	Error   string `json:"error"`
	Field   string `json:"field"`
	Message string `json:"message"`
}

func refuseCaps(w http.ResponseWriter, field, message string) {
	send(w, http.StatusBadRequest, capsProblem{Error: "fields", Field: field, Message: message})
}

var capWireFields = map[string]string{session.CapMoney: "money", session.CapTime: "minutes", session.CapTurns: "turns", session.CapDisk: "disk"}

func capsBody(body map[string]any) (session.Caps, string) {
	m, okMoney := body["money"].(float64)
	if !okMoney {
		return session.Caps{}, session.CapMoney
	}
	minutes, okMinutes := integer(body["minutes"])
	if !okMinutes {
		return session.Caps{}, session.CapTime
	}
	turns, okTurns := integer(body["turns"])
	if !okTurns {
		return session.Caps{}, session.CapTurns
	}
	var disk int64
	if raw, has := body["disk"]; has {
		var okDisk bool
		if disk, okDisk = integer(raw); !okDisk {
			return session.Caps{}, session.CapDisk
		}
	}
	return session.CheckCapsFault(session.Caps{MoneyUSD: m, Minutes: int(minutes), Turns: int(turns), DiskGiB: int(disk)})
}

func checkedCaps(w http.ResponseWriter, body map[string]any) (session.Caps, bool) {
	c, fault := capsBody(body)
	if fault != "" {
		refuseCaps(w, capWireFields[fault], session.CapRefusal(fault))
		return session.Caps{}, false
	}
	return c, true
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
	case "check":
		if !keysWithin(body, "op", "money", "minutes", "turns", "disk") {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		if _, ok := checkedCaps(w, body); !ok {
			return
		}
	case "project":
		name, okName := body["name"].(string)
		if !keysWithin(body, "op", "name", "money", "minutes", "turns", "disk") || !okName || len(name) > 400 {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		c, okCaps := checkedCaps(w, body)
		if !okCaps {
			return
		}
		if !s.knownProject(name) {
			send(w, http.StatusNotFound, problem{Error: "project", Message: "The engine has no project of that name."})
			return
		}
		err = l.SetProject(name, c)
	case "session":
		if !keysWithin(body, "op", "sid", "money", "minutes", "turns", "disk") || !okSid {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		c, okCaps := checkedCaps(w, body)
		if !okCaps {
			return
		}
		err = l.SetSession(sid, c)
	case "raise":
		kind, okKind := body["kind"].(string)
		value, okValue := body["value"].(float64)
		if !keysWithin(body, "op", "sid", "kind", "value") || !okSid || !okKind || !session.ValidCapKind(kind) {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		if !okValue || math.IsNaN(value) || math.IsInf(value, 0) {
			refuseCaps(w, "value", "Type the new cap as a number.")
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
	case "measure":
		if !keysWithin(body, "op", "sid") || !okSid {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		rc := http.NewResponseController(w)
		_ = rc.SetWriteDeadline(time.Now().Add(session.DiskMeasureFor + writeFor))
		ctx, cancel := context.WithTimeout(r.Context(), session.DiskMeasureFor)
		err = l.MeasureNow(ctx, sid)
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
	if op != "read" && op != "check" {
		shared.For(r.Context(), s.logger).Info("limits", "op", op, "ok", err == nil)
	}
	if err != nil {
		code, p := status(err)
		send(w, code, p)
		return
	}
	send(w, http.StatusOK, l.Frame())
}
