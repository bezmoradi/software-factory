package page

import (
	"context"
	"net/http"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	keepRoute  = "/api/keep"
	restoreFor = 2 * time.Minute
)

func (s *Server) UseKeep(k session.KeepPage) {
	s.branchCopy = k
}

type keepJSON struct {
	Branch     string `json:"branch"`
	Commit     string `json:"commit"`
	At         int64  `json:"at"`
	Bytes      int64  `json:"bytes"`
	Packs      int    `json:"packs"`
	Stopped    string `json:"stopped"`
	Kind       string `json:"kind"`
	Why        string `json:"why"`
	Head       string `json:"head"`
	Holds      bool   `json:"holds"`
	Copying    bool   `json:"copying"`
	Restorable bool   `json:"restorable"`
	Refusal    string `json:"refusal"`
	SessionCap int64  `json:"sessionCap"`
	AllCap     int64  `json:"allCap"`
	WasStopped bool   `json:"wasStopped"`
}

func keepJSONOf(v session.KeepView, stopped bool) keepJSON {
	return keepJSON{Branch: v.Branch, Commit: v.Commit, At: millis(v.At), Bytes: v.Bytes, Packs: v.Packs, Stopped: v.Stopped, Kind: string(v.Kind), Why: v.Why,
		Head: v.Head, Holds: v.Holds, Copying: v.Copying, Restorable: v.Restorable, Refusal: v.Refusal, SessionCap: v.SessionCap, AllCap: v.AllCap, WasStopped: stopped}
}

func (s *Server) keepAPI(w http.ResponseWriter, r *http.Request, body map[string]any) {
	if s.branchCopy == nil {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine has no agent-host or workspace, so it keeps no copy of the sessions' commits."})
		return
	}
	op, okOp := body["op"].(string)
	sid, okSID := viewSession(body)
	if !okOp || !keysWithin(body, "op", "sid") || !okSID {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	var v session.KeepView
	var stopped bool
	var err error
	switch op {
	case "read":
		v, err = s.branchCopy.View(sid)
	case "restore":
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(restoreFor + writeFor))
		ctx, cancel := context.WithTimeout(r.Context(), restoreFor)
		defer cancel()
		v, stopped, err = s.branchCopy.Restore(ctx, sid)
		s.logger.Info("restore branch", "ok", err == nil)
	default:
		send(w, http.StatusBadRequest, problem{Error: "op", Message: "The engine reads its copy of a session's commits and restores the branch from it only."})
		return
	}
	if err != nil {
		code, p := status(err)
		send(w, code, p)
		return
	}
	send(w, http.StatusOK, keepJSONOf(v, stopped))
}
