package page

import (
	"net/http"
	"regexp"
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
	ID       string `json:"id"`
	Commit   string `json:"commit"`
	Branch   string `json:"branch"`
	Kind     string `json:"kind"`
	State    string `json:"state"`
	Code     int    `json:"code"`
	Tail     string `json:"tail"`
	Cut      bool   `json:"cut"`
	Withheld bool   `json:"withheld"`
	Reason   string `json:"reason"`
	By       string `json:"by"`
	Image    string `json:"image"`
	ImageID  string `json:"imageId"`
	Limit    int64  `json:"limitSeconds"`
	Started  int64  `json:"started"`
	Ended    int64  `json:"ended"`
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
}

func verifyRunJSONOf(r session.VerifyRun) verifyRunJSON {
	return verifyRunJSON{ID: r.ID, Commit: r.Commit, Branch: r.Branch, Kind: string(r.Kind), State: r.State, Code: r.Code, Tail: r.Tail, Cut: r.Cut, Withheld: r.Withheld,
		Reason: r.Reason, By: r.By, Image: r.Image, ImageID: r.ImageID, Limit: int64(r.Limit / time.Second), Started: millis(r.Started), Ended: millis(r.Ended)}
}

func verifyViewJSONOf(v session.VerifyView) verifyViewJSON {
	out := verifyViewJSON{Head: v.Head, Branch: v.Branch, Command: v.Command, HasCheck: v.HasCheck, Image: v.Image, Required: v.Required, Busy: v.Busy, Queued: v.Queued, Why: v.Why,
		Runs: make([]verifyRunJSON, 0, len(v.Runs))}
	if v.Current != nil {
		c := verifyRunJSONOf(*v.Current)
		out.Current = &c
	}
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
	default:
		send(w, http.StatusBadRequest, problem{Error: "op", Message: "The engine reads, starts and stops a session's verification runs only."})
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
