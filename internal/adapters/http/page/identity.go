package page

import (
	"net/http"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const identityRoute = "/api/commit-identity"

const (
	saidIdentitySaved     = "Saved the name and email agents commit with."
	saidIdentityUnchanged = "No change: agents already commit with this name and email."
)

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
		refuse(w, http.StatusBadRequest, "op", saidOp)
		return
	}
	var said *savedSaid
	switch op {
	case "read":
		if !keysWithin(body, "op") {
			refuseFields(w)
			return
		}
	case "set":
		name, okName := body["name"].(string)
		email, okEmail := body["email"].(string)
		if !keysWithin(body, "op", "name", "email") || !okName || !okEmail {
			refuseFields(w)
			return
		}
		changed, err := s.cfg.Identity.SetIdentity(name, email)
		s.logger.Info("commit identity", "ok", err == nil, "changed", changed)
		if err != nil {
			code, p := status(err)
			send(w, code, p)
			return
		}
		done := saveSaid(changed, saidIdentitySaved, saidIdentityUnchanged)
		said = &done
	default:
		refuse(w, http.StatusBadRequest, "op", saidOp)
		return
	}
	id, err := s.cfg.Identity.Identity()
	if err != nil {
		code, p := status(err)
		send(w, code, p)
		return
	}
	answer := identityJSON(id)
	if said != nil {
		said.on(answer)
	}
	send(w, http.StatusOK, answer)
}
