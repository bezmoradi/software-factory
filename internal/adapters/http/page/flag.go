package page

import (
	"net/http"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	allowFileRoute  = "/api/allow-file"
	flagRemoveRoute = "/api/flag-remove"
	maxFlagPath     = 4096
)

type flagJSON struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"`
	Why     string `json:"why"`
	Size    int64  `json:"size"`
	Allowed bool   `json:"allowed"`
	Full    bool   `json:"full"`
}

func flagsJSON(list []session.FlagView) []flagJSON {
	out := make([]flagJSON, 0, len(list))
	for _, f := range list {
		out = append(out, flagJSON{Path: f.Path, Kind: string(f.Kind), Why: f.Why, Size: f.Size, Allowed: f.Allowed, Full: f.Full})
	}
	return out
}

func (s *Server) flag(w http.ResponseWriter, route, sid string, body map[string]any) {
	path, okPath := body["path"].(string)
	if sid == "" || !okPath || path == "" || len(path) > maxFlagPath {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	var err error
	switch route {
	case allowFileRoute:
		reason, okReason := body["reason"].(string)
		if !keysWithin(body, "sid", "path", "reason") || !okReason || len(reason) > 2000 {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		err = s.cfg.Work.AllowFile(sid, path, reason)
		s.logger.Info("flagged file", "op", "allow", "ok", err == nil)
	default:
		if !keysWithin(body, "sid", "path") {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		err = s.cfg.Work.AskRemove(sid, path)
		s.logger.Info("flagged file", "op", "ask-remove", "ok", err == nil)
	}
	reply(w, err, http.StatusOK)
}
