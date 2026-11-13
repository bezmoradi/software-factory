package page

import (
	"context"
	"net/http"
	"regexp"
	"slices"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	dispatchFor = 105 * time.Second
	manageFor   = 45 * time.Second
	modeRefused = "The page starts every session in the agent's asking mode, never auto mode."
)

const (
	reviewRoute   = "/api/review"
	overrideRoute = "/api/override"
)

var starting = map[string]bool{"/api/dispatch": true, "/api/wake": true, "/api/stop-session": true, "/api/remove-session": true}

type starterJSON struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Method  string `json:"method"`
	Enabled bool   `json:"enabled"`
	Off     string `json:"off,omitempty"`
	Expect  string `json:"expect"`
}

type projectJSON struct {
	Path         string   `json:"path"`
	Name         string   `json:"name"`
	URL          string   `json:"url"`
	Branch       string   `json:"branch"`
	AddedAt      int64    `json:"addedAt"`
	Agents       []string `json:"agents"`
	Rounds       int      `json:"rounds"`
	Type         string   `json:"type"`
	Instructions []string `json:"instructions"`
	AutoFindings bool     `json:"autoFindings"`
	PRComment    bool     `json:"prComment"`
	Hold         holdJSON `json:"hold"`
}

func (s *Server) projects(w http.ResponseWriter) {
	out := map[string]any{"projects": []any{}, "cloning": []any{}, "hosts": []any{}, "repos": s.cfg.Repos != nil, "starters": []any{}, "reviewer": nil}
	if s.cfg.Repos == nil {
		send(w, http.StatusOK, out)
		return
	}
	list, err := s.cfg.Repos.List()
	if err != nil {
		send(w, http.StatusServiceUnavailable, problem{Error: "records", Message: "The engine couldn't read its list of repositories."})
		return
	}
	view, err := s.cfg.Repos.View()
	if err != nil {
		send(w, http.StatusServiceUnavailable, problem{Error: "tokens", Message: "The engine couldn't read its saved tokens."})
		return
	}
	projects := make([]projectJSON, 0, len(list))
	for _, r := range list {
		p := projectJSON{Path: r.Path, Name: r.Name, URL: r.URL, Branch: r.Branch, AddedAt: millis(r.AddedAt), Agents: []string{}, Rounds: session.DefaultRounds, Instructions: []string{}}
		if s.cfg.Work != nil {
			info := s.cfg.Work.Project(r.Name)
			p.Rounds, p.Type, p.Instructions, p.PRComment = info.Rounds, info.Type, nonNil(info.Instructions), info.PRComment
			p.AutoFindings = info.AutoFindings
			p.Hold = holdJSONOf(info.Hold)
		}
		projects = append(projects, p)
	}
	cloning := make([]cloningJSON, 0, len(view.Cloning))
	for _, k := range view.Cloning {
		cloning = append(cloning, cloningJSON{URL: k.URL, Name: k.Name, StartedAt: millis(k.StartedAt)})
	}
	hosts := make([]hostJSON, 0, len(view.Hosts))
	for _, h := range view.Hosts {
		hosts = append(hosts, hostJSON{Host: h.Host, SavedAt: millis(h.SavedAt)})
	}
	starters := []starterJSON{}
	expect := map[string]session.Expect{}
	if s.cfg.Starter != nil {
		expect = s.cfg.Starter.Expectations()
		for _, a := range s.cfg.Starter.Agents() {
			x := session.ExpectAny
			if v, ok := expect[a.ID]; ok {
				x = v
			}
			starters = append(starters, starterJSON{ID: a.ID, Name: a.Name, Method: string(a.Method), Enabled: a.Enabled, Off: a.Off, Expect: string(x)})
		}
	}
	out["projects"], out["cloning"], out["hosts"], out["starters"], out["reviewer"] = projects, cloning, hosts, starters, s.reviewer(expect)
	send(w, http.StatusOK, out)
}

