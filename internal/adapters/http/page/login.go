package page

import (
	"net/http"
	"time"
)

const (
	loginRoute = "/api/claude-login"
	trustRoute = "/api/claude-trust"
)

type loginStateJSON struct {
	Agents          *signInJSON      `json:"agents,omitempty"`
	Reviewers       *signInJSON      `json:"reviewers,omitempty"`
	Reviewer        bool             `json:"reviewer"`
	AgentsExpect    string           `json:"agentsExpect"`
	ReviewersExpect string           `json:"reviewersExpect"`
	Each            []agentLoginJSON `json:"each"`
}

type agentLoginJSON struct {
	ID     string      `json:"id"`
	Name   string      `json:"name"`
	Step   string      `json:"step"`
	SignIn *signInJSON `json:"signIn,omitempty"`
}

func (s *Server) claudeLogin(w http.ResponseWriter, r *http.Request, body map[string]any) {
	if s.cfg.Logins == nil {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine has no agent-host, so it can't sign Claude Code in."})
		return
	}
	step, okStep := body["step"].(string)
	target, okTarget := body["target"].(string)
	code, okCode := body["code"].(string)
	if _, has := body["code"]; !has {
		okCode = true
	}
	if step == "key" {
		key, okKey := body["key"].(string)
		if !keysWithin(body, "step", "target", "key") || !okKey || !okTarget {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(time.Minute + writeFor))
		signedIn, err := s.cfg.Logins.KeyLogin(r.Context(), target, key)
		if err != nil {
			reply(w, err, http.StatusOK)
			return
		}
		send(w, http.StatusOK, map[string]any{"signIn": signInOf(signedIn)})
		return
	}
	if !keysWithin(body, "step", "target", "code") || !okStep || !okCode {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	if step == "status" {
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(time.Minute + writeFor))
		if _, has := body["target"]; has {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		st := s.cfg.Logins.LoginState(r.Context())
		each := make([]agentLoginJSON, 0, len(st.Each))
		for _, a := range st.Each {
			each = append(each, agentLoginJSON{ID: a.ID, Name: a.Name, Step: a.Step, SignIn: signInOf(a.SignIn)})
		}
		send(w, http.StatusOK, loginStateJSON{Agents: signInOf(st.Agents), Reviewers: signInOf(st.Reviewers), Reviewer: s.cfg.Reviews != nil, AgentsExpect: string(st.AgentsExpect), ReviewersExpect: string(st.ReviewersExpect), Each: each})
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(2*time.Minute + writeFor))
	if !okTarget || (step == "code") != (code != "") {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	link, signedIn, err := s.cfg.Logins.Login(r.Context(), target, step, code)
	if err != nil {
		reply(w, err, http.StatusOK)
		return
	}
	send(w, http.StatusOK, map[string]any{"url": link, "signIn": signInOf(signedIn)})
}

func (s *Server) claudeTrust(w http.ResponseWriter, r *http.Request, body map[string]any) {
	if s.cfg.Logins == nil {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine has no agent-host, so it can't answer Claude Code's trust question."})
		return
	}
	path, ok := body["path"].(string)
	if !keysWithin(body, "path") || !ok || path == "" || len(path) > 4096 {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(2*time.Minute + writeFor))
	reply(w, s.cfg.Logins.Trust(r.Context(), path), http.StatusOK)
}
