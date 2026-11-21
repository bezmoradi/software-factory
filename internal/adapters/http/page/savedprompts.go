package page

import (
	"net/http"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const savedPromptsRoute = "/api/saved-prompts"

type savedPromptJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Text string `json:"text"`
	At   int64  `json:"at"`
}

func (s *Server) UseSavedPrompts(p session.SavedPromptsPage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prompts = p
}

func (s *Server) savedPrompts(w http.ResponseWriter, body map[string]any) {
	s.mu.Lock()
	prompts := s.prompts
	s.mu.Unlock()
	if prompts == nil {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine has no repositories, so it keeps no saved prompts."})
		return
	}
	op, okOp := body["op"].(string)
	repo, okRepo := body["repo"].(string)
	if !okOp || !okRepo || len(repo) > maxRulesRepo || !rulesRepoName.MatchString(repo) {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	var list []session.SavedPrompt
	var err error
	switch op {
	case "list":
		if !keysWithin(body, "op", "repo") {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		list, err = prompts.Prompts(repo)
	case "save":
		id, name, text := "", "", ""
		okID := true
		if v, has := body["id"]; has {
			id, okID = v.(string)
			okID = okID && session.ValidPromptID(id)
		}
		name, okName := body["name"].(string)
		text, okText := body["text"].(string)
		if !keysWithin(body, "op", "repo", "id", "name", "text") || !okID || !okName || !okText {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		list, err = prompts.SavePrompt(repo, id, name, text)
		s.logger.Info("saved prompt", "op", "save", "ok", err == nil)
	case "remove":
		id, okID := body["id"].(string)
		if !keysWithin(body, "op", "repo", "id") || !okID || !session.ValidPromptID(id) {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		list, err = prompts.RemovePrompt(repo, id)
		s.logger.Info("saved prompt", "op", "remove", "ok", err == nil)
	default:
		send(w, http.StatusBadRequest, problem{Error: "op", Message: "The engine lists, saves and deletes saved prompts, nothing else."})
		return
	}
	if err != nil {
		code, p := status(err)
		send(w, code, p)
		return
	}
	out := make([]savedPromptJSON, 0, len(list))
	for _, p := range list {
		out = append(out, savedPromptJSON{ID: p.ID, Name: p.Name, Text: p.Text, At: millis(p.At)})
	}
	full := session.PromptsFull(len(list))
	send(w, http.StatusOK, map[string]any{"repo": repo, "prompts": out, "max": session.MaxSavedPrompts, "canAdd": full == "", "full": full})
}
