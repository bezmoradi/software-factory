package page

import (
	"context"
	"net/http"
	"regexp"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/review"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	sessionWorkRoute     = "/api/session-work"
	projectCommandsRoute = "/api/project-commands"
	reviewModelsRoute    = "/api/review-models"
	turnCheckRoute       = "/turn-check"
	sessionModelRoute    = "/session-model"
	sessionWorkFor       = 40 * time.Second
	maxQueueText         = 80 << 10
)

var (
	runRoutes = map[string]bool{sessionWorkRoute: true, projectCommandsRoute: true, reviewModelsRoute: true}
	queueID   = regexp.MustCompile(`^[0-9a-f]{32}$`)
	modelText = regexp.MustCompile(`^[A-Za-z0-9._:/-]{0,100}$`)
)

func (s *Server) UseSessionWork(w *session.SessionWork) {
	s.work = w
}

type commandRunJSON struct {
	Kind     string `json:"kind"`
	State    string `json:"state"`
	Code     int    `json:"code"`
	Tail     string `json:"tail"`
	Cut      bool   `json:"cut"`
	Withheld bool   `json:"withheld"`
	Reason   string `json:"reason"`
	Retry    bool   `json:"retry"`
	Started  int64  `json:"started"`
	Ended    int64  `json:"ended"`
}

type queuedJSON struct {
	ID   string `json:"id"`
	Text string `json:"text"`
	At   int64  `json:"at"`
}

type queueViewJSON struct {
	Items   []queuedJSON `json:"items"`
	Stopped string       `json:"stopped"`
	Waiting string       `json:"waiting"`
	Sending bool         `json:"sending"`
	Max     int          `json:"max"`
}

type agentModelJSON struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Efforts []string `json:"efforts"`
}

type sessionModelJSON struct {
	Start       string           `json:"start"`
	StartEffort string           `json:"startEffort"`
	Model       string           `json:"model"`
	Effort      string           `json:"effort"`
	Now         string           `json:"now"`
	NowEffort   string           `json:"nowEffort"`
	From        string           `json:"from"`
	Offered     []agentModelJSON `json:"offered"`
}

type portsJSON struct {
	First int  `json:"first"`
	Last  int  `json:"last"`
	Env   bool `json:"env"`
}

type sessionWorkJSON struct {
	Agent    string           `json:"agent"`
	Model    sessionModelJSON `json:"model"`
	Queue    queueViewJSON    `json:"queue"`
	Check    *commandRunJSON  `json:"check"`
	Setup    *commandRunJSON  `json:"setup"`
	Ports    *portsJSON       `json:"ports"`
	HasCheck bool             `json:"hasCheck"`
	HasSetup bool             `json:"hasSetup"`
}

func runJSONOf(r *session.CommandRun) *commandRunJSON {
	if r == nil {
		return nil
	}
	return &commandRunJSON{Kind: r.Kind, State: r.State, Code: r.Code, Tail: r.Tail, Cut: r.Cut, Withheld: r.Withheld, Reason: r.Reason, Retry: r.Retry, Started: millis(r.Started), Ended: millis(r.Ended)}
}

func modelsJSONOf(list []session.AgentModel) []agentModelJSON {
	out := make([]agentModelJSON, 0, len(list))
	for _, m := range list {
		out = append(out, agentModelJSON{ID: m.ID, Name: m.Name, Efforts: nonNil(m.Efforts)})
	}
	return out
}

func workJSONOf(v session.SessionWorkView) sessionWorkJSON {
	now := v.Model.Now()
	out := sessionWorkJSON{Agent: v.Agent,
		Model: sessionModelJSON{Start: v.Model.StartModel, StartEffort: v.Model.StartEffort, Model: v.Model.Model, Effort: v.Model.Effort,
			Now: now.Model, NowEffort: now.Effort, From: session.ModelsFrom, Offered: modelsJSONOf(v.Offered)},
		Queue:    queueViewJSON{Items: []queuedJSON{}, Stopped: v.Queue.Stopped, Waiting: v.Queue.Waiting, Sending: v.Queue.Sending, Max: session.MaxQueue},
		Check:    runJSONOf(v.Check),
		Setup:    runJSONOf(v.Setup),
		HasCheck: v.Commands.Check != "",
		HasSetup: v.Commands.Setup != "",
	}
	for _, m := range v.Queue.Items {
		out.Queue.Items = append(out.Queue.Items, queuedJSON{ID: m.ID, Text: m.Text, At: millis(m.At)})
	}
	if v.Ports > 0 {
		out.Ports = &portsJSON{First: v.Ports, Last: v.Ports + session.PortSpan - 1, Env: v.PortsEnv}
	}
	return out
}

