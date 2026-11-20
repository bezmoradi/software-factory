package session

import (
	"maps"
	"regexp"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/gate"
	"github.com/bezmoradi/software-factory/internal/core/review"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	RecapShown     = 20
	MaxRecapSeen   = 500
	AuditReadAll   = 5000
	recapSeenEvery = 20 * time.Second
)

var (
	recapReviewer = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:._-]{0,99}$`)
	recapBranch   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9/._-]{0,99}$`)
	capNames      = map[string]string{CapMoney: "spending", CapTime: "time", CapTurns: "turn", CapDisk: "disk"}
	recapCommit   = regexp.MustCompile(`^[0-9a-f]{7,64}$`)
)

type AuditAll interface {
	ReadAll(limit int) ([]AuditEntry, error)
}

type RecapSeenStore interface {
	Load() (map[string]time.Time, error)
	Save(map[string]time.Time) error
}

type RecapLine struct {
	Time time.Time
	Kind string
	Text string
}

type RecapView struct {
	Since   time.Time
	Lines   []RecapLine
	Waiting int
	More    bool
}

type RecapPage interface {
	Recap(sid string) (RecapView, error)
	Seen(sid string) error
}

type RecapsConfig struct {
	Audit   AuditAll
	Store   RecapSeenStore
	Targets func(sid string) (PushTarget, bool)
	Doubled func(sid string) bool
	Held    func(sid string) (PushTarget, bool)
	Waiting func(sid string) int
	Now     func() time.Time
	Logger  shared.Logger
}

type Recaps struct {
	cfg    RecapsConfig
	mu     sync.Mutex
	seen   map[string]time.Time
	loaded bool
}

var _ RecapPage = (*Recaps)(nil)

func NewRecaps(cfg RecapsConfig) *Recaps {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Recaps{cfg: cfg, seen: map[string]time.Time{}}
}

func (r *Recaps) loadLocked() {
	if r.loaded || r.cfg.Store == nil {
		r.loaded = true
		return
	}
	r.loaded = true
	saved, err := r.cfg.Store.Load()
	if err != nil {
		if r.cfg.Logger != nil {
			r.cfg.Logger.Warn("recap", "event", "seen-not-read")
		}
		return
	}
	for sid, at := range saved {
		if len(r.seen) >= MaxRecapSeen {
			break
		}
		r.seen[sid] = at
	}
}

func (r *Recaps) target(sid string) (PushTarget, error) {
	if r.cfg.Targets == nil {
		return PushTarget{}, explained(conflict("not started here"), "Only a session this engine started has a recap.")
	}
	t, ok := targetOrHeld(r.cfg.Targets, r.cfg.Held, sid)
	if !ok {
		return PushTarget{}, notStartedHere(r.cfg.Doubled, sid, "Only a session this engine started has a recap.")
	}
	return t, nil
}

func (r *Recaps) Seen(sid string) error {
	if err := heldBranch(r.cfg.Doubled, sid); err != nil {
		return err
	}
	if _, err := r.target(sid); err != nil {
		return err
	}
	now := r.cfg.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loadLocked()
	if at, ok := r.seen[sid]; ok && now.Sub(at) >= 0 && now.Sub(at) < recapSeenEvery {
		return nil
	}
	r.seen[sid] = now
	for len(r.seen) > MaxRecapSeen {
		oldest, at := "", time.Time{}
		for k, v := range r.seen {
			if oldest == "" || v.Before(at) {
				oldest, at = k, v
			}
		}
		delete(r.seen, oldest)
	}
	if r.cfg.Store == nil {
		return nil
	}
	if err := r.cfg.Store.Save(maps.Clone(r.seen)); err != nil {
		if r.cfg.Logger != nil {
			r.cfg.Logger.Warn("recap", "event", "seen-not-saved")
		}
		return explained(conflict("state"), "The engine couldn't save when you last viewed this session.")
	}
	return nil
}

func (r *Recaps) Recap(sid string) (RecapView, error) {
	t, err := r.target(sid)
	if err != nil {
		return RecapView{}, err
	}
	r.mu.Lock()
	r.loadLocked()
	since := r.seen[sid]
	r.mu.Unlock()
	v := RecapView{Since: since}
	if r.cfg.Waiting != nil {
		v.Waiting = max(r.cfg.Waiting(sid), 0)
	}
	if since.IsZero() || r.cfg.Audit == nil {
		return v, nil
	}
	entries, err := r.cfg.Audit.ReadAll(AuditReadAll)
	if err != nil {
		return v, explained(conflict("audit"), "The engine couldn't read its audit file.")
	}
	answered, denied := 0, 0
	var lastAnswer time.Time
	var lines []RecapLine
	for _, e := range slices.Backward(entries) {
		if !e.Time.After(since) || !recapOf(e, sid, t) {
			continue
		}
		if e.Kind == AuditPermission {
			if e.By == ByEngine {
				denied++
			} else {
				answered++
			}
			lastAnswer = e.Time
			continue
		}
		if kind, text := recapText(e); text != "" {
			lines = append(lines, RecapLine{Time: e.Time, Kind: kind, Text: text})
		}
	}
	if answered > 0 {
		lines = append(lines, RecapLine{Time: lastAnswer, Kind: "card", Text: recapCount(answered, "permission card", "permission cards") + " answered on the page."})
	}
	if denied > 0 {
		lines = append(lines, RecapLine{Time: lastAnswer, Kind: "card", Text: recapCount(denied, "permission card", "permission cards") + " denied by away mode."})
	}
	slices.SortStableFunc(lines, func(a, b RecapLine) int { return a.Time.Compare(b.Time) })
	if len(lines) > RecapShown {
		lines, v.More = lines[len(lines)-RecapShown:], true
	}
	v.Lines = lines
	return v, nil
}

