package page

import (
	"context"
	"net/http"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	forgeRoute       = "/forge-request"
	checksRoute      = "/checks-request"
	forgeAnswerRoute = "/api/forge-answer"
	MaxForgeBody     = 256 << 10
)

type forgeJSON struct {
	Card   string `json:"card"`
	Kind   string `json:"kind"`
	State  string `json:"state"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	Head   string `json:"head"`
	Base   string `json:"base"`
	Host   string `json:"host"`
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	URL    string `json:"url"`
	Reason string `json:"reason"`
	Draft  bool   `json:"draft"`
	Asked  int64  `json:"asked"`
	At     int64  `json:"at"`

	Matches []matchJSON `json:"matches"`
}

func forgeOf(list []session.ForgeView) []forgeJSON {
	out := make([]forgeJSON, 0, len(list))
	for _, v := range list {
		out = append(out, forgeJSON{Card: v.Card, Kind: string(v.Kind), State: v.State, Title: v.Title, Body: v.Body, Head: v.Head, Base: v.Base,
			Host: v.Host, Repo: v.Repo, Number: v.Number, URL: v.URL, Reason: v.Reason, Draft: v.Draft, Asked: millis(v.Asked), At: millis(v.At), Matches: matchesOf(v.Matches)})
	}
	return out
}

func (s *Server) forgeSession(w http.ResponseWriter, r *http.Request) (string, bool) {
	tokens := r.Header.Values("X-SF-Push")
	token := ""
	if len(tokens) == 1 {
		token = tokens[0]
	}
	sid, err := s.reg.PushSession(token)
	if err != nil {
		s.agentError(w, r, err)
		return "", false
	}
	return sid, true
}

func (s *Server) forgeRequest(w http.ResponseWriter, r *http.Request) {
	body, bad := readObject(w, r, MaxForgeBody)
	if bad != 0 {
		refuse(w, bad, "body")
		return
	}
	kind, okKind := body["kind"].(string)
	title, okTitle := body["title"].(string)
	text, okText := body["body"].(string)
	if !keysWithin(body, "kind", "title", "body") || !okKind || !okTitle || !okText || (kind != string(session.ForgeIssue) && kind != string(session.ForgePull)) {
		refuse(w, http.StatusBadRequest, "input")
		return
	}
	sid, ok := s.forgeSession(w, r)
	if !ok {
		return
	}
	if s.cfg.Forge == nil {
		send(w, http.StatusOK, map[string]string{"result": session.ForgeRefused, "reason": "this engine has no git host API set up"})
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(session.ForgeFor + writeFor))
	answer := s.cfg.Forge.Request(context.WithoutCancel(r.Context()), sid, session.ForgeKind(kind), title, text)
	s.logger.Info("forge request", "side", "agent", "kind", kind, "result", answer.Result)
	send(w, http.StatusOK, map[string]string{"result": answer.Result, "reason": answer.Reason})
}

func (s *Server) checksRequest(w http.ResponseWriter, r *http.Request) {
	body, bad := readObject(w, r, MaxAgentBody)
	if bad != 0 {
		refuse(w, bad, "body")
		return
	}
	if len(body) != 0 {
		refuse(w, http.StatusBadRequest, "input")
		return
	}
	sid, ok := s.forgeSession(w, r)
	if !ok {
		return
	}
	if s.cfg.Forge == nil {
		send(w, http.StatusOK, map[string]string{"result": session.ForgeRefused, "reason": "this engine has no git host API set up"})
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(session.ForgeFor + writeFor))
	answer := s.cfg.Forge.Checks(context.WithoutCancel(r.Context()), sid)
	s.logger.Info("checks request", "side", "agent", "result", answer.Result)
	send(w, http.StatusOK, map[string]string{"result": answer.Result, "reason": answer.Reason})
}

func (s *Server) forgeAnswer(w http.ResponseWriter, body map[string]any) {
	if s.cfg.Forge == nil {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine has no git host API set up."})
		return
	}
	sid, okSID := body["sid"].(string)
	card, okCard := body["card"].(string)
	approve, okApprove := body["approve"].(bool)
	draft, okDraft := body["draft"].(bool)
	if _, given := body["draft"]; !given {
		okDraft = true
	}
	if !keysWithin(body, "sid", "card", "approve", "draft") || !okSID || sid == "" || len(sid) > 80 || !okCard || !pushCard.MatchString(card) || !okApprove || !okDraft {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	err := s.cfg.Forge.Answer(context.Background(), sid, card, approve, draft)
	s.logger.Info("forge answer", "approve", approve, "draft", draft, "ok", err == nil)
	reply(w, err, http.StatusAccepted)
}
