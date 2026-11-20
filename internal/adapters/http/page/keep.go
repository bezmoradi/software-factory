package page

import (
	"context"
	"net/http"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	keepRoute  = "/api/keep"
	restoreFor = 2 * time.Minute
)

func (s *Server) UseKeep(k session.KeepPage) {
	s.branchCopy = k
}

type keepJSON struct {
	Branch     string         `json:"branch"`
	Commit     string         `json:"commit"`
	At         int64          `json:"at"`
	Bytes      int64          `json:"bytes"`
	Packs      int            `json:"packs"`
	Stopped    string         `json:"stopped"`
	Kind       string         `json:"kind"`
	Why        string         `json:"why"`
	Head       string         `json:"head"`
	Holds      bool           `json:"holds"`
	Copying    bool           `json:"copying"`
	Restorable bool           `json:"restorable"`
	Refusal    string         `json:"refusal"`
	SessionCap int64          `json:"sessionCap"`
	AllCap     int64          `json:"allCap"`
	WasStopped bool           `json:"wasStopped"`
	Heads      []keptHeadJSON `json:"heads"`
}

type keptHeadJSON struct {
	Commit     string         `json:"commit"`
	At         int64          `json:"at"`
	Holds      bool           `json:"holds"`
	Restorable bool           `json:"restorable"`
	Refusal    string         `json:"refusal"`
	Reviewed   bool           `json:"reviewed"`
	RolesRead  bool           `json:"rolesRead"`
	Roles      []keptRoleJSON `json:"roles"`
	Passed     bool           `json:"passed"`
}

type keptRoleJSON struct {
	Name    string `json:"name"`
	Agent   string `json:"agent"`
	Verdict string `json:"verdict"`
}

func keepJSONOf(v session.KeepView, stopped bool) keepJSON {
	heads := make([]keptHeadJSON, 0, len(v.Heads))
	for _, h := range v.Heads {
		roles := make([]keptRoleJSON, 0, len(h.Roles))
		for _, r := range h.Roles {
			roles = append(roles, keptRoleJSON{Name: r.Name, Agent: r.Agent, Verdict: string(r.Verdict)})
		}
		heads = append(heads, keptHeadJSON{Commit: h.Commit, At: millis(h.At), Holds: h.Holds, Restorable: h.Restorable, Refusal: h.Refusal, Reviewed: h.Reviewed, RolesRead: h.RolesRead, Roles: roles, Passed: h.Passed})
	}
	return keepJSON{Branch: v.Branch, Commit: v.Commit, At: millis(v.At), Bytes: v.Bytes, Packs: v.Packs, Stopped: v.Stopped, Kind: string(v.Kind), Why: v.Why,
		Head: v.Head, Holds: v.Holds, Copying: v.Copying, Restorable: v.Restorable, Refusal: v.Refusal, SessionCap: v.SessionCap, AllCap: v.AllCap, WasStopped: stopped, Heads: heads}
}

func (s *Server) keepAPI(w http.ResponseWriter, r *http.Request, body map[string]any) {
	if s.branchCopy == nil {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine has no agent-host or workspace, so it keeps no copy of the sessions' commits."})
		return
	}
	op, okOp := body["op"].(string)
	sid, okSID := viewSession(body)
	commit, okCommit := "", true
	if raw, given := body["commit"]; given {
		commit, okCommit = raw.(string)
		okCommit = okCommit && op == "restore" && flagCommit.MatchString(commit)
	}
	if !okOp || !keysWithin(body, "op", "sid", "commit") || !okSID || !okCommit {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	var v session.KeepView
	var stopped bool
	var err error
	switch op {
	case "read":
		v, err = s.branchCopy.View(r.Context(), sid)
	case "restore":
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(restoreFor + writeFor))
		ctx, cancel := context.WithTimeout(r.Context(), restoreFor)
		defer cancel()
		v, stopped, err = s.branchCopy.Restore(ctx, sid, commit)
		shared.For(r.Context(), s.logger).Info("restore branch", "ok", err == nil)
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
