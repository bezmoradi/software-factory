package page

import (
	"net/http"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

type matchJSON struct {
	Number  int    `json:"number"`
	URL     string `json:"url"`
	Title   string `json:"title"`
	Created int64  `json:"created"`
	Exact   bool   `json:"exact"`
}

func matchesOf(list []session.ForgeMatch) []matchJSON {
	out := make([]matchJSON, 0, len(list))
	for _, m := range list {
		out = append(out, matchJSON{Number: m.Number, URL: m.URL, Title: m.Title, Created: millis(m.Created), Exact: m.Exact})
	}
	return out
}

type holdJSON struct {
	AfterBlock bool `json:"afterBlock"`
	MaxFiles   int  `json:"maxFiles"`
	MaxLines   int  `json:"maxLines"`
}

func holdJSONOf(h session.HoldSettings) holdJSON {
	return holdJSON{AfterBlock: h.AfterBlock, MaxFiles: h.MaxFiles, MaxLines: h.MaxLines}
}

func (s *Server) projectHold(w http.ResponseWriter, body map[string]any) {
	repo, okPath := body["path"].(string)
	raw, okHold := body["hold"].(map[string]any)
	if !keysWithin(body, "path", "op", "hold") || !okPath || len(repo) > session.MaxCwd || !okHold || !keysWithin(raw, "afterBlock", "maxFiles", "maxLines") {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	after, okAfter := raw["afterBlock"].(bool)
	files, okFiles := typedCount(raw["maxFiles"])
	lines, okLines := typedCount(raw["maxLines"])
	h := session.HoldSettings{AfterBlock: after, MaxFiles: int(files), MaxLines: int(lines)}
	field := ""
	switch fault := h.Fault(); {
	case !okAfter:
		field = "afterBlock"
	case !okFiles || fault == "maxFiles":
		field = "maxFiles"
	case !okLines || fault != "":
		field = "maxLines"
	}
	if field != "" {
		send(w, http.StatusBadRequest, problem{Error: "hold", Field: field, Message: session.HoldRule})
		return
	}
	err := s.cfg.Work.SetHold(repo, h)
	s.logger.Info("project setting", "op", "hold", "ok", err == nil)
	reply(w, err, http.StatusOK)
}
