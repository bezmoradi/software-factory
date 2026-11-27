package page

import (
	"net/http"
	"regexp"

	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	copyRoute       = "/copy-request"
	copiesRoute     = "/api/copies"
	MaxCopyBody     = 8 << 10
	maxCopyAddress  = 2048
	maxCopyRefBytes = 512
)

var copyCard = regexp.MustCompile(`^[0-9a-f]{32}$`)

type copyCardJSON struct {
	Card      string            `json:"card"`
	URL       string            `json:"url"`
	Ref       string            `json:"ref,omitempty"`
	Host      string            `json:"host"`
	Path      string            `json:"path"`
	Token     bool              `json:"token"`
	TokenSaid string            `json:"tokenSaid"`
	Risk      string            `json:"risk"`
	State     string            `json:"state"`
	Said      saidJSON          `json:"said"`
	Note      string            `json:"note"`
	Commit    string            `json:"commit,omitempty"`
	Asked     int64             `json:"asked"`
	At        int64             `json:"at"`
	Actions   map[string]string `json:"actions"`
}

func copiesOf(list []session.CopyView) []copyCardJSON {
	if len(list) == 0 {
		return nil
	}
	out := make([]copyCardJSON, 0, len(list))
	for _, v := range list {
		out = append(out, copyCardJSON{Card: v.Card, URL: v.URL, Ref: v.Ref, Host: v.Host, Path: v.Path, Token: v.Token, TokenSaid: v.TokenSaid, Risk: v.Risk,
			State: v.State, Said: saidJSONOf(v.Said), Note: v.Note, Commit: v.Commit, Asked: millis(v.Asked), At: millis(v.At), Actions: nonNilActions(v.Actions)})
	}
	return out
}

func nonNilActions(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func (s *Server) UseCopies(copies *session.Copies) {
	s.mu.Lock()
	s.copies = copies
	s.mu.Unlock()
}

func (s *Server) copiesPart() *session.Copies {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.copies
}

func (s *Server) copyRequest(w http.ResponseWriter, r *http.Request) {
	body, bad := readObject(w, r, MaxCopyBody)
	if bad != 0 {
		agentRefuse(w, bad, "body")
		return
	}
	address, okURL := body["url"].(string)
	ref, okRef := body["ref"].(string)
	if _, given := body["ref"]; !given {
		okRef = true
	}
	if !keysWithin(body, "url", "ref") || !okURL || !okRef || len(address) > maxCopyAddress || len(ref) > maxCopyRefBytes {
		agentRefuse(w, http.StatusBadRequest, "input")
		return
	}
	sid, ok := s.forgeSession(w, r)
	if !ok {
		return
	}
	copies := s.copiesPart()
	if copies == nil {
		send(w, http.StatusOK, map[string]string{"result": session.CopyRefused, "reason": "this engine makes no copies of other repositories"})
		return
	}
	answer := copies.Request(sid, address, ref)
	shared.For(r.Context(), s.logger).Info("copy request", "side", "agent", "result", answer.Result)
	send(w, http.StatusOK, map[string]string{"result": answer.Result, "reason": answer.Reason})
}

func (s *Server) copiesPage(w http.ResponseWriter, r *http.Request, body map[string]any) {
	copies := s.copiesPart()
	if copies == nil {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine makes no copies of other repositories."})
		return
	}
	op, okOp := body["op"].(string)
	sid, okSID := viewSession(body)
	card, okCard := body["card"].(string)
	if !keysWithin(body, "op", "sid", "card") || !okOp || !okSID || !okCard || !copyCard.MatchString(card) {
		refuseFields(w)
		return
	}
	var said string
	var err error
	done := http.StatusOK
	switch op {
	case session.CopyApprove:
		said, err = copies.Approve(sid, card)
		done = http.StatusAccepted
	case session.CopyDecline:
		said, err = copies.Decline(sid, card)
	case session.CopyRemove:
		said, err = copies.Remove(sid, card)
	default:
		send(w, http.StatusBadRequest, problem{Error: "op", Message: "A copy is approved, declined or removed, nothing else."})
		return
	}
	shared.For(r.Context(), s.logger).Info("copy "+op, "ok", err == nil)
	if err != nil {
		code, p := status(err)
		send(w, code, p)
		return
	}
	send(w, done, map[string]any{"ok": true, "message": said})
}