func (s *Server) manage(w http.ResponseWriter, r *http.Request, body map[string]any) {
	if s.cfg.Starter == nil {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine has no agent-host, so it can't start or manage sessions."})
		return
	}
	rc := http.NewResponseController(w)
	path := r.URL.Path
	limit := manageFor
	if path == "/api/dispatch" {
		limit = dispatchFor
	}
	_ = rc.SetWriteDeadline(time.Now().Add(limit + writeFor))
	ctx, cancel := context.WithTimeout(r.Context(), limit)
	defer cancel()
	sid := str(body["sid"], 80)
	switch path {
	case "/api/dispatch":
		if !keysWithin(body, "path", "name", "model", "mode", "agent", "type", "approve", "costOk") {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		for _, k := range []string{"path", "name", "model", "mode", "agent", "type"} {
			if v, ok := body[k]; ok {
				if _, isText := v.(string); !isText {
					refuse(w, http.StatusBadRequest, k)
					return
				}
			}
		}
		mode := str(body["mode"], 40)
		if !slices.Contains([]string{"", "default", "manual"}, mode) {
			send(w, http.StatusBadRequest, problem{Error: "mode", Message: modeRefused})
			return
		}
		consent, ok := consentOf(body)
		if !ok {
			refuse(w, http.StatusBadRequest, "consent")
			return
		}
		agent := str(body["agent"], 40)
		if agent == "" {
			agent = "claude"
		}
		started, err := s.cfg.Starter.Dispatch(ctx, session.Dispatch{
			Path: str(body["path"], session.MaxCwd+1), Agent: agent, Name: str(body["name"], 400), Model: str(body["model"], 40), Type: str(body["type"], 40), Consent: consent,
		})
		s.logger.Info("dispatch", "agent", agent, "ok", err == nil)
		if err != nil {
			code, p := status(err)
			send(w, code, p)
			return
		}
		send(w, http.StatusAccepted, map[string]any{"ok": true, "id": started.ID, "sid": started.SID})
	case "/api/stop-session":
		if !keysWithin(body, "sid") {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		err := s.cfg.Starter.Stop(ctx, sid)
		s.logger.Info("stop-session", "ok", err == nil)
		reply(w, err, http.StatusAccepted)
	case "/api/wake":
		consent, ok := consentOf(body)
		if !keysWithin(body, "sid", "approve", "costOk") || !ok {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		err := s.cfg.Starter.Wake(ctx, sid, consent)
		s.logger.Info("wake", "ok", err == nil)
		reply(w, err, http.StatusAccepted)
	case "/api/remove-session":
		if !keysWithin(body, "sid", "flag", "value") {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		var given *session.Offer
		if flag, ok := body["flag"]; ok {
			f, isFlag := flag.(string)
			v, isValue := body["value"].(string)
			if !isFlag || !isValue || !slices.Contains([]string{"discard", "force", "worktree"}, f) || len(v) > 200 {
				refuse(w, http.StatusBadRequest, "offer")
				return
			}
			given = &session.Offer{Flag: f, Value: v}
		}
		offer, err := s.cfg.Starter.Remove(ctx, sid, given)
		s.logger.Info("remove-session", "ok", err == nil)
		if err != nil {
			code, p := status(err)
			out := map[string]any{"error": p.Error, "message": p.Message, "offer": nil}
			if offer != nil {
				out["offer"] = map[string]string{"flag": offer.Flag, "value": offer.Value}
			}
			send(w, code, out)
			return
		}
		send(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func (s *Server) review(w http.ResponseWriter, r *http.Request, body map[string]any) {
	if s.cfg.Reviews == nil {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine has no reviewer container, so it can't run reviews."})
		return
	}
	sid, ok := body["sid"].(string)
	if r.URL.Path == overrideRoute {
		s.override(w, r, sid, ok, body)
		return
	}
	consent, consented := consentOf(body)
	if !keysWithin(body, "sid", "costOk") || !ok || !consented || sid == "" || len(sid) > 80 {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(manageFor + writeFor))
	ctx, cancel := context.WithTimeout(r.Context(), manageFor)
	defer cancel()
	err := s.cfg.Reviews.Review(ctx, sid, consent.CostOK)
	s.logger.Info("review", "ok", err == nil)
	reply(w, err, http.StatusAccepted)
}

var agentName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,119}$`)

func (s *Server) override(w http.ResponseWriter, r *http.Request, sid string, ok bool, body map[string]any) {
	agent, okAgent := body["agent"].(string)
	commit, okCommit := body["commit"].(string)
	reason, okReason := body["reason"].(string)
	if !keysWithin(body, "sid", "agent", "commit", "reason") || !ok || sid == "" || len(sid) > 80 || !okAgent || !agentName.MatchString(agent) ||
		!okCommit || !commitHex.MatchString(commit) || !okReason || len(reason) > 2000 {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(manageFor + writeFor))
	ctx, cancel := context.WithTimeout(r.Context(), manageFor)
	defer cancel()
	err := s.cfg.Reviews.Override(ctx, sid, agent, commit, reason)
	s.logger.Info("override", "ok", err == nil)
	reply(w, err, http.StatusOK)
}
