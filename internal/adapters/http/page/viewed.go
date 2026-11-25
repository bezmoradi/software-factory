package page

import (
	"net/http"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const viewedRoute = "/api/viewed"

func (s *Server) UseViewed(v session.ViewedPage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.viewed = v
}

func (s *Server) viewedPage() session.ViewedPage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.viewed
}

func (s *Server) viewedMarks(sid string) map[string]string {
	if v := s.viewedPage(); v != nil {
		return v.Marks(sid)
	}
	return nil
}

func optionalString(body map[string]any, key string) (string, bool) {
	raw, present := body[key]
	if !present {
		return "", true
	}
	text, ok := raw.(string)
	return text, ok
}

func (s *Server) markViewed(w http.ResponseWriter, r *http.Request, body map[string]any) {
	v := s.viewedPage()
	if v == nil {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine has no changes view to mark files in."})
		return
	}
	sid, okSID := body["sid"].(string)
	path, okPath := body["path"].(string)
	blob, okBlob := body["blob"].(string)
	on, okOn := body["viewed"].(bool)
	if !keysWithin(body, "sid", "path", "blob", "viewed") || !okSID || sid == "" || len(sid) > 80 || !okPath || !session.ViewedPath(path) || !okBlob || !commitHex.MatchString(blob) || !okOn {
		refuseFields(w)
		return
	}
	reply(w, v.Mark(r.Context(), sid, path, blob, on), http.StatusOK)
}
