package page

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/push"
	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	pushPageRoute = "/api/push"
	answerRoute   = "/api/push-answer"
	handoffRoute  = "/api/handoff"
	findingsRoute = "/api/send-findings"
	maxSubject    = 200
)

var (
	pushCard  = regexp.MustCompile(`^[0-9a-f]{32}$`)
	commitHex = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

type pushAuditJSON struct {
	Time         int64    `json:"time"`
	By           string   `json:"by"`
	Result       string   `json:"result"`
	Reason       string   `json:"reason"`
	Commit       string   `json:"commit"`
	Branch       string   `json:"branch"`
	Remote       string   `json:"remote"`
	Missing      []string `json:"missing"`
	PersonReason string   `json:"personReason"`
}

type pushJSON struct {
	Running     bool           `json:"running"`
	Result      string         `json:"result"`
	Reason      string         `json:"reason"`
	Card        string         `json:"card"`
	Commit      string         `json:"commit"`
	Branch      string         `json:"branch"`
	Remote      string         `json:"remote"`
	Missing     []string       `json:"missing"`
	NeedsReason bool           `json:"needsReason"`
	Expires     int64          `json:"expires"`
	At          int64          `json:"at"`
	Audit       *pushAuditJSON `json:"audit"`
	Handoff     bool           `json:"handoff"`
	Passed      int            `json:"passed"`
	Overridden  int            `json:"overridden"`

	Findings     bool  `json:"findings"`
	FindingsSent int64 `json:"findingsSent"`
}

func pushOf(v *session.PushView) *pushJSON {
	if v == nil {
		return nil
	}
	out := &pushJSON{Running: v.Running, Result: v.Result, Reason: v.Reason, Card: v.Card, Commit: v.Commit, Branch: v.Branch, Remote: v.Remote,
		Missing: nonNil(v.Missing), NeedsReason: v.NeedsReason, Expires: millis(v.Expires), At: millis(v.At), Handoff: v.Handoff, Passed: v.Passed, Overridden: v.Overridden,
		Findings: v.Findings, FindingsSent: millis(v.FindingsSent)}
	if a := v.Audit; a != nil {
		out.Audit = &pushAuditJSON{Time: millis(a.Time), By: a.By, Result: a.Result, Reason: a.Reason, Commit: a.Commit, Branch: a.Branch,
			Remote: a.Remote, Missing: nonNil(a.Missing), PersonReason: a.PersonReason}
	}
	return out
}

func nonNil(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

type commitJSON struct {
	SHA     string   `json:"sha"`
	Parents []string `json:"parents"`
	Time    int64    `json:"time"`
	Subject string   `json:"subject"`
}

type fileJSON struct {
	Status string `json:"status"`
	Path   string `json:"path"`
}

type changesJSON struct {
	Branch   string       `json:"branch"`
	Head     string       `json:"head"`
	Worktree string       `json:"worktree"`
	Base     string       `json:"base"`
	BaseRef  string       `json:"baseRef"`
	NoBase   bool         `json:"noBase"`
	Commits  []commitJSON `json:"commits"`
	Files    []fileJSON   `json:"files"`
	More     bool         `json:"more"`
	Note     string       `json:"note"`
	Diff     string       `json:"diff"`
	Clipped  bool         `json:"clipped"`
}

func subject(message string) string {
	first, _, _ := strings.Cut(strings.TrimLeft(message, "\n"), "\n")
	return clip(strings.TrimSpace(first), maxSubject)
}

func changesOf(v session.ChangeView) changesJSON {
	out := changesJSON{Branch: v.Branch, Head: v.Head, Worktree: "linked", Base: v.Base, NoBase: v.Base == "", More: v.More, Diff: v.Diff, Clipped: v.Clipped,
		Commits: make([]commitJSON, 0, len(v.Commits)), Files: make([]fileJSON, 0, len(v.Files))}
	switch {
	case v.Base == "" && v.Main == "":
		out.Note = "The engine hasn't fetched the remote's main branch yet, so everything on the branch is shown as new."
	case v.Base == "":
		out.Note = "The branch shares no history with the remote's main branch, so everything on it is new."
	default:
		out.BaseRef = "the remote's main branch (" + v.Base[:12] + ")"
	}
	if !strings.HasSuffix(out.Note, ".") && out.Note != "" {
		out.Note += "."
	}
	out.Note = strings.TrimSpace(out.Note + " Only committed work is shown.")
	for _, c := range v.Commits {
		out.Commits = append(out.Commits, commitJSON{SHA: c.SHA, Parents: nonNil(c.Parents), Time: millis(c.Committed), Subject: subject(c.Message)})
	}
	for _, f := range v.Files {
		status := "changed"
		switch f.Status {
		case push.Added:
			status = "added"
		case push.Deleted:
			status = "deleted"
		}
		out.Files = append(out.Files, fileJSON{Status: status, Path: f.Path})
	}
	return out
}

func (s *Server) pushPage(w http.ResponseWriter, r *http.Request, body map[string]any) {
	if s.cfg.Pushes == nil {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine can't push: it has no repositories or agent-host."})
		return
	}
	sid, ok := body["sid"].(string)
	if !ok || sid == "" || len(sid) > 80 {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(manageFor + writeFor))
	ctx, cancel := context.WithTimeout(r.Context(), manageFor)
	defer cancel()
	switch r.URL.Path {
	case "/api/changes":
		if !keysWithin(body, "sid") {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		v, err := s.cfg.Pushes.Changes(sid)
		if err != nil {
			code, p := status(err)
			send(w, code, p)
			return
		}
		send(w, http.StatusOK, map[string]any{"changes": changesOf(v)})
	case pushPageRoute:
		if !keysWithin(body, "sid") {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		err := s.cfg.Pushes.Push(ctx, sid)
		s.logger.Info("push", "side", "page", "ok", err == nil)
		reply(w, err, http.StatusAccepted)
	case answerRoute:
		card, okCard := body["card"].(string)
		commit, okCommit := body["commit"].(string)
		allow, okAllow := body["allow"].(bool)
		reason := ""
		if raw, present := body["reason"]; present {
			text, ok := raw.(string)
			if !ok || len(text) > 2000 {
				refuse(w, http.StatusBadRequest, "reason")
				return
			}
			reason = text
		}
		if !keysWithin(body, "sid", "card", "commit", "allow", "reason") || !okCard || !pushCard.MatchString(card) || !okCommit || !commitHex.MatchString(commit) || !okAllow {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		err := s.cfg.Pushes.Answer(ctx, sid, card, commit, allow, reason)
		s.logger.Info("push answer", "allow", allow, "ok", err == nil)
		reply(w, err, http.StatusAccepted)
	case handoffRoute:
		card, okCard := body["card"].(string)
		commit, okCommit := body["commit"].(string)
		if !keysWithin(body, "sid", "card", "commit") || !okCard || !pushCard.MatchString(card) || !okCommit || !commitHex.MatchString(commit) {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		err := s.cfg.Pushes.Handoff(ctx, sid, card, commit)
		s.logger.Info("push handoff", "ok", err == nil)
		reply(w, err, http.StatusAccepted)
	case findingsRoute:
		card, okCard := body["card"].(string)
		commit, okCommit := body["commit"].(string)
		if !keysWithin(body, "sid", "card", "commit") || !okCard || !pushCard.MatchString(card) || !okCommit || !commitHex.MatchString(commit) {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		err := s.cfg.Pushes.SendFindings(ctx, sid, card, commit)
		s.logger.Info("push findings", "ok", err == nil)
		reply(w, err, http.StatusAccepted)
	}
}
