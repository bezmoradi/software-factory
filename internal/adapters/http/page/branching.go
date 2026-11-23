package page

import (
	"context"
	"net/http"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/push"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	bringMainRoute   = "/api/bring-main"
	startPointsRoute = "/api/start-points"
	maxFromBranch    = 255
)

var branchingRoutes = map[string]bool{bringMainRoute: true, startPointsRoute: true}

type remoteRefJSON struct {
	Name   string `json:"name"`
	Commit string `json:"commit"`
}

type pullHeadJSON struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Branch string `json:"branch"`
	Commit string `json:"commit"`
}

type lineageJSON struct {
	Branch string `json:"branch"`
	Origin string `json:"origin,omitempty"`
	From   string `json:"from"`

	Group     string `json:"group,omitempty"`
	GroupKind string `json:"groupKind,omitempty"`
}

func lineageOf(l *session.Lineage) *lineageJSON {
	if l == nil {
		return nil
	}
	return &lineageJSON{Branch: l.Branch, Origin: l.Origin, From: l.From, Group: l.Group, GroupKind: l.GroupKind}
}

func (s *Server) UseBranching(b session.BranchPage) {
	s.mu.Lock()
	s.branching = b
	s.mu.Unlock()
}

func startFromOf(v any, present bool) (session.StartFrom, bool) {
	if !present {
		return session.StartFrom{}, true
	}
	m, ok := v.(map[string]any)
	if !ok || !keysWithin(m, "branch", "pull", "sid", "commit") {
		return session.StartFrom{}, false
	}
	_, hasSid := m["sid"]
	if hasSid {
		sid, isText := m["sid"].(string)
		ok := isText && sid != "" && len(sid) <= 80
		if raw, has := m["commit"]; has {
			commit, isCommit := raw.(string)
			return session.StartFrom{SID: sid, Commit: commit}, ok && len(m) == 2 && isCommit && commitHex.MatchString(commit)
		}
		return session.StartFrom{SID: sid}, ok && len(m) == 1
	}
	commit, isCommit := m["commit"].(string)
	if len(m) != 2 || !isCommit || !commitHex.MatchString(commit) {
		return session.StartFrom{}, false
	}
	if raw, ok := m["branch"]; ok {
		b, isText := raw.(string)
		return session.StartFrom{Branch: b, Commit: commit}, isText && len(b) <= maxFromBranch && push.ValidBranch(b)
	}
	n, isNumber := integer(m["pull"])
	return session.StartFrom{Pull: int(n), Commit: commit}, isNumber && n > 0 && n <= 1_000_000_000
}

func (s *Server) branchingAPI(w http.ResponseWriter, r *http.Request, body map[string]any) {
	s.mu.Lock()
	b := s.branching
	s.mu.Unlock()
	if b == nil {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine has no agent-host, so it can't start or manage sessions."})
		return
	}
	sid, okSid := body["sid"].(string)
	okSid = okSid && sid != "" && len(sid) <= 80
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(manageFor + writeFor))
	ctx, cancel := context.WithTimeout(r.Context(), manageFor)
	defer cancel()
	switch r.URL.Path {
	case bringMainRoute:
		if !keysWithin(body, "sid") || !okSid {
			refuseFields(w)
			return
		}
		commit, err := b.BringMain(ctx, sid)
		shared.For(r.Context(), s.logger).Info("bring-main", "ok", err == nil)
		if err != nil {
			code, p := status(err)
			send(w, code, p)
			return
		}
		send(w, http.StatusOK, map[string]string{"commit": commit})
	case startPointsRoute:
		path, okPath := body["path"].(string)
		if !keysWithin(body, "path") || !okPath || path == "" || len(path) > session.MaxCwd {
			refuseFields(w)
			return
		}
		points, err := b.StartPoints(ctx, path)
		if err != nil {
			code, p := status(err)
			send(w, code, p)
			return
		}
		branches := make([]remoteRefJSON, 0, len(points.Branches))
		for _, x := range points.Branches {
			branches = append(branches, remoteRefJSON{Name: x.Name, Commit: x.Commit})
		}
		pulls := make([]pullHeadJSON, 0, len(points.Pulls))
		for _, x := range points.Pulls {
			pulls = append(pulls, pullHeadJSON{Number: x.Number, Title: x.Title, Branch: x.Branch, Commit: x.Commit})
		}
		send(w, http.StatusOK, map[string]any{"main": points.Main, "branches": branches, "pulls": pulls, "pullsWhy": points.PullsWhy})
	default:
		refuse(w, http.StatusNotFound, "route", saidRoute)
	}
}
