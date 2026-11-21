package page

import (
	"errors"
	"net/http"

	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	updateRoute   = "/api/update"
	maxLaterField = 40
)

type updateReleaseJSON struct {
	Version string `json:"version"`
}

type updateJSON struct {
	Running     string             `json:"running"`
	Forced      bool               `json:"forced"`
	Unreadable  bool               `json:"unreadable"`
	Development bool               `json:"development"`
	Result      string             `json:"result"`
	Checked     int64              `json:"checked"`
	Update      *updateReleaseJSON `json:"update"`
}

func updateJSONOf(v session.UpdateView) updateJSON {
	out := updateJSON{Running: v.Running, Forced: v.Forced, Unreadable: v.Unreadable, Development: v.Development, Result: v.Result, Checked: millis(v.Checked)}
	if v.Version != "" {
		out.Update = &updateReleaseJSON{Version: v.Version}
	}
	return out
}

func (s *Server) UseUpdates(u session.UpdatesPage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates = u
}

func (s *Server) updatesPage() session.UpdatesPage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updates
}

func updateRefusal(err error) string {
	for _, known := range []error{session.ErrUpdateForced, session.ErrUpdateUnreadable, session.ErrUpdateNotShown} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	return "The engine couldn't save Remind me later in its state folder."
}

func (s *Server) update(w http.ResponseWriter, r *http.Request, body map[string]any) {
	u := s.updatesPage()
	if u == nil {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine has no update check."})
		return
	}
	if !keysWithin(body, "later") {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	if raw, present := body["later"]; present {
		version, ok := raw.(string)
		if !ok || version == "" || len(version) > maxLaterField {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		if err := u.Later(version); err != nil {
			send(w, http.StatusConflict, problem{Error: "update", Message: updateRefusal(err)})
			return
		}
		shared.For(r.Context(), s.logger).Info("update check", "event", "later")
	}
	send(w, http.StatusOK, updateJSONOf(u.View()))
}
