package state

import "github.com/bezmoradi/software-factory/internal/core/session"

const maxOrigin = 200

type stintJSON struct {
	Agent string `json:"agent"`
	SID   string `json:"sid"`
	Name  string `json:"name,omitempty"`
	From  string `json:"from"`
	To    string `json:"to"`
}

func stintOK(t stintJSON) bool {
	return agentID.MatchString(t.Agent) && sessionID.MatchString(t.SID) && cleanText(t.Name, maxDisplay) && commitID.MatchString(t.From) && commitID.MatchString(t.To)
}

func lineageInto(v *session.SavedRun, r runJSON) {
	if cleanText(r.Origin, maxOrigin) {
		v.Origin = r.Origin
	}
	for _, t := range r.Earlier {
		if len(v.Earlier) >= session.MaxStints {
			break
		}
		if stintOK(t) {
			v.Earlier = append(v.Earlier, session.Stint{Agent: t.Agent, SID: t.SID, Name: t.Name, From: t.From, To: t.To})
		}
	}
	if sessionID.MatchString(r.HandedTo) && commitID.MatchString(r.HandedHead) {
		v.HandedTo, v.HandedHead = r.HandedTo, r.HandedHead
	}
}

func lineageJSONInto(out *runJSON, r session.SavedRun) {
	if cleanText(r.Origin, maxOrigin) {
		out.Origin = r.Origin
	}
	for _, t := range r.Earlier {
		j := stintJSON{Agent: t.Agent, SID: t.SID, Name: t.Name, From: t.From, To: t.To}
		if len(out.Earlier) < session.MaxStints && stintOK(j) {
			out.Earlier = append(out.Earlier, j)
		}
	}
	if sessionID.MatchString(r.HandedTo) && commitID.MatchString(r.HandedHead) {
		out.HandedTo, out.HandedHead = r.HandedTo, r.HandedHead
	}
}
