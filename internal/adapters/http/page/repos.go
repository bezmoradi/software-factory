package page

import (
	"net/http"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

var repoRoutes = map[string]bool{"/api/add-repo": true, "/api/remove-repo": true, "/api/save-token": true, "/api/remove-token": true}

type cloningJSON struct {
	URL       string             `json:"url"`
	Name      string             `json:"name"`
	StartedAt int64              `json:"startedAt"`
	Progress  *cloneProgressJSON `json:"progress,omitempty"`
}

type cloneProgressJSON struct {
	Phase   string `json:"phase"`
	Percent *int   `json:"percent,omitempty"`
	Said    string `json:"said"`
}

func cloningJSONOf(k session.Cloning) cloningJSON {
	out := cloningJSON{URL: k.URL, Name: k.Name, StartedAt: millis(k.StartedAt)}
	if k.Progress.Phase != "" {
		p := &cloneProgressJSON{Phase: string(k.Progress.Phase), Said: k.Progress.Said}
		if k.Progress.HasPercent {
			p.Percent = &k.Progress.Percent
		}
		out.Progress = p
	}
	return out
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
		refuseFields(w)
		return nil, false
	}
	out := map[string]string{}
	for _, k := range keys {
		v, ok := body[k].(string)
		if !ok || len(v) > limit {
			refuse(w, http.StatusBadRequest, k, "The engine needs text of at most "+sizeWords(int64(limit))+" here.")
			return nil, false
		}
		out[k] = v
	}
	return out, true
}

func (s *Server) repos(w http.ResponseWriter, r *http.Request, body map[string]any) {
	if s.cfg.Repos == nil {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine has no workspace, so it can't add repositories."})
		return
	}
	switch r.URL.Path {
	case "/api/add-repo":
		f, ok := textFields(w, body, MaxPageBody, "url")
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
		f, ok := textFields(w, body, MaxPageBody, "host", "token")
		if !ok {
			return
		}
		info, err := s.cfg.Repos.SaveToken(r.Context(), f["host"], f["token"])
		if err != nil {
			code, p := status(err)
			send(w, code, p)
			return
		}
		s.reg.Limits().TokenSaved(info.Host)
		answer := map[string]any{"ok": true, "host": hostJSON{Host: info.Host, SavedAt: millis(info.SavedAt)}}
		if info.Warning != "" {
			answer["warning"] = info.Warning
		}
		send(w, http.StatusOK, answer)
	case "/api/remove-token":
		f, ok := textFields(w, body, 4096, "host")
		if !ok {
			return
		}
		err := s.cfg.Repos.RemoveToken(f["host"])
		if err == nil {
			s.reg.Limits().TokenSaved(f["host"])
		}
		reply(w, err, http.StatusOK)
	}
}
