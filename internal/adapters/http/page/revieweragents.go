package page

import (
	"net/http"
	"strconv"

	"github.com/bezmoradi/software-factory/internal/core/review"
	"github.com/bezmoradi/software-factory/internal/core/session"
)

const reviewerAgentsRoute = "/api/reviewer-agents"

type agentFileViewJSON struct {
	Name        string   `json:"name"`
	Agent       string   `json:"agent"`
	Description string   `json:"description"`
	Model       string   `json:"model"`
	Tools       []string `json:"tools"`
	Bytes       int      `json:"bytes"`
	SHA         string   `json:"sha"`
	At          int64    `json:"at"`
}

type reviewerAgentsJSON struct {
	Files     []agentFileViewJSON `json:"files"`
	Roles     map[string]string   `json:"roles"`
	Defaults  map[string]string   `json:"defaults"`
	RoleNames map[string]string   `json:"roleNames"`
	Limit     int                 `json:"limit"`
	MaxBytes  int                 `json:"maxBytes"`
	Tools     []string            `json:"tools"`
}

func reviewerAgentsOf(v session.ReviewerAgentsView) reviewerAgentsJSON {
	out := reviewerAgentsJSON{Files: []agentFileViewJSON{}, Roles: map[string]string{}, Defaults: map[string]string{}, RoleNames: map[string]string{},
		Limit: session.MaxProjectAgents, MaxBytes: session.MaxAgentFile, Tools: session.ReviewTools}
	for _, f := range v.Files {
		out.Files = append(out.Files, agentFileViewJSON{Name: f.Name, Agent: f.Agent, Description: f.Description, Model: f.Model,
			Tools: nonNil(f.Tools), Bytes: f.Bytes, SHA: f.SHA, At: millis(f.At)})
	}
	for _, role := range review.Roles {
		out.RoleNames[string(role)] = role.Name()
		out.Defaults[string(role)] = v.Defaults[role]
		out.Roles[string(role)] = v.Roles[role]
	}
	return out
}

func (s *Server) reviewerAgents(w http.ResponseWriter, body map[string]any) {
	if s.cfg.ReviewerAgents == nil {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine has no repositories, so it keeps no reviewer agent files."})
		return
	}
	repo, okPath := body["path"].(string)
	op, okOp := body["op"].(string)
	if !okPath || repo == "" || len(repo) > session.MaxCwd || !okOp {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	var v session.ReviewerAgentsView
	var err error
	switch op {
	case "list":
		if !keysWithin(body, "op", "path") {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		v, err = s.cfg.ReviewerAgents.AgentFiles(repo)
	case "add":
		text, ok := body["text"].(string)
		if !keysWithin(body, "op", "path", "text") || !ok {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		if len(text) > session.MaxAgentFile {
			send(w, http.StatusRequestEntityTooLarge, problem{Error: "agent file", Message: "The engine refused this agent file: it is over " + strconv.Itoa(session.MaxAgentFile>>10) + " KiB."})
			return
		}
		v, err = s.cfg.ReviewerAgents.AddAgentFile(repo, text)
	case "remove":
		name, ok := body["name"].(string)
		if !keysWithin(body, "op", "path", "name") || !ok || name == "" || len(name) > session.MaxAgentName {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		v, err = s.cfg.ReviewerAgents.RemoveAgentFile(repo, name)
	case "role":
		name, okName := body["name"].(string)
		role, okRole := body["role"].(string)
		if !keysWithin(body, "op", "path", "role", "name") || !okName || !okRole || len(name) > session.MaxAgentName || len(role) > 40 {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		v, err = s.cfg.ReviewerAgents.PickAgentFile(repo, review.Role(role), name)
	default:
		send(w, http.StatusBadRequest, problem{Error: "op", Message: "The engine lists, adds, removes and picks reviewer agent files only."})
		return
	}
	if op != "list" {
		s.logger.Info("reviewer agent file", "op", op, "ok", err == nil)
	}
	if err != nil {
		code, p := status(err)
		send(w, code, p)
		return
	}
	send(w, http.StatusOK, reviewerAgentsOf(v))
}
