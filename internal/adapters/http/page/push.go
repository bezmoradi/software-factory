package page

import (
	"context"
	"net/http"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/gate"
	"github.com/bezmoradi/software-factory/internal/core/push"
	"github.com/bezmoradi/software-factory/internal/core/session"
)

var writeTools = map[string]bool{"Write": true, "Edit": true, "MultiEdit": true, "NotebookEdit": true}

const (
	pushRoute     = "/push-request"
	MaxCheckBody  = 2 << 20
	maxCheckPaths = 64
	pushFor       = session.AgentReplyFor
)

func (s *Server) pushRequest(w http.ResponseWriter, r *http.Request) {
	body, bad := readObject(w, r, MaxAgentBody)
	if bad != 0 {
		refuse(w, bad, "body")
		return
	}
	if len(body) != 0 {
		refuse(w, http.StatusBadRequest, "input")
		return
	}
	tokens := r.Header.Values("X-SF-Push")
	token := ""
	if len(tokens) == 1 {
		token = tokens[0]
	}
	sid, err := s.reg.PushSession(token)
	if err != nil {
		s.agentError(w, r, err)
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(pushFor + writeFor))
	done := make(chan push.Reply, 1)
	go func() { done <- s.cfg.Push.Request(context.WithoutCancel(r.Context()), sid) }()
	var answer push.Reply
	select {
	case answer = <-done:
	case <-time.After(pushFor):
		answer = push.Reply{Decision: push.DecisionHeld, Reason: "the engine is still deciding or pushing this commit; end your turn and don't call factory_push again: Software Factory's page shows the result"}
	}
	s.logger.Info("push request", "side", "agent", "result", string(answer.Decision))
	send(w, http.StatusOK, map[string]string{"result": string(answer.Decision), "reason": answer.Reason})
}

func (s *Server) check(w http.ResponseWriter, r *http.Request) {
	sid, err := s.reg.Checking(connSecret(r))
	if err != nil {
		s.agentError(w, r, err)
		return
	}
	body, bad := readObject(w, r, MaxCheckBody)
	if bad != 0 {
		refuse(w, bad, "body")
		return
	}
	if !keysWithin(body, "tool", "input", "top", "paths") {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	tool, ok := body["tool"].(string)
	if !ok || tool == "" || len(tool) > 200 {
		refuse(w, http.StatusBadRequest, "tool")
		return
	}
	input := map[string]any{}
	if raw, present := body["input"]; present && raw != nil {
		m, ok := raw.(map[string]any)
		if !ok {
			refuse(w, http.StatusBadRequest, "input")
			return
		}
		input = m
	}
	top := ""
	if raw, present := body["top"]; present {
		t, ok := raw.(string)
		if !ok || len(t) > 4096 {
			refuse(w, http.StatusBadRequest, "top")
			return
		}
		top = t
	}
	var paths []string
	if raw, present := body["paths"]; present {
		list, ok := raw.([]any)
		if !ok || len(list) > maxCheckPaths {
			refuse(w, http.StatusBadRequest, "paths")
			return
		}
		for _, item := range list {
			p, ok := item.(string)
			if !ok || len(p) > 4096 {
				refuse(w, http.StatusBadRequest, "paths")
				return
			}
			paths = append(paths, p)
		}
	}
	advice := gate.Check(tool, input, top, paths, false)
	held := ""
	if s.cfg.Work != nil && advice.Protected == "" && !advice.Pushes && writeTools[tool] && len(paths) > 0 {
		held = s.cfg.Work.EditHold(sid, top, paths)
	}
	s.refused(sid, adviceKinds{pushes: advice.Pushes, remote: advice.Waits, protected: advice.Protected != ""})
	send(w, http.StatusOK, map[string]any{"kind": string(advice.Kind), "waits": advice.Waits, "pushes": advice.Pushes, "protected": advice.Protected, "held": held})
}