func (s *Server) runs(w http.ResponseWriter, r *http.Request, body map[string]any) {
	if s.work == nil {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine has no agent-host, so it runs no project commands, queues or model switches."})
		return
	}
	op, okOp := body["op"].(string)
	if !okOp {
		refuse(w, http.StatusBadRequest, "op")
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(sessionWorkFor + writeFor))
	ctx, cancel := context.WithTimeout(r.Context(), sessionWorkFor)
	defer cancel()
	switch r.URL.Path {
	case sessionWorkRoute:
		s.sessionWork(ctx, w, op, body)
	case projectCommandsRoute:
		s.projectCommands(w, op, body)
	case reviewModelsRoute:
		s.reviewModels(w, op, body)
	default:
		refuse(w, http.StatusNotFound, "route")
	}
}

func (s *Server) sessionWork(ctx context.Context, w http.ResponseWriter, op string, body map[string]any) {
	sid, okSID := viewSession(body)
	id, okID := body["id"].(string)
	okID = okID && queueID.MatchString(id)
	text, okText := body["text"].(string)
	okText = okText && len(text) <= maxQueueText
	var err error
	switch op {
	case "read":
		if !keysWithin(body, "op", "sid") || !okSID {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
	case "model":
		model, okModel := body["model"].(string)
		effort, okEffort := body["effort"].(string)
		if !keysWithin(body, "op", "sid", "model", "effort") || !okSID || !okModel || !okEffort || !modelText.MatchString(model) || len(effort) > 20 {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		err = s.work.SwitchModel(ctx, sid, model, effort)
	case "queue-add":
		if !keysWithin(body, "op", "sid", "text") || !okSID || !okText {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		err = s.work.QueueAdd(sid, text)
	case "queue-edit":
		if !keysWithin(body, "op", "sid", "id", "text") || !okSID || !okID || !okText {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		err = s.work.QueueEdit(sid, id, text)
	case "queue-move":
		to, okTo := integer(body["to"])
		if !keysWithin(body, "op", "sid", "id", "to") || !okSID || !okID || !okTo || to < 0 || to >= session.MaxQueue {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		err = s.work.QueueMove(sid, id, int(to))
	case "queue-remove":
		if !keysWithin(body, "op", "sid", "id") || !okSID || !okID {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		err = s.work.QueueRemove(sid, id)
	case "queue-resume":
		if !keysWithin(body, "op", "sid") || !okSID {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		err = s.work.QueueResume(sid)
	default:
		send(w, http.StatusBadRequest, problem{Error: "op", Message: "The engine reads a session's work, switches its model and changes its queue only."})
		return
	}
	if op != "read" {
		s.logger.Info("session work", "op", op, "ok", err == nil)
	}
	if err != nil {
		code, p := status(err)
		send(w, code, p)
		return
	}
	v, err := s.work.View(sid)
	if err != nil {
		code, p := status(err)
		send(w, code, p)
		return
	}
	send(w, http.StatusOK, workJSONOf(v))
}

func (s *Server) projectCommands(w http.ResponseWriter, op string, body map[string]any) {
	name, okName := body["name"].(string)
	if !okName || name == "" || len(name) > 400 {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	switch op {
	case "read":
		if !keysWithin(body, "op", "name") {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		if !s.knownProject(name) {
			send(w, http.StatusNotFound, problem{Error: "project", Message: "The engine has no project of that name."})
			return
		}
	case "set":
		check, okCheck := body["check"].(string)
		setup, okSetup := body["setup"].(string)
		image, okImage := s.work.Commands(name).Image, true
		if raw, given := body["image"]; given {
			image, okImage = raw.(string)
		}
		if !keysWithin(body, "op", "name", "check", "setup", "image") || !okCheck || !okSetup || !okImage || len(check) > session.MaxProjectCommand+64 || len(setup) > session.MaxProjectCommand+64 || len(image) > session.MaxVerifyImage+64 {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		err := s.work.SetCommands(name, session.ProjectCommands{Check: check, Setup: setup, Image: image})
		s.logger.Info("project commands", "op", op, "ok", err == nil)
		if err != nil {
			code, p := status(err)
			send(w, code, p)
			return
		}
	default:
		send(w, http.StatusBadRequest, problem{Error: "op", Message: "The engine reads and sets a project's commands only."})
		return
	}
	c := s.work.Commands(name)
	send(w, http.StatusOK, map[string]any{"check": c.Check, "setup": c.Setup, "image": c.Image, "max": session.MaxProjectCommand, "maxImage": session.MaxVerifyImage})
}

type roleModelJSON struct {
	Name   string `json:"name"`
	Model  string `json:"model"`
	Effort string `json:"effort"`
}

func (s *Server) reviewModels(w http.ResponseWriter, op string, body map[string]any) {
	path, okPath := body["path"].(string)
	if !okPath || path == "" || len(path) > session.MaxCwd {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	var roles map[review.Role]session.ModelChoice
	var err error
	switch op {
	case "read":
		if !keysWithin(body, "op", "path") {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		roles, err = s.work.RoleModels(path)
	case "set":
		role, okRole := body["role"].(string)
		model, okModel := body["model"].(string)
		effort, okEffort := body["effort"].(string)
		if !keysWithin(body, "op", "path", "role", "model", "effort") || !okRole || len(role) > 40 || !okModel || len(model) > 40 || !okEffort || len(effort) > 20 {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		roles, err = s.work.SetRoleModel(path, review.Role(role), model, effort)
		s.logger.Info("review models", "op", op, "ok", err == nil)
	default:
		send(w, http.StatusBadRequest, problem{Error: "op", Message: "The engine reads and sets review roles' models only."})
		return
	}
	if err != nil {
		code, p := status(err)
		send(w, code, p)
		return
	}
	out := map[string]roleModelJSON{}
	for _, role := range review.Roles {
		c := roles[role]
		out[string(role)] = roleModelJSON{Name: role.Name(), Model: c.Model, Effort: c.Effort}
	}
	send(w, http.StatusOK, map[string]any{"roles": out, "models": modelsJSONOf(session.ClaudeOffered()), "efforts": session.ClaudeEfforts})
}

func (s *Server) turnCheck(w http.ResponseWriter, r *http.Request) {
	sid, err := s.reg.Checking(connSecret(r))
	if err != nil {
		s.agentError(w, r, err)
		return
	}
	body, bad := readObject(w, r, MaxAgentBody)
	if bad != 0 {
		refuse(w, bad, "body")
		return
	}
	again, okAgain := body["again"].(bool)
	if _, given := body["again"]; !given {
		okAgain = true
	}
	if !keysWithin(body, "again") || !okAgain {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	if s.work == nil {
		send(w, http.StatusOK, map[string]any{"result": session.CheckNone, "block": false, "text": ""})
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(session.CheckFor + time.Minute + writeFor))
	answer := s.work.TurnCheck(context.WithoutCancel(r.Context()), sid, again)
	if answer.Result != session.CheckNone {
		shared.For(r.Context(), s.logger).Info("turn check", "side", "agent", "result", answer.Result, "block", answer.Block)
	}
	send(w, http.StatusOK, map[string]any{"result": answer.Result, "block": answer.Block, "text": answer.Text})
}

func (s *Server) sessionModel(w http.ResponseWriter, r *http.Request) {
	sid, err := s.reg.Checking(connSecret(r))
	if err != nil {
		s.agentError(w, r, err)
		return
	}
	c := session.ModelChoice{}
	if s.work != nil {
		c = s.work.ClaudeOverride(sid)
	}
	send(w, http.StatusOK, map[string]string{"model": c.Model, "effort": c.Effort})
}
