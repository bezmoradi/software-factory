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
	Session      string   `json:"session,omitempty"`
	Box          string   `json:"box,omitempty"`
	From         string   `json:"from,omitempty"`
	FromRef      string   `json:"fromRef,omitempty"`
	Source       string   `json:"source,omitempty"`
	Person       string   `json:"person,omitempty"`
	Req          string   `json:"req,omitempty"`
	Hash         string   `json:"hash,omitempty"`
}

func (s *Server) audit(w http.ResponseWriter, body map[string]any) {
	if s.cfg.Audit == nil {
		send(w, http.StatusOK, map[string]any{"entries": []any{}, "refusals": map[string]any{}, "history": []any{}, "friction": []any{}})
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
	refusals := v.Refusals
	if refusals == nil {
		refusals = map[string]int{}
	}
	send(w, http.StatusOK, map[string]any{"entries": auditEntriesJSON(v.Entries), "refusals": refusals,
		"history": historyLinesJSON(session.HistoryLines(v.Entries)), "friction": session.FrictionTips(refusals)})
}

type historyLineJSON struct {
	Time int64  `json:"time"`
	Text string `json:"text"`
	Tone string `json:"tone"`
}

func historyLinesJSON(list []session.HistoryLine) []historyLineJSON {
	out := make([]historyLineJSON, 0, len(list))
	for _, l := range list {
		out = append(out, historyLineJSON{Time: millis(l.Time), Text: l.Text, Tone: l.Tone})
	}
	return out
}

func auditEntriesJSON(list []session.AuditEntry) []auditEntryJSON {
	entries := make([]auditEntryJSON, 0, len(list))
	for _, e := range list {
		entries = append(entries, auditEntryJSON{Time: millis(e.Time), Kind: e.Kind, By: e.By, Result: e.Result, Reason: e.Reason, PersonReason: e.PersonReason,
			Commit: e.Commit, Branch: e.Branch, Reviewer: e.Reviewer, Verdict: e.Verdict, Path: e.Path, Missing: nonNil(e.Missing),
			Session: e.Session, Box: e.Box, From: e.From, FromRef: e.FromRef, Source: e.Source, Person: e.Person, Req: e.Req, Hash: e.Hash})
	}
	return entries
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
	if advice.cloud {
		s.cfg.Audit.Refused(sid, session.RefusedCloud)
	}
}

type adviceKinds struct {
	pushes    bool
	remote    bool
	protected bool
	cloud     bool
}
