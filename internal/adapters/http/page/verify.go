package page

import (
	"context"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const verifyRoute = "/api/verify"

var verifyRunID = regexp.MustCompile(`^[0-9a-f]{16}$`)

func (s *Server) UseVerify(v session.VerifyPage) {
	s.verify = v
}

type verifyRunJSON struct {
	ID       string           `json:"id"`
	Commit   string           `json:"commit"`
	Branch   string           `json:"branch"`
	Kind     string           `json:"kind"`
	State    string           `json:"state"`
	Code     int              `json:"code"`
	Tail     string           `json:"tail"`
	Cut      bool             `json:"cut"`
	Withheld bool             `json:"withheld"`
	Reason   string           `json:"reason"`
	By       string           `json:"by"`
	Image    string           `json:"image"`
	ImageID  string           `json:"imageId"`
	Limit    int64            `json:"limitSeconds"`
	Started  int64            `json:"started"`
	Ended    int64            `json:"ended"`
	Files    []verifyFileJSON `json:"files"`
	FileNote string           `json:"fileNote"`
}

type verifyFileJSON struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

type verifyKindJSON struct {
	Command  string         `json:"command"`
	Has      bool           `json:"hasCommand"`
	Required bool           `json:"required"`
	Queued   bool           `json:"queued"`
	Current  *verifyRunJSON `json:"current"`
	Why      string         `json:"why"`
}

type verifyViewJSON struct {
	Head     string          `json:"head"`
	Branch   string          `json:"branch"`
	Command  string          `json:"command"`
	HasCheck bool            `json:"hasCheck"`
	Image    string          `json:"image"`
	Required bool            `json:"required"`
	Busy     bool            `json:"busy"`
	Queued   bool            `json:"queued"`
	Current  *verifyRunJSON  `json:"current"`
	Why      string          `json:"why"`
	Runs     []verifyRunJSON `json:"runs"`
	Browser  verifyKindJSON  `json:"browser"`
}

func verifyRunJSONOf(r session.VerifyRun) verifyRunJSON {
	out := verifyRunJSON{ID: r.ID, Commit: r.Commit, Branch: r.Branch, Kind: string(r.Kind), State: r.State, Code: r.Code, Tail: r.Tail, Cut: r.Cut, Withheld: r.Withheld,
		Reason: r.Reason, By: r.By, Image: r.Image, ImageID: r.ImageID, Limit: int64(r.Limit / time.Second), Started: millis(r.Started), Ended: millis(r.Ended),
		Files: make([]verifyFileJSON, 0, len(r.Attached)), FileNote: r.AttachNote}
	for _, a := range r.Attached {
		out.Files = append(out.Files, verifyFileJSON{Name: a.Name, Kind: a.Kind, Bytes: a.Bytes, SHA256: a.SHA256, Width: a.Width, Height: a.Height})
	}
	return out
}

func currentJSON(r *session.VerifyRun) *verifyRunJSON {
	if r == nil {
		return nil
	}
	c := verifyRunJSONOf(*r)
	return &c
}

func verifyViewJSONOf(v session.VerifyView) verifyViewJSON {
	out := verifyViewJSON{Head: v.Head, Branch: v.Branch, Command: v.Command, HasCheck: v.HasCheck, Image: v.Image, Required: v.Required, Busy: v.Busy, Queued: v.Queued, Why: v.Why,
		Runs: make([]verifyRunJSON, 0, len(v.Runs)), Current: currentJSON(v.Current),
		Browser: verifyKindJSON{Command: v.Browser.Command, Has: v.Browser.Has, Required: v.Browser.Required, Queued: v.Browser.Queued, Current: currentJSON(v.Browser.Current), Why: v.Browser.Why}}
	for _, r := range v.Runs {
		out.Runs = append(out.Runs, verifyRunJSONOf(r))
	}
	return out
}

func (s *Server) verifyAPI(w http.ResponseWriter, r *http.Request, body map[string]any) {
	if s.verify == nil {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine has no box helper or verification folder, so it runs no verification."})
		return
	}
	op, okOp := body["op"].(string)
	_, hasRepo := body["repo"]
	if okOp && hasRepo {
		s.verifyMain(w, r, body, op)
		return
	}
	sid, okSID := viewSession(body)
	if !okOp || !okSID {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	switch op {
	case "read":
		if !keysWithin(body, "op", "sid") {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
	case "start":
		kind, okKind := body["kind"].(string)
		if !keysWithin(body, "op", "sid", "kind") || !okKind || len(kind) > 20 {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		_, err := s.verify.Start(r.Context(), sid, session.VerifyKind(kind), session.VerifyByPage)
		shared.For(r.Context(), s.logger).Info("verify", "op", op, "ok", err == nil)
		if err != nil {
			code, p := status(err)
			send(w, code, p)
			return
		}
	case "stop":
		id, okID := body["id"].(string)
		if !keysWithin(body, "op", "sid", "id") || !okID || !verifyRunID.MatchString(id) {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		err := s.verify.Stop(r.Context(), sid, id)
		shared.For(r.Context(), s.logger).Info("verify", "op", op, "ok", err == nil)
		if err != nil {
			code, p := status(err)
			send(w, code, p)
			return
		}
	case "attachment":
		id, okID := body["id"].(string)
		name, okName := body["name"].(string)
		if !keysWithin(body, "op", "sid", "id", "name") || !okID || !verifyRunID.MatchString(id) || !okName || name == "" || len(name) > 124 {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		s.verifyAttachment(w, r, sid, id, name)
		return
	default:
		send(w, http.StatusBadRequest, problem{Error: "op", Message: "The engine reads, starts and stops a session's verification runs and reads a browser run's files only."})
		return
	}
	v, err := s.verify.View(r.Context(), sid)
	if err != nil {
		code, p := status(err)
		send(w, code, p)
		return
	}
	send(w, http.StatusOK, verifyViewJSONOf(v))
}

func (s *Server) verifyMain(w http.ResponseWriter, r *http.Request, body map[string]any, op string) {
	repo, okRepo := body["repo"].(string)
	if _, hasSID := body["sid"]; hasSID || !okRepo || len(repo) > maxRulesRepo || !rulesRepoName.MatchString(repo) {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	switch op {
	case "read":
		if !keysWithin(body, "op", "repo") {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
	case "start":
		kind, okKind := body["kind"].(string)
		if !keysWithin(body, "op", "repo", "kind") || !okKind || len(kind) > 20 {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(manageFor + writeFor))
		ctx, cancel := context.WithTimeout(r.Context(), manageFor)
		defer cancel()
		run, err := s.verify.StartMain(ctx, repo, session.VerifyKind(kind), session.VerifyByPage)
		shared.For(r.Context(), s.logger).Info("verify", "op", op, "on", "main", "ok", err == nil)
		if err != nil {
			code, p := status(err)
			send(w, code, p)
			return
		}
		send(w, http.StatusAccepted, map[string]string{"id": run.ID, "commit": run.Commit})
		return
	default:
		send(w, http.StatusBadRequest, problem{Error: "op", Message: "The engine reads and starts a project's verification runs on main only."})
		return
	}
	v, err := s.verify.ViewMain(r.Context(), repo)
	if err != nil {
		code, p := status(err)
		send(w, code, p)
		return
	}
	send(w, http.StatusOK, verifyViewJSONOf(v))
}

type verifyImageJSON struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Data   []byte `json:"data"`
}

func (s *Server) verifyAttachment(w http.ResponseWriter, r *http.Request, sid, id, name string) {
	a, rc, err := s.verify.Attachment(r.Context(), sid, id, name)
	shared.For(r.Context(), s.logger).Info("verify", "op", "attachment", "ok", err == nil)
	if err != nil {
		code, p := status(err)
		send(w, code, p)
		return
	}
	defer func() { _ = rc.Close() }()
	if a.Kind == session.VerifyFileImage {
		data, err := io.ReadAll(io.LimitReader(rc, session.MaxVerifyTrace+1))
		if err != nil || int64(len(data)) != a.Bytes {
			send(w, http.StatusConflict, problem{Error: "files", Message: "The engine couldn't read that file."})
			return
		}
		send(w, http.StatusOK, verifyImageJSON{Name: a.Name, Type: "image/png", Width: a.Width, Height: a.Height, Data: data})
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Disposition", `attachment; filename="`+a.Name+`"`)
	h.Set("Content-Length", strconv.FormatInt(a.Bytes, 10))
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if _, err := io.CopyN(w, rc, a.Bytes); err != nil {
		shared.For(r.Context(), s.logger).Warn("verify", "event", "attachment-cut-off")
		panic(http.ErrAbortHandler)
	}
}
