package page

import (
	"context"
	"net/http"
	"regexp"
	"slices"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	recapRoute      = "/api/recap"
	workspaceRoute  = "/api/workspace"
	exceptionsRoute = "/api/exceptions"
	maxViewSID      = 80
	maxOfferValue   = 200
	workspaceFor    = 40 * time.Second
	exceptionsFor   = 40 * time.Second
)

var (
	viewRoutes = map[string]bool{recapRoute: true, workspaceRoute: true, exceptionsRoute: true}
	viewSID    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,79}$`)
)

func (s *Server) UseViews(recaps session.RecapPage, workspace session.WorkspacePage, exceptions session.ExceptionsPage) {
	s.cfg.Recaps, s.cfg.WorkspaceView, s.cfg.Exceptions = recaps, workspace, exceptions
}

func viewSession(body map[string]any) (string, bool) {
	sid, ok := body["sid"].(string)
	return sid, ok && len(sid) <= maxViewSID && viewSID.MatchString(sid)
}

func (s *Server) views(w http.ResponseWriter, r *http.Request, body map[string]any) {
	switch r.URL.Path {
	case recapRoute:
		s.recap(w, body)
	case workspaceRoute:
		s.workspace(w, r, body)
	case exceptionsRoute:
		s.exceptions(w, r, body)
	default:
		refuse(w, http.StatusNotFound, "route", saidRoute)
	}
}

type recapLineJSON struct {
	Time int64  `json:"time"`
	Kind string `json:"kind"`
	Text string `json:"text"`
}

func (s *Server) recap(w http.ResponseWriter, body map[string]any) {
	if s.cfg.Recaps == nil {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine has no agent-host, so it keeps no recap."})
		return
	}
	op, okOp := body["op"].(string)
	sid, okSID := viewSession(body)
	if !keysWithin(body, "op", "sid") || !okOp || !okSID {
		refuseFields(w)
		return
	}
	switch op {
	case "seen":
		reply(w, s.cfg.Recaps.Seen(sid), http.StatusOK)
	case "read":
		v, err := s.cfg.Recaps.Recap(sid)
		if err != nil {
			code, p := status(err)
			send(w, code, p)
			return
		}
		lines := make([]recapLineJSON, 0, len(v.Lines))
		for _, l := range v.Lines {
			lines = append(lines, recapLineJSON{Time: millis(l.Time), Kind: l.Kind, Text: l.Text})
		}
		send(w, http.StatusOK, map[string]any{"since": millis(v.Since), "waiting": v.Waiting, "more": v.More, "lines": lines})
	default:
		refuse(w, http.StatusBadRequest, "op", saidOp)
	}
}

type workspaceRowJSON struct {
	SID       string   `json:"sid"`
	Name      string   `json:"name"`
	Repo      string   `json:"repo"`
	Branch    string   `json:"branch"`
	Tree      string   `json:"tree"`
	Running   bool     `json:"running"`
	Size      int64    `json:"size"`
	Entries   int      `json:"entries"`
	SizeMore  bool     `json:"sizeMore"`
	SizeKnown bool     `json:"sizeKnown"`
	State     string   `json:"state"`
	Head      string   `json:"head"`
	Deletable bool     `json:"deletable"`
	Why       string   `json:"why"`
	Said      saidJSON `json:"said"`
}

func (s *Server) workspace(w http.ResponseWriter, r *http.Request, body map[string]any) {
	if s.cfg.WorkspaceView == nil {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine has no agent-host or workspace, so it shows no workspace status."})
		return
	}
	op, ok := body["op"].(string)
	if !ok {
		refuseFields(w)
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(workspaceFor + writeFor))
	ctx, cancel := context.WithTimeout(r.Context(), workspaceFor)
	defer cancel()
	switch op {
	case "view":
		if !keysWithin(body, "op") {
			refuseFields(w)
			return
		}
		v, err := s.cfg.WorkspaceView.WorkspaceView(ctx)
		if err != nil {
			code, p := status(err)
			send(w, code, p)
			return
		}
		rows := make([]workspaceRowJSON, 0, len(v.Sessions))
		for _, x := range v.Sessions {
			rows = append(rows, workspaceRowJSON{SID: x.SID, Name: x.Name, Repo: x.Repo, Branch: x.Branch, Tree: x.Tree, Running: x.Running, Size: x.Size.Bytes, Entries: x.Size.Entries,
				SizeMore: x.Size.More, SizeKnown: x.SizeKnown, State: x.State, Head: x.Head, Deletable: x.Deletable, Why: x.Why, Said: saidJSONOf(session.WorkspaceSaid(x.State))})
		}
		send(w, http.StatusOK, map[string]any{"spaceKnown": v.SpaceKnown, "free": v.Free, "total": v.Total, "low": v.Low, "lowBelow": v.LowBelow, "sessions": rows})
	case "delete":
		sid, okSID := viewSession(body)
		if !keysWithin(body, "op", "sid", "flag", "value") || !okSID {
			refuseFields(w)
			return
		}
		var given *session.Offer
		if flag, has := body["flag"]; has {
			f, isFlag := flag.(string)
			v, isValue := body["value"].(string)
			if !isFlag || !isValue || !slices.Contains([]string{"discard", "force", "worktree"}, f) || len(v) > maxOfferValue {
				refuse(w, http.StatusBadRequest, "offer", saidOffer)
				return
			}
			given = &session.Offer{Flag: f, Value: v}
		} else if _, has := body["value"]; has {
			refuse(w, http.StatusBadRequest, "offer", saidOffer)
			return
		}
		offer, err := s.cfg.WorkspaceView.DeleteSession(ctx, sid, given)
		shared.For(r.Context(), s.logger).Info("workspace-delete", "ok", err == nil)
		if err != nil {
			code, p := status(err)
			out := map[string]any{"error": p.Error, "message": p.Message, "offer": nil}
			if offer != nil {
				out["offer"] = map[string]string{"flag": offer.Flag, "value": offer.Value}
			}
			send(w, code, out)
			return
		}
		send(w, http.StatusOK, map[string]bool{"ok": true})
	default:
		refuse(w, http.StatusBadRequest, "op", saidOp)
	}
}

type exceptionJSON struct {
	Kind     string `json:"kind"`
	Time     int64  `json:"time"`
	SID      string `json:"sid"`
	Repo     string `json:"repo"`
	Branch   string `json:"branch"`
	Commit   string `json:"commit"`
	Reviewer string `json:"reviewer"`
	Path     string `json:"path"`
	Missing  int    `json:"missing"`
	Result   string `json:"result"`
	Verdict  string `json:"verdict"`
	Reason   string `json:"reason"`
	Label    string `json:"label"`
	Note     string `json:"note"`
}

type exceptionHostJSON struct {
	Host    string `json:"host"`
	AddedAt int64  `json:"addedAt"`
	Expires int64  `json:"expires"`
	Reason  string `json:"reason"`
}

type exceptionRulesJSON struct {
	Repo    string     `json:"repo"`
	Problem string     `json:"problem"`
	Rules   []ruleJSON `json:"rules"`
}

func (s *Server) exceptions(w http.ResponseWriter, r *http.Request, body map[string]any) {
	if s.cfg.Exceptions == nil {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine keeps no register of exceptions."})
		return
	}
	if !keysWithin(body) {
		refuseFields(w)
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(exceptionsFor + writeFor))
	ctx, cancel := context.WithTimeout(r.Context(), exceptionsFor)
	defer cancel()
	v, err := s.cfg.Exceptions.Exceptions(ctx)
	if err != nil {
		code, p := status(err)
		send(w, code, p)
		return
	}
	items := make([]exceptionJSON, 0, len(v.Items))
	for _, e := range v.Items {
		items = append(items, exceptionJSON{Kind: e.Kind, Time: millis(e.Time), SID: e.SID, Repo: e.Repo, Branch: e.Branch, Commit: e.Commit, Reviewer: e.Reviewer, Path: e.Path, Missing: e.Missing, Result: e.Result, Verdict: e.Verdict, Reason: e.Reason,
			Label: session.ExceptionLabel(e.Kind, e.Result, e.Verdict, e.Reviewer, e.Path, e.Missing), Note: session.ExceptionNote(e.Kind, e.Verdict, e.Reason)})
	}
	hosts := make([]exceptionHostJSON, 0, len(v.Hosts))
	for _, h := range v.Hosts {
		hosts = append(hosts, exceptionHostJSON{Host: h.Name, AddedAt: millis(h.AddedAt), Expires: millis(h.Expires), Reason: h.Reason})
	}
	var gateway any
	if v.Gateway != nil {
		gateway = map[string]any{"host": v.Gateway.Host}
	}
	rules := make([]exceptionRulesJSON, 0, len(v.Rules))
	for _, x := range v.Rules {
		list := make([]ruleJSON, 0, len(x.Rules))
		for _, rule := range x.Rules {
			list = append(list, ruleJSON{Rule: rule.Rule, Place: string(rule.Place), Worktree: rule.Worktree, Path: rule.Path, Wide: nonNil(rule.Wide), Where: session.PlaceLabel(string(rule.Place))})
		}
		rules = append(rules, exceptionRulesJSON{Repo: x.Repo, Problem: x.Problem, Rules: list})
	}
	send(w, http.StatusOK, map[string]any{"items": items, "more": v.More, "auditProblem": v.AuditProblem, "hosts": hosts, "hostsKnown": v.HostsKnown, "gateway": gateway,
		"rules": rules, "rulesKnown": v.RulesKnown, "now": millis(v.Now)})
}