func recapOf(e AuditEntry, sid string, t PushTarget) bool {
	if e.Session != "" {
		return e.Session == sid
	}
	if e.Kind == AuditUsageAnswer {
		return true
	}
	return e.Repository == t.Name && e.Branch != "" && e.Branch == t.Branch
}

func recapCount(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

func recapSHA(sha string) string {
	if !recapCommit.MatchString(sha) {
		return ""
	}
	return shortSHA7(sha)
}

func commitWords(sha string) string {
	if s := recapSHA(sha); s != "" {
		return "commit " + s
	}
	return "a commit"
}

func reviewerWords(name string) string {
	if recapReviewer.MatchString(name) {
		return name
	}
	return "A reviewer"
}

func capKind(e AuditEntry) string {
	if _, ok := capNames[e.Result]; ok {
		return e.Result
	}
	return ""
}

func capAnswer(e AuditEntry) string {
	if e.Verdict == CapRaised || e.Verdict == CapStopped || e.Verdict == CapFreed {
		return e.Verdict
	}
	return ""
}

func recapText(e AuditEntry) (string, string) {
	c := commitWords(e.Commit)
	switch e.Kind {
	case AuditPush:
		return "push", pushRecap(e, c)
	case AuditReview:
		who := reviewerWords(e.Reviewer)
		switch review.State(e.Result) {
		case review.Pass:
			return "review", who + " passed " + c + "."
		case review.Block:
			return "review", who + " blocked " + c + "."
		case review.Failed:
			return "review", who + "'s review of " + c + " failed."
		case review.Unknown:
			return "review", who + " gave no verdict on " + c + "."
		}
	case AuditOverride:
		return "exception", "You overrode the verdict from " + reviewerWords(e.Reviewer) + " on " + c + "."
	case AuditAllowFile:
		return "exception", "You allowed a flagged file in " + c + "."
	case AuditAllowCommit:
		return "exception", "You allowed flagged " + c + "."
	case string(ForgeIssue), string(ForgePull):
		what := "An issue"
		if e.Kind == string(ForgePull) {
			what = "A pull request"
		}
		switch e.Result {
		case ForgeOpened:
			return "forge", what + " was opened."
		case ForgeExists:
			return "forge", what + " was already open."
		case ForgeDenied:
			return "forge", what + " was denied on the page."
		case ForgeUnclear:
			return "forge", "Whether " + recapLower(what) + " was opened is unclear."
		}
	case AuditPRComment:
		if e.Result == ResultCommented {
			return "forge", "A comment was posted on the pull request."
		}
	case AuditFindings:
		if e.Result == ResultSent {
			return "review", "Blocking findings were sent to the agent."
		}
	case AuditApproveRequirements:
		return "approval", "You approved the requirements."
	case AuditApprovePlan:
		return "approval", "You approved the plan."
	case AuditHomeSettings:
		if e.Result == ResultRefused {
			return "approval", "You didn't wake the session after the agent's user settings changed."
		}
		return "approval", "You approved the agent's changed user settings before a wake."
	case AuditCapReached:
		if k := capKind(e); k != "" {
			return "cap", "The session reached its " + capNames[k] + " cap."
		}
		return "cap", "The session reached a cap."
	case AuditCapAnswer:
		which := "a cap"
		if k := capKind(e); k != "" {
			which = "its " + capNames[k] + " cap"
		}
		switch capAnswer(e) {
		case CapRaised:
			return "cap", "You raised " + which + "."
		case CapStopped:
			return "cap", "You stopped the session at " + which + "."
		case CapFreed:
			return "cap", "The engine measured the session's folder under " + which + " again, so the session can go on."
		}
		return "cap", "You answered the card for " + which + "."
	case AuditUsageAnswer:
		return "usage", "You kept automatic reviews and automatic Send findings running near a usage limit."
	}
	return "", ""
}

func recapLower(s string) string {
	if s == "" {
		return s
	}
	b := []byte(s)
	if b[0] >= 'A' && b[0] <= 'Z' {
		b[0] += 'a' - 'A'
	}
	return string(b)
}

func pushRecap(e AuditEntry, c string) string {
	to := ""
	if recapBranch.MatchString(e.Branch) {
		to = " to " + e.Branch
	}
	switch gate.Result(e.Result) {
	case gate.Pushed:
		return "Pushed " + c + to + "."
	case ResultUpToDate:
		return "The remote already had " + c + "."
	case gate.Held:
		return "A push of " + c + " was held: " + recapCount(len(e.Missing), "item", "items") + " missing."
	case gate.Denied:
		if e.By == ByPage {
			return "You denied the push of " + c + "."
		}
		return "The push of " + c + " was denied."
	case gate.Refused:
		return "A push was refused."
	case ResultUnclear:
		return "Whether " + c + " was pushed is unclear."
	}
	return ""
}
