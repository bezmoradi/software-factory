package page

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/push"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
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

	Extra  *pushExtraJSON   `json:"extra,omitempty"`
	Checks []agentCheckJSON `json:"checks,omitempty"`

	CheckedBy *pushCheckedJSON `json:"checkedBy,omitempty"`
}

type pushReviewerJSON struct {
	Agent        string `json:"agent"`
	Name         string `json:"name"`
	Required     bool   `json:"required"`
	Advisory     bool   `json:"advisory"`
	State        string `json:"state"`
	Overridden   bool   `json:"overridden"`
	Counted      bool   `json:"counted"`
	SetAside     string `json:"setAside"`
	Unchecked    bool   `json:"unchecked"`
	Runs         int    `json:"runs"`
	Blocking     int    `json:"blocking"`
	Requirements int    `json:"requirements"`
	Verified     int    `json:"verified"`
	Violated     int    `json:"violated"`
	NotVerified  int    `json:"notVerified"`
}

type pushRunJSON struct {
	Kind  string `json:"kind"`
	State string `json:"state"`
	Code  int    `json:"code"`
}

type pushCheckedJSON struct {
	Reviewers  []pushReviewerJSON `json:"reviewers"`
	Runs       []pushRunJSON      `json:"runs"`
	Unreadable bool               `json:"unreadable"`
}

func checkedOf(c *session.PushChecked) *pushCheckedJSON {
	if c == nil {
		return nil
	}
	out := &pushCheckedJSON{Reviewers: make([]pushReviewerJSON, 0, len(c.Reviewers)), Runs: make([]pushRunJSON, 0, len(c.Runs)), Unreadable: c.Unreadable}
	for _, r := range c.Reviewers {
		out.Reviewers = append(out.Reviewers, pushReviewerJSON{Agent: r.Agent, Name: r.Name, Required: r.Required, Advisory: r.Advisory, State: r.State, Overridden: r.Overridden, Counted: r.Counted,
			SetAside: r.SetAside, Unchecked: r.Unchecked, Runs: r.Runs, Blocking: r.Blocking, Requirements: r.Requirements, Verified: r.Verified, Violated: r.Violated, NotVerified: r.NotVerified})
	}
	for _, r := range c.Runs {
		out.Runs = append(out.Runs, pushRunJSON{Kind: r.Kind, State: r.State, Code: r.Code})
	}
	return out
}

func pushOf(v *session.PushView) *pushJSON {
	if v == nil {
		return nil
	}
	out := &pushJSON{Running: v.Running, Result: v.Result, Reason: v.Reason, Card: v.Card, Commit: v.Commit, Branch: v.Branch, Remote: v.Remote,
		Missing: nonNil(v.Missing), NeedsReason: v.NeedsReason, Expires: millis(v.Expires), At: millis(v.At), Handoff: v.Handoff, Passed: v.Passed, Overridden: v.Overridden,
		Findings: v.Findings, FindingsSent: millis(v.FindingsSent)}
	out.Extra = extraOf(v.Extra)
	out.Checks = checksOf(v.Checks)
	out.CheckedBy = checkedOf(v.CheckedBy)
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
	Blob   string `json:"blob"`
	Viewed bool   `json:"viewed"`
	Stale  bool   `json:"stale"`
	Size   int64  `json:"size,omitempty"`
	Skip   string `json:"notDiffed,omitempty"`
}

type pointsJSON struct {
	Reviewed   string `json:"reviewed"`
	ReviewedAt int64  `json:"reviewedAt"`
	Turn       string `json:"turn"`
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
	From     string       `json:"from"`
	Points   pointsJSON   `json:"points"`
}

func subject(message string) string {
	first, _, _ := strings.Cut(strings.TrimLeft(message, "\n"), "\n")
	return clip(strings.TrimSpace(first), maxSubject)
}

func changesOf(v session.ChangeView, viewed map[string]string) changesJSON {
	out := changesJSON{Branch: v.Branch, Head: v.Head, Worktree: "linked", Base: v.Base, NoBase: v.Base == "", More: v.More, Diff: v.Diff, Clipped: v.Clipped,
		Commits: make([]commitJSON, 0, len(v.Commits)), Files: make([]fileJSON, 0, len(v.Files)), From: string(v.From),
		Points: pointsJSON{Reviewed: v.Points.Reviewed, ReviewedAt: millis(v.Points.ReviewedAt), Turn: v.Points.Turn}}
	switch {
	case v.From == session.FromReviewed && v.Base != "":
		out.BaseRef = "the last reviewed commit (" + v.Base[:12] + ")"
	case v.From == session.FromTurn && v.Base != "":
		out.BaseRef = "the commit before the turn the session last reported starting (" + v.Base[:12] + ")"
	case v.From == session.FromCommit && v.Base != "":
		out.BaseRef = "the chosen commit " + v.Base[:12]
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
	skipped := map[string]session.NotDiffed{}
	for _, n := range v.NotDiffed {
		skipped[n.Path] = n
	}
	for _, f := range v.Files {
		status, blob := "changed", f.NewSHA
		switch f.Status {
		case push.Added:
			status = "added"
		case push.Deleted:
			status, blob = "deleted", f.OldSHA
		}
		if !commitHex.MatchString(blob) {
			blob = ""
		}
		mark, marked := viewed[f.Path]
		n := skipped[f.Path]
		out.Files = append(out.Files, fileJSON{Status: status, Path: f.Path, Blob: blob, Viewed: marked && blob != "" && mark == blob, Stale: marked && mark != blob, Size: n.Size, Skip: n.Why})
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
		from, okFrom := optionalString(body, "from")
		commit, okCommit := optionalString(body, "commit")
		at, okAt := session.ChangePointOf(from, commit)
		if !keysWithin(body, "sid", "from", "commit") || !okFrom || !okCommit || !okAt {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		v, err := s.cfg.Pushes.Changes(ctx, sid, at)
		if err != nil {
			code, p := status(err)
			send(w, code, p)
			return
		}
		send(w, http.StatusOK, map[string]any{"changes": changesOf(v, s.viewedMarks(sid))})
	case pushPageRoute:
		if !keysWithin(body, "sid") {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		err := s.cfg.Pushes.Push(ctx, sid)
		shared.For(r.Context(), s.logger).Info("push", "side", "page", "ok", err == nil)
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
		shared.For(r.Context(), s.logger).Info("push answer", "allow", allow, "ok", err == nil)
		reply(w, err, http.StatusAccepted)
	case handoffRoute:
		card, okCard := body["card"].(string)
		commit, okCommit := body["commit"].(string)
		if !keysWithin(body, "sid", "card", "commit") || !okCard || !pushCard.MatchString(card) || !okCommit || !commitHex.MatchString(commit) {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		err := s.cfg.Pushes.Handoff(ctx, sid, card, commit)
		shared.For(r.Context(), s.logger).Info("push handoff", "ok", err == nil)
		reply(w, err, http.StatusAccepted)
	case findingsRoute:
		card, okCard := body["card"].(string)
		commit, okCommit := body["commit"].(string)
		if !keysWithin(body, "sid", "card", "commit") || !okCard || !pushCard.MatchString(card) || !okCommit || !commitHex.MatchString(commit) {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		err := s.cfg.Pushes.SendFindings(ctx, sid, card, commit)
		shared.For(r.Context(), s.logger).Info("push findings", "ok", err == nil)
		reply(w, err, http.StatusAccepted)
	}
}
