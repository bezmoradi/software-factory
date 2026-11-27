package page

import (
	"net/http"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/review"
	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	packsRoute  = "/api/packs"
	MaxPackBody = 2 << 20
)

type packSourceJSON struct {
	Kind   string `json:"kind"`
	URL    string `json:"url"`
	Commit string `json:"commit"`
}

type packJSON struct {
	Digest        string            `json:"digest"`
	SessionDigest string            `json:"sessionDigest"`
	Name          string            `json:"name"`
	Version       string            `json:"version"`
	Source        packSourceJSON    `json:"source"`
	Roles         map[string]string `json:"roles"`
	Steps         map[string]string `json:"steps"`
	Documents     bool              `json:"documents"`
	Agents        []string          `json:"agents"`
	CodeFiles     []string          `json:"codeFiles"`
	AddedAt       int64             `json:"addedAt,omitempty"`
	UsedBy        []string          `json:"usedBy"`
	RemoveOff     string            `json:"removeOff,omitempty"`
}

type packsJSON struct {
	Using     string            `json:"using"`
	Knackpack map[string]string `json:"knackpack"`
	Packs     []packJSON        `json:"packs"`
	Max       int               `json:"max"`
	RoleNames map[string]string `json:"roleNames"`
	CanAdd    bool              `json:"canAdd"`
	Full      string            `json:"full,omitempty"`
	Kept      string            `json:"kept,omitempty"`
	Upload    string            `json:"uploadHelp"`
}

func packOfView(v session.PackView) packJSON {
	out := packJSON{Digest: v.Digest, SessionDigest: v.SessionDigest, Name: v.Name, Version: v.Version,
		Source: packSourceJSON{Kind: v.Source.Kind, URL: v.Source.URL, Commit: v.Source.Commit}, Roles: map[string]string{}, Steps: map[string]string{},
		Documents: v.Documents, Agents: nonNil(v.Agents), CodeFiles: nonNil(v.CodeFiles), UsedBy: nonNil(v.UsedBy), RemoveOff: v.RemoveOff}
	if !v.AddedAt.IsZero() {
		out.AddedAt = millis(v.AddedAt)
	}
	for role, agent := range v.Roles {
		out.Roles[string(role)] = agent
	}
	for step, cmd := range v.Steps {
		out.Steps[string(step)] = cmd
	}
	return out
}

func packsOf(v session.PacksView) packsJSON {
	out := packsJSON{Using: v.Using, Knackpack: map[string]string{"version": v.KnackpackVersion, "commit": v.KnackpackCommit}, Packs: []packJSON{}, Max: v.Max, RoleNames: map[string]string{}, Full: v.Full(), Kept: v.Kept, Upload: session.PackUploadHelp}
	out.CanAdd = out.Full == ""
	for _, p := range v.Packs {
		out.Packs = append(out.Packs, packOfView(p))
	}
	for _, role := range review.Roles {
		out.RoleNames[string(role)] = role.Name()
	}
	return out
}

func (s *Server) packs(w http.ResponseWriter, r *http.Request, body map[string]any) {
	if s.cfg.Packs == nil {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine runs no reviewers, so it keeps no packs."})
		return
	}
	repo, okPath := body["path"].(string)
	op, okOp := body["op"].(string)
	if !okPath || repo == "" || len(repo) > session.MaxCwd || !okOp {
		refuseFields(w)
		return
	}
	text := func(key string, limit int) (string, bool) {
		v, ok := body[key].(string)
		return v, ok && v != "" && len(v) <= limit
	}
	typed := func(key string) (string, bool) {
		v, ok := body[key].(string)
		return v, ok
	}
	if op == "fetch" || op == "add" || op == "add-use" {
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(session.PackFetchFor + writeFor))
	}
	var v session.PacksView
	var err error
	switch op {
	case "list":
		if !keysWithin(body, "op", "path") {
			refuseFields(w)
			return
		}
		v, err = s.cfg.Packs.ListPacks(repo)
	case "fetch":
		url, okURL := typed("url")
		commit, okCommit := typed("commit")
		if !keysWithin(body, "op", "path", "url", "commit") || !okURL || !okCommit {
			refuseFields(w)
			return
		}
		pv, ferr := s.cfg.Packs.FetchPack(r.Context(), repo, url, commit)
		s.logger.Info("pack", "op", op, "ok", ferr == nil)
		if ferr != nil {
			code, p := status(ferr)
			send(w, code, p)
			return
		}
		send(w, http.StatusOK, map[string]any{"pack": packOfView(pv)})
		return
	case "add":
		url, okURL := text("url", session.MaxPackURL)
		commit, okCommit := text("commit", 64)
		digest, okDigest := text("digest", 64)
		if !keysWithin(body, "op", "path", "url", "commit", "digest") || !okURL || !okCommit || !okDigest {
			refuseFields(w)
			return
		}
		v, err = s.cfg.Packs.AddPack(r.Context(), repo, url, commit, digest)
	case "add-use":
		url, okURL := text("url", session.MaxPackURL)
		commit, okCommit := text("commit", 64)
		digest, okDigest := text("digest", 64)
		if !keysWithin(body, "op", "path", "url", "commit", "digest") || !okURL || !okCommit || !okDigest {
			refuseFields(w)
			return
		}
		v, err = s.cfg.Packs.AddAndUsePack(r.Context(), repo, url, commit, digest)
	case "remove":
		digest, ok := text("digest", 64)
		if !keysWithin(body, "op", "path", "digest") || !ok {
			refuseFields(w)
			return
		}
		v, err = s.cfg.Packs.RemovePack(repo, digest)
	case "use":
		pack, ok := text("pack", 64)
		if !keysWithin(body, "op", "path", "pack") || !ok {
			refuseFields(w)
			return
		}
		v, err = s.cfg.Packs.UsePack(repo, pack)
	case "upload":
		name, okName := typed("name")
		files, okFiles := uploadFiles(body["files"])
		if !keysWithin(body, "op", "path", "name", "files") || !okName || !okFiles {
			refuseFields(w)
			return
		}
		v, err = s.cfg.Packs.UploadPack(repo, name, files)
	default:
		send(w, http.StatusBadRequest, problem{Error: "op", Message: "The engine lists, fetches, adds, uploads, removes and uses packs only."})
		return
	}
	if op != "list" {
		s.logger.Info("pack", "op", op, "ok", err == nil)
	}
	if err != nil {
		code, p := status(err)
		send(w, code, p)
		return
	}
	send(w, http.StatusOK, packsOf(v))
}

func uploadFiles(v any) ([]session.PackUpload, bool) {
	list, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]session.PackUpload, 0, len(list))
	for _, item := range list {
		f, ok := item.(map[string]any)
		if !ok || !keysWithin(f, "path", "text") {
			return nil, false
		}
		p, okPath := f["path"].(string)
		t, okText := f["text"].(string)
		if !okPath || !okText {
			return nil, false
		}
		out = append(out, session.PackUpload{Path: p, Text: t})
	}
	return out, true
}
