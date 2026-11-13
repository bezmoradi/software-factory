package page

import (
	"net/http"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

type auditEntryJSON struct {
	Time         int64    `json:"time"`
	Kind         string   `json:"kind"`
	By           string   `json:"by"`
	Result       string   `json:"result"`
	Reason       string   `json:"reason"`
	PersonReason string   `json:"personReason"`
	Commit       string   `json:"commit"`
	Branch       string   `json:"branch"`
	Reviewer     string   `json:"reviewer"`
	Verdict      string   `json:"verdict"`
	Path         string   `json:"path"`
	Missing      []string `json:"missing"`
}

func (s *Server) audit(w http.ResponseWriter, body map[string]any) {
	if s.cfg.Audit == nil {
		send(w, http.StatusOK, map[string]any{"entries": []any{}, "refusals": map[string]any{}})
		return
	}
	sid, ok := body["sid"].(string)
	if !keysWithin(body, "sid", "branch") || !ok || sid == "" || len(sid) > 80 {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	v, err := s.cfg.Audit.View(sid)
	if err != nil {
		code, p := status(err)
		send(w, code, p)
		return
	}
	entries := make([]auditEntryJSON, 0, len(v.Entries))
	for _, e := range v.Entries {
		entries = append(entries, auditEntryJSON{Time: millis(e.Time), Kind: e.Kind, By: e.By, Result: e.Result, Reason: e.Reason, PersonReason: e.PersonReason,
			Commit: e.Commit, Branch: e.Branch, Reviewer: e.Reviewer, Verdict: e.Verdict, Path: e.Path, Missing: nonNil(e.Missing)})
	}
	refusals := v.Refusals
	if refusals == nil {
		refusals = map[string]int{}
	}
	send(w, http.StatusOK, map[string]any{"entries": entries, "refusals": refusals})
}

func (s *Server) refused(sid string, advice adviceKinds) {
	if s.cfg.Audit == nil {
		return
	}
	if advice.pushes {
		s.cfg.Audit.Refused(sid, session.RefusedPush)
	}
	if advice.remote {
		s.cfg.Audit.Refused(sid, session.RefusedRemote)
	}
	if advice.protected {
		s.cfg.Audit.Refused(sid, session.RefusedProtected)
	}
}

type adviceKinds struct {
	pushes    bool
	remote    bool
	protected bool
}
