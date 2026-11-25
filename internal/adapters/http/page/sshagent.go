package page

import (
	"context"
	"encoding/base64"
	"net/http"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	sshKeysRoute   = "/ssh-keys"
	sshSignRoute   = "/ssh-sign"
	maxSSHSignBody = 32 << 10
	maxSSHKeyBlob  = 4 << 10
)

func (s *Server) sshKeys(w http.ResponseWriter, r *http.Request) {
	keys := []map[string]string{}
	if s.cfg.Access != nil {
		for _, k := range s.cfg.Access.SSHIdentities(ConnBox(r.Context())) {
			keys = append(keys, map[string]string{"key": base64.StdEncoding.EncodeToString(k.Key), "comment": k.Comment})
		}
	}
	send(w, http.StatusOK, map[string]any{"keys": keys})
}

func base64Field(body map[string]any, key string, limit int) ([]byte, bool) {
	t, ok := body[key].(string)
	if !ok || t == "" || len(t) > base64.StdEncoding.EncodedLen(limit) {
		return nil, false
	}
	b, err := base64.StdEncoding.DecodeString(t)
	return b, err == nil
}

func (s *Server) sshSign(w http.ResponseWriter, r *http.Request) {
	body, bad := readObject(w, r, maxSSHSignBody)
	if bad != 0 {
		agentRefuse(w, bad, "body")
		return
	}
	key, okKey := base64Field(body, "key", maxSSHKeyBlob)
	data, okData := base64Field(body, "data", session.MaxSSHSignData)
	flags, okFlags := integer(body["flags"])
	if !keysWithin(body, "key", "data", "flags") || !okKey || !okData || !okFlags || flags < 0 || flags > 1<<32-1 {
		agentRefuse(w, http.StatusBadRequest, "input")
		return
	}
	if s.cfg.Access == nil {
		agentRefuse(w, http.StatusConflict, "access")
		return
	}
	sig, err := s.cfg.Access.SSHSign(context.WithoutCancel(r.Context()), ConnBox(r.Context()), session.SSHSignAsk{Key: key, Data: data, Flags: uint32(flags)})
	if err != nil {
		s.agentError(w, r, err)
		return
	}
	out := map[string]string{"format": sig.Format, "blob": base64.StdEncoding.EncodeToString(sig.Blob)}
	if len(sig.Rest) > 0 {
		out["rest"] = base64.StdEncoding.EncodeToString(sig.Rest)
	}
	send(w, http.StatusOK, out)
}
