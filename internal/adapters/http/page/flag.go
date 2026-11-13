package page

import (
	"net/http"
	"regexp"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	allowFileRoute     = "/api/allow-file"
	flagRemoveRoute    = "/api/flag-remove"
	allowCommitRoute   = "/api/allow-commit"
	commitRewriteRoute = "/api/commit-rewrite"
	maxFlagPath        = 4096
)

var flagCommit = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

type flagJSON struct {
	Path    string `json:"path"`
	Shown   string `json:"shown"`
	Commit  string `json:"commit,omitempty"`
	Field   string `json:"field,omitempty"`
	Label   string `json:"label,omitempty"`
	Kind    string `json:"kind"`
	Why     string `json:"why"`
	Size    int64  `json:"size"`
	Allowed bool   `json:"allowed"`
	Full    bool   `json:"full"`
}

func flagsJSON(list []session.FlagView) []flagJSON {
	out := make([]flagJSON, 0, len(list))
	for _, f := range list {
		j := flagJSON{Path: f.Path, Shown: f.Shown, Kind: string(f.Kind), Why: f.Why, Size: f.Size, Allowed: f.Allowed, Full: f.Full}
		if f.Commit != "" {
			j.Commit, j.Field, j.Label = f.Commit, string(f.Field), session.FieldWords[f.Field]
		}
		out = append(out, j)
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

func (s *Server) commitFlag(w http.ResponseWriter, route, sid string, body map[string]any) {
	commit, okCommit := body["commit"].(string)
	field, okField := body["field"].(string)
	if sid == "" || !okCommit || !flagCommit.MatchString(commit) || !okField || session.FieldWords[session.CommitField(field)] == "" {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	var err error
	switch route {
	case allowCommitRoute:
		reason, okReason := body["reason"].(string)
		if !keysWithin(body, "sid", "commit", "field", "reason") || !okReason || len(reason) > 2000 {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		err = s.cfg.Work.AllowCommit(sid, commit, session.CommitField(field), reason)
		s.logger.Info("flagged commit", "op", "allow", "ok", err == nil)
	default:
		if !keysWithin(body, "sid", "commit", "field") {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		err = s.cfg.Work.AskRewrite(sid, commit, session.CommitField(field))
		s.logger.Info("flagged commit", "op", "ask-rewrite", "ok", err == nil)
	}
	reply(w, err, http.StatusOK)
}
