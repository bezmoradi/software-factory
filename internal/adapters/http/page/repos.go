package page

import (
	"net/http"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

var repoRoutes = map[string]bool{"/api/add-repo": true, "/api/remove-repo": true, "/api/save-token": true, "/api/remove-token": true}

type cloningJSON struct {
	URL       string `json:"url"`
	Name      string `json:"name"`
	StartedAt int64  `json:"startedAt"`
}

type hostJSON struct {
	Host    string `json:"host"`
	SavedAt int64  `json:"savedAt"`
}

func millis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func textFields(w http.ResponseWriter, body map[string]any, limit int, keys ...string) (map[string]string, bool) {
	if !keysWithin(body, keys...) {
		refuse(w, http.StatusBadRequest, "fields")
		return nil, false
	}
	out := map[string]string{}
	for _, k := range keys {
		v, ok := body[k].(string)
		if !ok || len(v) > limit {
			refuse(w, http.StatusBadRequest, k)
			return nil, false
		}
		out[k] = v
	}
	return out, true
}

func (s *Server) repos(w http.ResponseWriter, path string, body map[string]any) {
	if s.cfg.Repos == nil {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine has no workspace, so it can't add repositories."})
		return
	}
	switch path {
	case "/api/add-repo":
		f, ok := textFields(w, body, 4096, "url")
		if !ok {
			return
		}
		k, err := s.cfg.Repos.Add(f["url"])
		if err != nil {
			code, p := status(err)
			send(w, code, p)
			return
		}
		send(w, http.StatusAccepted, map[string]any{"ok": true, "url": k.URL, "name": k.Name})
	case "/api/remove-repo":
		f, ok := textFields(w, body, 200, "name")
		if !ok {
			return
		}
		reply(w, s.cfg.Repos.Remove(f["name"]), http.StatusOK)
	case "/api/save-token":
		f, ok := textFields(w, body, session.MaxTokenLength+1, "host", "token")
		if !ok {
			return
		}
		info, err := s.cfg.Repos.SaveToken(f["host"], f["token"])
		if err != nil {
			code, p := status(err)
			send(w, code, p)
			return
		}
		send(w, http.StatusOK, map[string]any{"ok": true, "host": hostJSON{Host: info.Host, SavedAt: millis(info.SavedAt)}})
	case "/api/remove-token":
		f, ok := textFields(w, body, 4096, "host")
		if !ok {
			return
		}
		reply(w, s.cfg.Repos.RemoveToken(f["host"]), http.StatusOK)
	}
}
