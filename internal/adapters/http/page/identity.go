package page

import (
	"net/http"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const identityRoute = "/api/commit-identity"

func identityJSON(id session.CommitIdentity) map[string]any {
	if !id.Set() {
		return map[string]any{"identity": nil}
	}
	return map[string]any{"identity": map[string]string{"name": id.Name, "email": id.Email}}
}

func (s *Server) commitIdentity(w http.ResponseWriter, body map[string]any) {
	if s.cfg.Identity == nil {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine has no agent-host, so it starts no agents to commit with a name and email."})
		return
	}
	op, okOp := body["op"].(string)
	if !okOp {
		refuse(w, http.StatusBadRequest, "op")
		return
	}
	switch op {
	case "read":
		if !keysWithin(body, "op") {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
	case "set":
		name, okName := body["name"].(string)
		email, okEmail := body["email"].(string)
		if !keysWithin(body, "op", "name", "email") || !okName || !okEmail {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		err := s.cfg.Identity.SetIdentity(name, email)
		s.logger.Info("commit identity", "ok", err == nil)
		if err != nil {
			code, p := status(err)
			send(w, code, p)
			return
		}
	default:
		refuse(w, http.StatusBadRequest, "op")
		return
	}
	id, err := s.cfg.Identity.Identity()
	if err != nil {
		code, p := status(err)
		send(w, code, p)
		return
	}
	send(w, http.StatusOK, identityJSON(id))
}
