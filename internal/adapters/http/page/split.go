package page

import (
	"context"
	"net/http"
	"regexp"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	splitRoute     = "/split-request"
	splitPageRoute = "/api/split"
	MaxSplitBody   = 512 << 10
)

const (
	saidSplitSaved     = "Saved. The task starts with this title and message."
	saidSplitUnchanged = "No change: the task card already says this."
)

var splitCard = regexp.MustCompile(`^[0-9a-f]{32}$`)

type splitJSON struct {
	Card     string            `json:"card"`
	Title    string            `json:"title"`
	Message  string            `json:"message"`
	Project  *splitProjectJSON `json:"project,omitempty"`
	State    string            `json:"state"`
	SID      string            `json:"sid,omitempty"`
	Edited   bool              `json:"edited"`
	Asked    int64             `json:"asked"`
	At       int64             `json:"at"`
	SecretAt string            `json:"secretAt,omitempty"`
	Said     saidJSON          `json:"said"`
	Place    int               `json:"place,omitempty"`
	Note     string            `json:"note,omitempty"`
	Needs    *needsJSON        `json:"needs,omitempty"`
	Actions  map[string]string `json:"actions,omitempty"`
}

type splitProjectJSON struct {
	URL   string `json:"url"`
	Added bool   `json:"added"`
	Name  string `json:"name,omitempty"`
	Said  string `json:"said"`
}

type splitGoalJSON struct {
	Running int      `json:"running"`
	Limit   int      `json:"limit"`
	Queue   []string `json:"queue"`
}

func splitGoalOf(g *session.SplitGoal, cards []splitJSON) *splitGoalJSON {
	if g == nil || len(cards) == 0 {
		return nil
	}
	return &splitGoalJSON{Running: g.Running, Limit: g.Limit, Queue: nonNil(g.Queue)}
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
		j := splitJSON{Card: v.Card, Title: v.Title, State: v.State, SID: v.SID, Edited: v.Edited, Asked: millis(v.Asked), At: millis(v.At), SecretAt: v.SecretAt, Said: saidJSONOf(session.SplitSaid(v.State)),
			Place: v.Place, Note: v.Note, Needs: needsOf(v.Needs), Actions: v.Actions}
		if v.State != session.SplitStarted {
			j.Message = v.Message
		}
		if v.Project != "" {
			j.Project = &splitProjectJSON{URL: v.Project, Added: v.ProjectName != "", Name: v.ProjectName, Said: session.SplitProjectSaid(v.ProjectName)}
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
		agentRefuse(w, bad, "body")
		return
	}
	raw, ok := body["tasks"].([]any)
	if !keysWithin(body, "tasks") || !ok || len(raw) == 0 || len(raw) > session.MaxSplitTasks {
		agentRefuse(w, http.StatusBadRequest, "input")
		return
	}
	tasks := make([]session.SplitTask, 0, len(raw))
	for _, v := range raw {
		m, isObject := v.(map[string]any)
		title, okTitle := m["title"].(string)
		message, okMessage := m["message"].(string)
		project, okProject := m["project"].(string)
		if _, given := m["project"]; !given {
			okProject = true
		}
		if !isObject || !keysWithin(m, "title", "message", "project") || !okTitle || !okMessage || !okProject {
			agentRefuse(w, http.StatusBadRequest, "input")
			return
		}
		tasks = append(tasks, session.SplitTask{Title: title, Message: message, Project: project})
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
	shared.For(r.Context(), s.logger).Info("split request", "side", "agent", "tasks", len(tasks), "result", answer.Result)
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
		refuseFields(w)
		return
	}
	switch op {
	case "edit":
		title, okTitle := body["title"].(string)
		message, okMessage := body["message"].(string)
		if !keysWithin(body, "op", "sid", "card", "title", "message") || !okTitle || !okMessage {
			refuseFields(w)
			return
		}
		changed, err := splits.Edit(sid, card, title, message)
		shared.For(r.Context(), s.logger).Info("split edit", "ok", err == nil, "changed", changed)
		if err != nil {
			code, p := status(err)
			send(w, code, p)
			return
		}
		send(w, http.StatusOK, saveSaid(changed, saidSplitSaved, saidSplitUnchanged).ok())
	case "dismiss", "unqueue":
		if !keysWithin(body, "op", "sid", "card") {
			refuseFields(w)
			return
		}
		var err error
		if op == "dismiss" {
			err = splits.Dismiss(sid, card)
		} else {
			err = splits.Unqueue(sid, card)
		}
		shared.For(r.Context(), s.logger).Info("split "+op, "ok", err == nil)
		reply(w, err, http.StatusOK)
	case "start":
		consent, consented := consentOf(body)
		if !keysWithin(body, "op", "sid", "card", "approve", "costOk") || !consented {
			refuseFields(w)
			return
		}
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(dispatchFor + writeFor))
		ctx, cancel := context.WithTimeout(r.Context(), dispatchFor)
		defer cancel()
		started, err := splits.Start(ctx, sid, card, consent)
		shared.For(r.Context(), s.logger).Info("split start", "ok", err == nil, "adding", started.Adding != "")
		if err != nil {
			code, p := status(err)
			send(w, code, p)
			return
		}
		if started.Adding != "" {
			send(w, http.StatusAccepted, map[string]any{"ok": true, "adding": true, "message": started.Adding})
			return
		}
		send(w, http.StatusAccepted, map[string]any{"ok": true, "id": started.ID, "sid": started.SID})
	default:
		send(w, http.StatusBadRequest, problem{Error: "op", Message: "A task card is edited, dismissed, started or taken out of the queue, nothing else."})
	}
}
