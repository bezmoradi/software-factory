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
	Version       string `json:"version"`
	Digest        string `json:"digest"`
	DigestProblem string `json:"digestProblem"`
}

type updateJSON struct {
	Running     string             `json:"running"`
	Check       bool               `json:"check"`
	Forced      bool               `json:"forced"`
	Unreadable  bool               `json:"unreadable"`
	Development bool               `json:"development"`
	Result      string             `json:"result"`
	Checked     int64              `json:"checked"`
	Update      *updateReleaseJSON `json:"update"`
}

func updateJSONOf(v session.UpdateView) updateJSON {
	out := updateJSON{Running: v.Running, Check: v.Check, Forced: v.Forced, Unreadable: v.Unreadable, Development: v.Development, Result: v.Result, Checked: millis(v.Checked)}
	if v.Version != "" {
		out.Update = &updateReleaseJSON{Version: v.Version, Digest: v.Digest, DigestProblem: v.DigestProblem}
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
	return "The engine couldn't save the update setting in its state folder."
}

func (s *Server) update(w http.ResponseWriter, r *http.Request, body map[string]any) {
	u := s.updatesPage()
	if u == nil {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine has no update check."})
		return
	}
	if len(body) > 1 || !keysWithin(body, "check", "later") {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	var err error
	event := ""
	if raw, present := body["check"]; present {
		on, ok := raw.(bool)
		if !ok {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		err, event = u.SetCheck(on), "setting"
	}
	if raw, present := body["later"]; present {
		version, ok := raw.(string)
		if !ok || version == "" || len(version) > maxLaterField {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		err, event = u.Later(version), "later"
	}
	if err != nil {
		send(w, http.StatusConflict, problem{Error: "update", Message: updateRefusal(err)})
		return
	}
	if event != "" {
		shared.For(r.Context(), s.logger).Info("update check", "event", event)
	}
	send(w, http.StatusOK, updateJSONOf(u.View()))
}
