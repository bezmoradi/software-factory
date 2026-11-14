package page

import (
	"context"
	"net/http"
	"regexp"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	splitRoute     = "/split-request"
	splitPageRoute = "/api/split"
	MaxSplitBody   = 512 << 10
)

var splitCard = regexp.MustCompile(`^[0-9a-f]{32}$`)

type splitJSON struct {
	Card     string `json:"card"`
	Title    string `json:"title"`
	Message  string `json:"message"`
	State    string `json:"state"`
	SID      string `json:"sid,omitempty"`
	Edited   bool   `json:"edited"`
	Asked    int64  `json:"asked"`
	At       int64  `json:"at"`
	SecretAt string `json:"secretAt,omitempty"`
}

func splitsOf(list []session.SplitView) []splitJSON {
	if len(list) == 0 {
		return nil
	}
	out := make([]splitJSON, 0, len(list))
	for _, v := range list {
		if v.State == session.SplitDismissed {
			continue
		}
		j := splitJSON{Card: v.Card, Title: v.Title, State: v.State, SID: v.SID, Edited: v.Edited, Asked: millis(v.Asked), At: millis(v.At), SecretAt: v.SecretAt}
		if v.State != session.SplitStarted {
			j.Message = v.Message
		}
		out = append(out, j)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (s *Server) UseSplits(splits *session.Splits) {
	s.mu.Lock()
	s.splits = splits
	s.mu.Unlock()
}

func (s *Server) splitsPart() *session.Splits {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.splits
}

func (s *Server) splitRequest(w http.ResponseWriter, r *http.Request) {
	body, bad := readObject(w, r, MaxSplitBody)
	if bad != 0 {
		refuse(w, bad, "body")
		return
	}
	raw, ok := body["tasks"].([]any)
	if !keysWithin(body, "tasks") || !ok || len(raw) == 0 || len(raw) > session.MaxSplitTasks {
		refuse(w, http.StatusBadRequest, "input")
		return
	}
	tasks := make([]session.SplitTask, 0, len(raw))
	for _, v := range raw {
		m, isObject := v.(map[string]any)
		title, okTitle := m["title"].(string)
		message, okMessage := m["message"].(string)
		if !isObject || !keysWithin(m, "title", "message") || !okTitle || !okMessage {
			refuse(w, http.StatusBadRequest, "input")
			return
		}
		tasks = append(tasks, session.SplitTask{Title: title, Message: message})
	}
	sid, ok := s.forgeSession(w, r)
	if !ok {
		return
	}
	splits := s.splitsPart()
	if splits == nil {
		send(w, http.StatusOK, map[string]string{"result": session.SplitRefused, "reason": "this engine doesn't take task proposals"})
		return
	}
	answer := splits.Request(sid, tasks)
	s.logger.Info("split request", "side", "agent", "tasks", len(tasks), "result", answer.Result)
	send(w, http.StatusOK, map[string]string{"result": answer.Result, "reason": answer.Reason})
}

func (s *Server) splitPage(w http.ResponseWriter, r *http.Request, body map[string]any) {
	splits := s.splitsPart()
	if splits == nil {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine doesn't take task proposals."})
		return
	}
	op, okOp := body["op"].(string)
	sid, okSID := viewSession(body)
	card, okCard := body["card"].(string)
	if !okOp || !okSID || !okCard || !splitCard.MatchString(card) {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	switch op {
	case "edit":
		title, okTitle := body["title"].(string)
		message, okMessage := body["message"].(string)
		if !keysWithin(body, "op", "sid", "card", "title", "message") || !okTitle || !okMessage {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		err := splits.Edit(sid, card, title, message)
		s.logger.Info("split edit", "ok", err == nil)
		reply(w, err, http.StatusOK)
	case "dismiss":
		if !keysWithin(body, "op", "sid", "card") {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		err := splits.Dismiss(sid, card)
		s.logger.Info("split dismiss", "ok", err == nil)
		reply(w, err, http.StatusOK)
	case "start":
		consent, consented := consentOf(body)
		if !keysWithin(body, "op", "sid", "card", "approve", "costOk") || !consented {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(dispatchFor + writeFor))
		ctx, cancel := context.WithTimeout(r.Context(), dispatchFor)
		defer cancel()
		started, err := splits.Start(ctx, sid, card, consent)
		s.logger.Info("split start", "ok", err == nil)
		if err != nil {
			code, p := status(err)
			send(w, code, p)
			return
		}
		send(w, http.StatusAccepted, map[string]any{"ok": true, "id": started.ID, "sid": started.SID})
	default:
		send(w, http.StatusBadRequest, problem{Error: "op", Message: "A task card is edited, dismissed or started, nothing else."})
	}
}
