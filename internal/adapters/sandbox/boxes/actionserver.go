package boxes

import (
	"context"
	"net/http"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	routeActions     = "actions"
	routeAction      = "action"
	routeActionBox   = "action-box"
	routeActionStart = "action-start"
	routeActionLog   = "action-log"
)

type actionJSON struct {
	Run        string `json:"run"`
	State      string `json:"state"`
	ExitCode   int    `json:"exitCode"`
	OOMKilled  bool   `json:"oomKilled"`
	Error      string `json:"error"`
	StartedAt  int64  `json:"startedAt"`
	FinishedAt int64  `json:"finishedAt"`
	Subnet     string `json:"subnet,omitempty"`
	Recipe     string `json:"recipe,omitempty"`
}

type actionAnswerJSON struct {
	Version string      `json:"version"`
	Action  *actionJSON `json:"action"`
}

type actionLogJSON struct {
	Version string `json:"version"`
	Log     string `json:"log"`
	Cut     bool   `json:"cut"`
}

type actionsJSON struct {
	Version string    `json:"version"`
	Actions []boxJSON `json:"actions"`
}

func actionRoute(rest []string) (routed, bool) {
	switch {
	case len(rest) == 0:
		return routed{name: routeActions}, true
	case len(rest) == 1:
		return routed{name: routeAction, id: rest[0]}, true
	case len(rest) == 2 && rest[1] == actionStartSeg:
		return routed{name: routeActionStart, id: rest[0]}, true
	case len(rest) == 2 && rest[1] == logSeg:
		return routed{name: routeActionLog, id: rest[0]}, true
	case len(rest) == 2:
		return routed{name: routeActionBox, id: rest[0], snapshot: rest[1]}, true
	}
	return routed{}, false
}

func (s *server) actions(base context.Context, w http.ResponseWriter, r *http.Request, rt routed) {
	if s.cfg.Actions == nil {
		s.refuse(w, r, refuse(http.StatusNotFound, ReasonRoute, "the box helper has no such route"))
		return
	}
	if rt.name != routeActions {
		if err := badAction(rt.id, rt.snapshot); err != nil {
			s.failed(w, r, err)
			return
		}
	}
	limit := otherFor
	if r.Method == http.MethodPut || rt.name == routeActionStart {
		limit = startFor
	}
	ctx, cancel := context.WithTimeout(base, limit)
	defer cancel()
	switch {
	case rt.name == routeActions:
		list, err := s.cfg.Actions.ListActions(ctx)
		if err != nil {
			s.failed(w, r, err)
			return
		}
		out := actionsJSON{Version: WireVersion, Actions: []boxJSON{}}
		for _, b := range list {
			out.Actions = append(out.Actions, boxOf(b))
		}
		writeJSON(w, http.StatusOK, out)
	case r.Method == http.MethodPut:
		info, err := s.cfg.Actions.MakeAction(ctx, rt.id, rt.snapshot)
		if err != nil {
			s.failed(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, actionAnswerJSON{Version: WireVersion, Action: &actionJSON{Run: info.Run, State: "created", Subnet: info.Subnet.String(), Recipe: info.Recipe}})
	case rt.name == routeActionStart:
		if err := s.cfg.Actions.StartAction(ctx, rt.id); err != nil {
			s.failed(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case rt.name == routeActionLog:
		text, cut, err := s.cfg.Actions.ActionLog(ctx, rt.id)
		if err != nil {
			s.failed(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, actionLogJSON{Version: WireVersion, Log: text, Cut: cut})
	case r.Method == http.MethodDelete:
		if err := s.cfg.Actions.RemoveAction(ctx, rt.id); err != nil {
			s.failed(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		st, err := s.cfg.Actions.ActionState(ctx, rt.id)
		if err != nil {
			s.failed(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, actionAnswerJSON{Version: WireVersion, Action: &actionJSON{Run: st.Run, State: st.State, ExitCode: st.ExitCode, OOMKilled: st.OOMKilled, Error: st.Error,
			StartedAt: millisOf(st.StartedAt), FinishedAt: millisOf(st.FinishedAt)}})
	}
}

var _ session.ActionHost = (*Client)(nil)
