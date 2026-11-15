package session

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/gate"
	"github.com/bezmoradi/software-factory/internal/core/level"
	"github.com/bezmoradi/software-factory/internal/core/push"
	"github.com/bezmoradi/software-factory/internal/core/review"
	"github.com/bezmoradi/software-factory/internal/core/shared"
	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

const (
	AllowFor        = 15 * time.Minute
	PushCalls       = 5
	PushWindow      = 10 * time.Minute
	PushFor         = 2 * time.Minute
	DefaultWorkType = "fix"
	ByAgent         = "agent"
	ByPage          = "page"
	ByReviews       = "agent, after reviews"
	ByEngine        = "engine"
	ByStart         = "engine, at start"
	ByReset         = "engine, at reset"
	AgentReplyFor   = 55 * time.Second

	ResultUpToDate gate.Result = "up-to-date"

	maxPushReason = 1000
	maxNoteReason = 600
)

type PushTarget struct {
	SID     string
	Repo    string
	Name    string
	Branch  string
	Project string

	SyncFailed string
	SyncWhy    string
}

type PushRepos interface {
	Mains
	PushTarget(name string) (url, token string, err error)
	Main(name string) (string, error)
}

type ReviewEntries interface {
	Entries() (map[review.Key]ReviewEntry, error)
	ReplyOf(sid string, e ReviewEntry) (string, int)
}

type AuditEntry struct {
	Time         time.Time
	Session      string
	Repository   string
	Branch       string
	Commit       string
	Remote       string
	By           string
	Result       string
	Reason       string
	Missing      []string
	PersonReason string
	Kind         string
	Reviewer     string
	Verdict      string
	Path         string
	Blobs        []string
	Request      string
	Req          string
	Person       string
	FormerPerson string
	Box          string
	From         string
	FromRef      string
	Source       string
	Hash         string
	Agent        string
	PluginCommit string
	ClaudeCode   string
	Models       []string
}

type Audit interface {
	Append(AuditEntry) error
}

type PushView struct {
	Running     bool
	Result      string
	Reason      string
	Card        string
	Commit      string
	Branch      string
	Remote      string
	Missing     []string
	Unmet       []string
	NeedsReason bool
	Expires     time.Time
	At          time.Time
	Audit       *AuditEntry
	Handoff     bool
	Passed      int
	Overridden  int

	Findings     bool
	FindingsSent time.Time
	Extra        *PushExtra
}

func (v PushView) clone() PushView {
	v.Missing = slices.Clone(v.Missing)
	v.Unmet = slices.Clone(v.Unmet)
	if v.Audit != nil {
		a := *v.Audit
		a.Missing = slices.Clone(a.Missing)
		v.Audit = &a
	}
	return v
}

type ChangeView struct {
	Branch string
	Head   string
	Base   string
	Main   string
	ChangeSet
}

type PushStarter interface {
	Push(ctx context.Context, sid string) error
	Answer(ctx context.Context, sid, card, commit string, allow bool, reason string) error
	Handoff(ctx context.Context, sid, card, commit string) error
	SendFindings(ctx context.Context, sid, card, commit string) error
	Changes(sid string) (ChangeView, error)
}

type PushesConfig struct {
	Pusher      push.Pusher
	Heads       Heads
	History     History
	Changes     ChangeReader
	Repos       PushRepos
	Reviews     ReviewEntries
	Audit       Audit
	Targets     func(sid string) (PushTarget, bool)
	Level       func(sid string) (level.Result, bool)
	Show        func(sid string, v PushView)
	Agents      map[review.Role]string
	RoleAgents  RoleAgents
	WorkType    *workflow.WorkType
	StartReview func(ctx context.Context, sid string) error
	Work        PushWork
	Tell        func(sid, text string) error
	Note        func(sid, text string)
	Denials     DenialStore
	Waiting     WaitingStore
	RetryReview func(ctx context.Context, sid string) error
	Base        context.Context
	Now         func() time.Time
	Logger      shared.Logger

	ReviewerSignIn func() (SignIn, bool)
	SessionSignIn  func(sid string) (SignIn, bool)
	AutoFindings   func(project string) bool
}

type PushWork interface {
	PushFacts(sid string, t PushTarget, head, main string) (WorkFacts, bool)
	HandoffText(sid string) string
}

type heldPush struct {
	card    string
	commit  string
	branch  string
	remote  string
	missing []string
	expires time.Time
	handoff bool

	findings bool
	sent     time.Time
	req      string
}

type Pushes struct {
	cfg     PushesConfig
	kind    workflow.WorkType
	mu      sync.Mutex
	busy    map[string]bool
	held    map[string]*heldPush
	calls   *limiter
	capped  map[string]bool
	views   map[string]PushView
	waiting map[string]string
	waitReq map[string]waitedFor
	again   map[string]bool
	recheck map[string]string
	refused map[string][]string
	spoken  map[string]string
	pushed  func(PushedFacts)
	closing bool
	resume  resumeState
	saveMu  sync.Mutex
	wg      sync.WaitGroup
	halt    atomic.Pointer[Halt]
	extras  atomic.Pointer[pushExtras]
	limits  atomic.Pointer[Limits]

	denialsUnread bool
}

var (
	_ push.Requests = (*Pushes)(nil)
	_ PushStarter   = (*Pushes)(nil)
)

func NewPushes(cfg PushesConfig) (*Pushes, error) {
	if cfg.Pusher == nil || cfg.Heads == nil || cfg.History == nil || cfg.Changes == nil || cfg.Repos == nil || cfg.Audit == nil || cfg.Targets == nil || cfg.Level == nil {
		return nil, errors.New("pushes: need the pusher, the reader, the repositories, the audit file, the sessions and their levels")
	}
	cfg.Audit = masking(cfg.Audit)
	kind, ok := workType(DefaultWorkType)
	if cfg.WorkType != nil {
		kind, ok = *cfg.WorkType, true
	}
	if !ok {
		return nil, errors.New("pushes: no default work type")
	}
	if cfg.Agents == nil {
		cfg.Agents = DefaultReviewers
	}
	if cfg.Show == nil {
		cfg.Show = func(string, PushView) {}
	}
	if cfg.Base == nil {
		cfg.Base = context.Background()
	}
	if cfg.Note == nil {
		cfg.Note = func(string, string) {}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = quiet{}
	}
	p := &Pushes{cfg: cfg, kind: kind, busy: map[string]bool{}, held: map[string]*heldPush{},
		calls: newLimiter(PushCalls, PushWindow), capped: map[string]bool{}, views: map[string]PushView{}, waiting: map[string]string{}, waitReq: map[string]waitedFor{}, again: map[string]bool{}, recheck: map[string]string{}, refused: map[string][]string{}, spoken: map[string]string{}}
	p.loadDenials()
	p.loadWaiting()
	return p, nil
}

func workType(id string) (workflow.WorkType, bool) {
	i := slices.IndexFunc(workflow.Builtin, func(t workflow.WorkType) bool { return t.ID == id })
	if i < 0 {
		return workflow.WorkType{}, false
	}
	return workflow.Builtin[i], true
}

func (p *Pushes) Wait() { p.wg.Wait() }

func (p *Pushes) View(sid string) (PushView, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v, ok := p.views[sid]
	return v.clone(), ok
}

func (p *Pushes) Request(ctx context.Context, sid string) push.Reply {
	p.mu.Lock()
	now := p.cfg.Now()
	if !p.calls.allow(sid, now) {
		first := !p.capped[sid]
		p.capped[sid] = true
		p.mu.Unlock()
		why := "factory_push was called more than " + strconv.Itoa(PushCalls) + " times in " + strconv.Itoa(int(PushWindow/time.Minute)) + " minutes for this session; end your turn and let the person push from the page"
		if first {
			p.record(AuditEntry{Time: now, Session: sid, By: ByAgent, Result: string(gate.Refused), Reason: why, Req: shared.RequestOf(ctx)})
		}
		return push.Reply{Decision: push.DecisionRefused, Reason: why}
	}
	delete(p.capped, sid)
	if p.closing {
		p.mu.Unlock()
		return push.Reply{Decision: push.DecisionRefused, Reason: stoppingReason}
	}
	if p.busy[sid] {
		p.mu.Unlock()
		return push.Reply{Decision: push.DecisionRefused, Reason: "a push of this session is being decided now; end your turn and let the person see the result on the page"}
	}
	p.busy[sid] = true
	unread := p.denialsUnread
	p.wg.Add(1)
	p.mu.Unlock()
	defer p.wg.Done()
	defer p.release(sid)
	if unread {
		return p.heldUnread(sid, ByAgent, now)
	}
	if reply, ok := p.stillDenied(sid, now); ok {
		return reply
	}
	ctx, cancel := context.WithTimeout(ctx, PushFor)
	defer cancel()
	reply, d := p.decide(ctx, sid, ByAgent, nil, "")
	p.mu.Lock()
	said := p.spoken[sid]
	delete(p.spoken, sid)
	p.mu.Unlock()
	if p.cfg.Now().Sub(now) < AgentReplyFor {
		said = ""
	}
	p.cfg.Note(sid, said)
	if reply.Decision != push.DecisionHeld {
		return reply
	}
	if d.NeedsApproval {
		shared.For(ctx, p.cfg.Logger).Info("push", "event", "review-not-started", "why", "approval")
		reply.Reason = awaitingApproval + reply.Reason
		return reply
	}
	head, ok, why := p.reviewFor(ctx, sid)
	switch {
	case ok:
		reply.Reason = "the engine is reviewing commit " + shortSHA(head) + " now and pushes it itself if every review passes; until then: " + reply.Reason
	case why != "":
		reply.Reason = why + "; " + reply.Reason
	}
	return reply
}

const awaitingApproval = "the engine doesn't start reviews while the person's approval of the requirements or the plan is missing: it starts them when you push after the person approves on the page, and the person can press Review there any time; until then: "

func (p *Pushes) stillDenied(sid string, now time.Time) (push.Reply, bool) {
	t, ok := p.cfg.Targets(sid)
	if !ok {
		return push.Reply{}, false
	}
	head, err := p.cfg.Heads.Head(t.Repo, t.Branch)
	if err != nil {
		return push.Reply{}, false
	}
	p.mu.Lock()
	denied := p.refusedLocked(sid, head)
	p.mu.Unlock()
	if !denied {
		return push.Reply{}, false
	}
	why := "the person denied commit " + shortSHA(head) + " on the page, so the engine doesn't push it on your request; end your turn, and commit a fix or tell the person what changed: they can push from the page"
	p.record(AuditEntry{Time: now, Session: sid, Repository: t.Name, Branch: t.Branch, Commit: head, By: ByAgent, Result: string(gate.Denied), Reason: why})
	return push.Reply{Decision: push.DecisionDenied, Reason: why}, true
}

func (p *Pushes) heldUnread(sid, by string, now time.Time) push.Reply {
	t, ok := p.cfg.Targets(sid)
	e := AuditEntry{Time: now, Session: sid, Repository: t.Name, Branch: t.Branch, By: by, Result: string(gate.Held), Reason: denialsUnreadWhy}
	if head, err := p.cfg.Heads.Head(t.Repo, t.Branch); ok && err == nil {
		e.Commit = head
	}
	p.record(e)
	p.show(sid, PushView{Result: string(gate.Held), Reason: denialsUnreadWhy, Commit: e.Commit, Branch: t.Branch, At: now, Audit: &e})
	p.cfg.Logger.Info("push", "event", "held-denials-unread", "by", by)
	return push.Reply{Decision: push.DecisionHeld, Reason: denialsUnreadWhy}
}

func (p *Pushes) reviewFor(ctx context.Context, sid string) (string, bool, string) {
	if p.cfg.StartReview == nil || p.cfg.Reviews == nil {
		return "", false, ""
	}
	t, ok := p.cfg.Targets(sid)
	if !ok {
		return "", false, ""
	}
	head, err := p.cfg.Heads.Head(t.Repo, t.Branch)
	if err != nil {
		return "", false, ""
	}
	entries, required, err := p.entriesAt(sid, t, head)
	if err != nil {
		return "", false, ""
	}
	p.mu.Lock()
	refused := p.refusedLocked(sid, head)
	p.mu.Unlock()
	if refused {
		return "", false, "the person denied commit " + shortSHA(head) + " on the page, so the engine won't push it on its own; tell the person what changed, or commit a fix, which they can push from the page"
	}
	running, recorded, rerun := false, false, false
	var ended []ReviewEntry
	for _, q := range required {
		for _, k := range (review.Key{Commit: head, Branch: t.Branch, Agent: q.Agent}).Runs(q.RunCount()) {
			e, ok := entries[k]
			if !ok {
				continue
			}
			recorded = true
			running = running || e.Record.State == review.Running
			rerun = rerun || Rerunnable(e)
			if e.Record.State == review.Failed || e.Record.State == review.Unknown {
				ended = append(ended, e)
			}
		}
	}
	start := p.cfg.StartReview
	if rerun && p.cfg.RetryReview != nil {
		start = p.cfg.RetryReview
	}
	switch {
	case running:
	case recorded && !rerun:
		return "", false, unfinishedReviews(head, ended)
	default:
		if p.halted(ByReviews) != "" {
			return p.holdForRelease(sid, head, shared.RequestOf(ctx))
		}
		if err := start(ctx, sid); err != nil {
			e, ok := errors.AsType[*Error](err)
			switch {
			case ok && e.Reason == "halted":
				return p.holdForRelease(sid, head, shared.RequestOf(ctx))
			case ok && e.Reason == "cost":
				shared.For(ctx, p.cfg.Logger).Info("push", "event", "review-not-started", "why", "cost")
				return "", false, "the reviewer container runs on a sign-in that costs money, so the engine doesn't start reviews by itself; the person starts them from the page"
			case ok && e.Reason == "capped":
				return "", false, "the reviewers reached this project's round limit and still block, so the engine doesn't start them again; stop fixing and summarize their open findings for the person, who decides on the page"
			case ok && e.Reason == "limits":
				return "", false, e.Message
			case ok && e.Reason == "auto-capped":
				return "", false, "the engine already ran its limit of automatic reviews on this branch; the person starts more from the page"
			case ok && e.Reason == "reviews":
				p.waitAs(sid, head, shared.RequestOf(ctx))
				p.queueReview(sid, head)
				return "", false, "the engine reviews at most " + strconv.Itoa(MaxReviewing) + " sessions at once, so it starts the reviews of commit " + shortSHA(head) + " when one finishes, and pushes it itself if every review passes"
			case !ok || e.Reason != "reviewing":
				return "", false, ""
			}
		}
	}
	p.waitAs(sid, head, shared.RequestOf(ctx))
	return head, true, ""
}

func (p *Pushes) heldReviewed(sid, head, branch, req string) {
	p.wg.Go(func() {
		defer p.release(sid)
		log := shared.Tagged(p.cfg.Logger, req)
		kept := func(why string) {
			log.Info("push", "event", "card-kept", "why", why)
		}
		t, ok := p.cfg.Targets(sid)
		switch {
		case !ok:
			kept("target")
			return
		case t.Branch != branch:
			kept("branch")
			return
		}
		now, err := p.cfg.Heads.Head(t.Repo, t.Branch)
		switch {
		case err != nil:
			kept("head-unread")
			return
		case now != head:
			kept("head-moved")
			return
		}
		ctx, cancel := context.WithTimeout(shared.WithRequest(p.cfg.Base, req), PushFor)
		defer cancel()
		reply, _ := p.decide(ctx, sid, ByReviews, nil, head)
		log.Info("push", "event", "held-reviewed", "then", string(reply.Decision))
	})
}

func (p *Pushes) requiredAgents(sid string, t PushTarget, head string) []string {
	list := p.requiredOf(sid, t, head)
	out := make([]string, 0, len(list))
	for _, q := range list {
		out = append(out, q.Agent)
	}
	return out
}

func (p *Pushes) requiredOf(sid string, t PushTarget, head string) []workflow.Requirement {
	required, _ := p.requiredNow(sid, t, head)
	return required
}

func (p *Pushes) entriesAt(sid string, t PushTarget, head string) (map[review.Key]ReviewEntry, []workflow.Requirement, error) {
	entries, err := p.cfg.Reviews.Entries()
	if err != nil {
		return nil, nil, err
	}
	required, approval := p.requiredNow(sid, t, head)
	approval.hold(entries, t.Branch, head)
	return entries, required, nil
}

func (p *Pushes) requiredNow(sid string, t PushTarget, head string) ([]workflow.Requirement, ApprovalGiven) {
	if p.cfg.Work != nil {
		main, _ := p.cfg.Repos.Main(t.Name)
		if wf, ok := p.cfg.Work.PushFacts(sid, t, head, main); ok {
			return slices.Clone(wf.Required), wf.Approval
		}
	}
	out := make([]workflow.Requirement, 0, len(p.cfg.Agents))
	for role, a := range p.cfg.Agents {
		out = append(out, workflow.Requirement{Agent: a, Role: role})
	}
	return out, ApprovalGiven{}
}

func (p *Pushes) ReviewDone(sid, head string) {
	if p.halted(ByReviews) != "" {
		p.cfg.Logger.Info("push", "event", "held-by-halt")
		return
	}
	p.mu.Lock()
	if p.closing || p.refusedLocked(sid, head) {
		p.mu.Unlock()
		return
	}
	if p.waiting[sid] != head {
		h := p.held[sid]
		again := h != nil && h.commit == head && p.cfg.Now().Before(h.expires)
		branch, req := "", ""
		switch {
		case again && p.busy[sid]:
			p.recheck[sid], again = head, false
		case again:
			p.busy[sid], branch, req = true, h.branch, h.req
		}
		p.mu.Unlock()
		if again {
			p.heldReviewed(sid, head, branch, req)
		}
		return
	}
	if p.denialsUnread {
		delete(p.waiting, sid)
		p.mu.Unlock()
		p.saveWaiting()
		p.heldUnread(sid, ByReviews, p.cfg.Now())
		return
	}
	if p.busy[sid] {
		p.again[sid] = true
		p.mu.Unlock()
		return
	}
	req := p.waitedReq(sid, head)
	delete(p.waiting, sid)
	delete(p.waitReq, sid)
	p.busy[sid] = true
	p.mu.Unlock()
	p.wg.Go(func() {
		defer p.release(sid)
		ctx, cancel := context.WithTimeout(shared.WithRequest(p.cfg.Base, req), PushFor)
		defer cancel()
		reply, _ := p.decide(ctx, sid, ByReviews, nil, "")
		p.decided(sid, head, reply)
		if reply.Decision == push.DecisionHeld {
			p.autoFindings(ctx, sid)
		}
	})
}

func (p *Pushes) Push(ctx context.Context, sid string) error {
	if _, ok := p.cfg.Targets(sid); !ok {
		return explained(conflict("not started here"), "Only a session this engine started can be pushed from here.")
	}
	if err := p.pageHalted(); err != nil {
		return err
	}
	if err := p.claim(sid); err != nil {
		return err
	}
	if t, ok := p.cfg.Targets(sid); ok {
		if head, err := p.cfg.Heads.Head(t.Repo, t.Branch); err == nil {
			p.mu.Lock()
			p.unrefuseLocked(sid, head)
			p.mu.Unlock()
			p.saveDenials()
		}
	}
	p.start(sid, nil, shared.RequestOf(ctx))
	return nil
}

func (p *Pushes) claim(sid string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closing {
		return explained(conflict("stopping"), stoppingMessage)
	}
	if p.busy[sid] {
		return explained(conflict("pushing"), "A push of this session is being decided. Wait for its result.")
	}
	p.busy[sid] = true
	return nil
}

func (p *Pushes) release(sid string) {
	p.mu.Lock()
	delete(p.busy, sid)
	again, head := p.again[sid], p.waiting[sid]
	held := p.recheck[sid]
	delete(p.again, sid)
	delete(p.recheck, sid)
	p.mu.Unlock()
	if again && head != "" {
		p.ReviewDone(sid, head)
	}
	if held != "" {
		p.ReviewDone(sid, held)
	}
}

func (p *Pushes) start(sid string, answer *gate.Answer, req string) {
	p.mu.Lock()
	v := p.views[sid]
	v.Running = true
	p.views[sid] = v.clone()
	p.mu.Unlock()
	p.cfg.Show(sid, v.clone())
	p.wg.Go(func() {
		defer p.release(sid)
		ctx, cancel := context.WithTimeout(shared.WithRequest(p.cfg.Base, req), PushFor)
		defer cancel()
		p.decide(ctx, sid, ByPage, answer, "")
	})
}

func (p *Pushes) Answer(ctx context.Context, sid, card, commit string, allow bool, reason string) error {
	now := p.cfg.Now()
	p.mu.Lock()
	h := p.held[sid]
	if h == nil || h.card != card || h.commit != commit {
		p.mu.Unlock()
		return explained(conflict("card"), "That push card isn't the one waiting any more. Press Push again.")
	}
	if !now.Before(h.expires) {
		delete(p.held, sid)
		p.mu.Unlock()
		p.showCleared(sid)
		return explained(conflict("expired"), "That push card expired after 15 minutes. Press Push again.")
	}
	if p.closing {
		p.mu.Unlock()
		return explained(conflict("stopping"), stoppingMessage)
	}
	if p.busy[sid] {
		p.mu.Unlock()
		return explained(conflict("pushing"), "A push of this session is being decided. Wait for its result.")
	}
	if err := p.pageHalted(); allow && err != nil {
		p.mu.Unlock()
		return err
	}
	given := strings.TrimSpace(reason)
	clean := gate.Reason(reason)
	if given != "" && clean == "" {
		p.mu.Unlock()
		return explained(invalid("reason"), "A reason has at most 500 characters and no control characters.")
	}
	if allow && len(h.missing) > 0 && clean == "" {
		p.mu.Unlock()
		return explained(invalid("reason"), "Type your reason for allowing this push with items missing. The audit file keeps it.")
	}
	delete(p.held, sid)
	answer := &gate.Answer{Allow: allow, Reason: clean, Commit: h.commit, Branch: h.branch, Remote: h.remote, Expires: h.expires}
	if !allow {
		p.refuseLocked(sid, h.commit)
		delete(p.waiting, sid)
		p.mu.Unlock()
		p.saveDenials()
		p.saveWaiting()
		why := "the developer denied this push"
		if clean != "" {
			why += ": " + clean
		}
		t, _ := p.cfg.Targets(sid)
		e := AuditEntry{Time: now, Session: sid, Repository: t.Name, Branch: h.branch, Commit: h.commit, Remote: h.remote, By: ByPage,
			Result: string(gate.Denied), Reason: why, Missing: slices.Clone(h.missing), PersonReason: clean, Req: shared.RequestOf(ctx)}
		p.record(e)
		p.show(sid, PushView{Result: string(gate.Denied), Reason: why, Commit: h.commit, Branch: h.branch, Remote: h.remote, Missing: slices.Clone(h.missing), At: now, Audit: &e})
		said := "The person denied the push of commit " + shortSHA7(h.commit) + " to " + h.branch + " on Software Factory's page"
		if clean != "" {
			said += ": " + clip(clean, maxNoteReason)
		}
		p.cfg.Note(sid, said+". Software Factory won't push that commit on your request; commit a fix or ask the person what they want.")
		shared.For(ctx, p.cfg.Logger).Info("push", "event", "denied")
		return nil
	}
	p.unrefuseLocked(sid, h.commit)
	p.busy[sid] = true
	p.mu.Unlock()
	p.saveDenials()
	p.start(sid, answer, shared.RequestOf(ctx))
	return nil
}

func (p *Pushes) Handoff(ctx context.Context, sid, card, commit string) error {
	now := p.cfg.Now()
	p.mu.Lock()
	h := p.held[sid]
	switch {
	case h == nil || h.card != card || h.commit != commit:
		p.mu.Unlock()
		return explained(conflict("card"), "That push card isn't the one waiting any more. Press Push again.")
	case !h.handoff:
		p.mu.Unlock()
		return explained(conflict("no handoff"), "Hand off is for a push held by a reviewer that reached this project's round limit, and this one isn't.")
	case !now.Before(h.expires):
		delete(p.held, sid)
		p.mu.Unlock()
		p.showCleared(sid)
		return explained(conflict("expired"), "That push card expired after 15 minutes. Press Push again.")
	case p.closing:
		p.mu.Unlock()
		return explained(conflict("stopping"), stoppingMessage)
	case p.busy[sid]:
		p.mu.Unlock()
		return explained(conflict("pushing"), "A push of this session is being decided. Wait for its result.")
	}
	p.busy[sid] = true
	p.mu.Unlock()
	defer p.release(sid)
	if p.cfg.Tell == nil || p.cfg.Work == nil {
		return explained(conflict("no session"), "This engine can't send the session a message, so it can't hand off.")
	}
	if err := p.cfg.Tell(sid, p.cfg.Work.HandoffText(sid)); err != nil {
		if e, ok := errors.AsType[*Error](err); ok && e.Message != "" {
			return e
		}
		return explained(conflict("not sent"), "The handoff message couldn't reach the session, which must be running and controlled from this page, so the push wasn't denied. Try again once it is.")
	}
	p.mu.Lock()
	if p.held[sid] == h {
		delete(p.held, sid)
	}
	p.refuseLocked(sid, h.commit)
	delete(p.waiting, sid)
	p.mu.Unlock()
	p.saveDenials()
	p.saveWaiting()
	why := "the developer chose Hand off, so the push was denied and the session was asked for a handoff"
	t, _ := p.cfg.Targets(sid)
	e := AuditEntry{Time: now, Session: sid, Repository: t.Name, Branch: h.branch, Commit: h.commit, Remote: h.remote, By: ByPage,
		Result: string(gate.Denied), Reason: why, Missing: slices.Clone(h.missing), PersonReason: "Hand off", Req: shared.RequestOf(ctx)}
	p.record(e)
	p.show(sid, PushView{Result: string(gate.Denied), Reason: why, Commit: h.commit, Branch: h.branch, Remote: h.remote, Missing: slices.Clone(h.missing), At: now, Audit: &e})
	shared.For(ctx, p.cfg.Logger).Info("push", "event", "handoff")
	return nil
}

func (p *Pushes) showCleared(sid string) {
	p.mu.Lock()
	v := p.views[sid]
	v.Card, v.NeedsReason, v.Expires = "", false, time.Time{}
	if v.Result == string(gate.Held) {
		v.Reason = "the card expired; press Push again"
	}
	p.mu.Unlock()
	p.show(sid, v)
}

func (p *Pushes) show(sid string, v PushView) {
	v.Extra = p.extraFor(sid, v)
	p.mu.Lock()
	p.views[sid] = v.clone()
	p.mu.Unlock()
	p.cfg.Show(sid, v.clone())
}

func (p *Pushes) record(e AuditEntry) {
	if err := p.cfg.Audit.Append(e); err != nil {
		p.cfg.Logger.Warn("push", "event", "audit-not-written")
	}
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func shortSHA7(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

type pushing struct {
	p          *Pushes
	sid        string
	token      string
	entry      AuditEntry
	answer     *gate.Answer
	handoff    bool
	findings   bool
	passed     int
	overridden int
	unmet      []string
	refresh    bool
}

func (x *pushing) clean(s string) string {
	if x.token != "" {
		s = strings.ReplaceAll(s, x.token, "[token]")
	}
	return clipAtWord(s, maxPushReason)
}

func clipAtWord(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := clip(s, n-len("…"))
	if i := strings.LastIndexByte(cut, ' '); i > 0 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ;,:") + "…"
}

func (x *pushing) refreshCard(result gate.Result, reason string, missing []string) push.Reply {
	p := x.p
	kept := func(why string) push.Reply {
		p.cfg.Logger.Info("push", "event", "card-kept", "was", string(result), "why", why)
		return push.Reply{Decision: push.DecisionRefused, Reason: x.clean(reason)}
	}
	if result != gate.Held {
		return kept("outcome")
	}
	e := x.entry
	e.Reason = x.clean(reason)
	e = e.masked()
	p.mu.Lock()
	old := p.held[x.sid]
	if old == nil || old.commit != e.Commit || old.branch != e.Branch || old.remote != e.Remote || !p.cfg.Now().Before(old.expires) {
		p.mu.Unlock()
		return kept("card")
	}
	h := &heldPush{card: old.card, commit: old.commit, branch: old.branch, remote: old.remote, missing: slices.Clone(missing), expires: old.expires, handoff: x.handoff, findings: x.findings, sent: old.sent}
	p.held[x.sid] = h
	v := PushView{Result: string(gate.Held), Reason: e.Reason, Commit: h.commit, Branch: h.branch, Remote: h.remote, Missing: slices.Clone(missing), At: e.Time, Passed: x.passed, Overridden: x.overridden, Unmet: slices.Clone(x.unmet),
		Card: h.card, NeedsReason: len(missing) > 0, Expires: h.expires, Handoff: h.handoff, Findings: h.findings, FindingsSent: h.sent, Audit: p.views[x.sid].Audit}
	p.mu.Unlock()
	p.show(x.sid, v)
	shared.Tagged(p.cfg.Logger, x.entry.Req).Info("push", "event", "card-refreshed", "missing", len(missing))
	return push.Reply{Decision: push.DecisionHeld, Reason: e.Reason}
}

func (x *pushing) end(result gate.Result, reason string, missing []string, person string) push.Reply {
	p := x.p
	if x.refresh {
		return x.refreshCard(result, reason, missing)
	}
	reason = x.clean(reason)
	e := x.entry
	e.Result, e.Reason, e.Missing, e.PersonReason = string(result), reason, slices.Clone(missing), x.clean(person)
	e = e.masked()
	reason = e.Reason
	v := PushView{Result: string(result), Reason: reason, Commit: e.Commit, Branch: e.Branch, Remote: e.Remote, Missing: slices.Clone(missing), At: e.Time, Passed: x.passed, Overridden: x.overridden}
	if result == gate.Held {
		v.Unmet = slices.Clone(x.unmet)
	}
	p.mu.Lock()
	ended := (result == gate.Pushed || result == ResultUpToDate) && p.waiting[x.sid] == e.Commit
	if ended {
		delete(p.waiting, x.sid)
	}
	old := p.held[x.sid]
	delete(p.held, x.sid)
	if result == gate.Held && e.Commit != "" && e.Remote != "" {
		h := &heldPush{card: randomHex(16), commit: e.Commit, branch: e.Branch, remote: e.Remote, missing: slices.Clone(missing), expires: e.Time.Add(AllowFor), handoff: x.handoff, findings: x.findings, req: e.Req}
		if old != nil && old.commit == h.commit && old.branch == h.branch && old.remote == h.remote {
			h.sent = old.sent
			if e.Time.Before(old.expires) {
				h.card, h.expires = old.card, old.expires
			}
		}
		p.held[x.sid] = h
		v.Card, v.NeedsReason, v.Expires, v.Handoff = h.card, len(missing) > 0, h.expires, h.handoff
		v.Findings, v.FindingsSent = h.findings, h.sent
	}
	p.mu.Unlock()
	if ended {
		p.saveWaiting()
	}
	p.record(e)
	v.Audit = &e
	p.show(x.sid, v)
	shared.Tagged(p.cfg.Logger, e.Req).Info("push", "event", string(result), "by", e.By)
	said := x.said(result, reason, len(missing) > 0)
	if e.By == ByAgent {
		p.mu.Lock()
		p.spoken[x.sid] = said
		p.mu.Unlock()
	} else if said != "" {
		p.cfg.Note(x.sid, said)
	}
	decision := map[gate.Result]push.Decision{gate.Pushed: push.DecisionPushed, ResultUpToDate: push.DecisionPushed, gate.Held: push.DecisionHeld, gate.Denied: push.DecisionDenied}[result]
	if decision == "" {
		decision = push.DecisionRefused
	}
	return push.Reply{Decision: decision, Reason: reason}
}

func (x *pushing) said(result gate.Result, reason string, missing bool) string {
	e := x.entry
	sha, by := shortSHA7(e.Commit), e.By
	if sha == "" {
		return ""
	}
	why := clip(reason, maxNoteReason)
	switch {
	case result == gate.Pushed:
		how := ""
		switch {
		case x.answer != nil && x.answer.Allow && missing:
			how = " because the person allowed it on the page with items missing"
		case x.passed > 0 || x.overridden > 0:
			how = " after " + reviewWords(x.passed, x.overridden)
		}
		if by == ByPage && x.answer == nil {
			how = " when the person pressed Push on the page" + strings.Replace(how, " after ", ", after ", 1)
		}
		return "Software Factory pushed commit " + sha + " to " + e.Branch + how + "."
	case result == ResultUpToDate:
		if by == ByAgent {
			return "Software Factory found commit " + sha + " already on the remote's branch " + e.Branch + ", so it pushed nothing."
		}
		return ""
	case by == ByPage:
		return ""
	case result == gate.Held:
		return "Software Factory didn't push commit " + sha + " to " + e.Branch + ": " + why + ". The person decides on the page; don't call factory_push for this commit again."
	case result == gate.Refused || result == gate.Denied:
		return "Software Factory didn't push commit " + sha + " to " + e.Branch + ": " + why + "."
	case result == ResultUnclear:
		return "Software Factory can't tell whether commit " + sha + " reached " + e.Branch + ". The person checks on the page; don't call factory_push for this commit again."
	}
	return ""
}

func reviewWords(passed, overridden int) string {
	n := passed + overridden
	switch {
	case overridden == 0 && n == 1:
		return "its review passed"
	case overridden == 0 && n == 2:
		return "both reviews passed"
	case overridden == 0:
		return "all " + strconv.Itoa(n) + " reviews passed"
	case passed == 0 && n == 1:
		return "the person overrode its review's verdict"
	case passed == 0:
		return "the person overrode every review's verdict"
	}
	return strconv.Itoa(passed) + " of " + strconv.Itoa(n) + " reviews passed and the person overrode the rest"
}

func (p *Pushes) decide(ctx context.Context, sid, by string, answer *gate.Answer, card string) (push.Reply, gate.Decision) {
	x := &pushing{p: p, sid: sid, answer: answer, refresh: card != "", entry: AuditEntry{Time: p.cfg.Now(), Session: sid, By: by, Request: randomHex(16), Req: shared.RequestOf(ctx)}}
	refuse := func(why string) (push.Reply, gate.Decision) {
		return x.end(gate.Refused, why, nil, ""), gate.Decision{Result: gate.Refused, Reason: why}
	}
	t, ok := p.cfg.Targets(sid)
	if !ok {
		return refuse("only a session this engine started can push")
	}
	x.entry.Repository, x.entry.Branch = t.Name, t.Branch
	if why := p.halted(by); why != "" {
		return refuse(why)
	}
	url, token, err := p.cfg.Repos.PushTarget(t.Name)
	if err != nil {
		return refuse("the session's repository isn't in the engine's list any more")
	}
	x.token = token
	x.entry.Remote = url
	if token == "" {
		host, _ := HostOf(url)
		return refuse("no token is saved for " + host + "; save one on the page, then push again")
	}
	head, err := p.cfg.Heads.Head(t.Repo, t.Branch)
	if err != nil {
		return refuse("the engine couldn't read the session's branch " + t.Branch + ": " + err.Error())
	}
	x.entry.Commit = head
	if x.refresh && head != card {
		return refuse("the branch moved from commit " + shortSHA(card) + " to " + shortSHA(head) + ", so the held card stays as it was")
	}
	p.mu.Lock()
	unread, denied := p.denialsUnread && by != ByPage, p.refusedLocked(sid, head)
	p.mu.Unlock()
	switch {
	case unread && x.refresh:
		return refuse(denialsUnreadWhy)
	case unread:
		return x.end(gate.Held, denialsUnreadWhy, nil, ""), gate.Decision{Result: gate.Held, Reason: denialsUnreadWhy}
	case denied && by == ByPage:
		why := "the branch moved to commit " + shortSHA(head) + ", which the person denied, so the engine pushed nothing; press Push to push that commit anyway"
		return x.end(gate.Denied, why, nil, ""), gate.Decision{Result: gate.Denied, Reason: why}
	case denied:
		why := "the person denied commit " + shortSHA(head) + " on the page, so the engine doesn't push it on its own; commit a fix or tell the person what changed: they can push from the page"
		return x.end(gate.Denied, why, nil, ""), gate.Decision{Result: gate.Denied, Reason: why}
	}
	target := push.Target{URL: url, Token: token, Branch: t.Branch}
	remote, err := p.cfg.Pusher.Remote(ctx, target)
	if err != nil {
		return refuse("the engine couldn't ask the remote where its branches are: " + err.Error())
	}
	if !remote.Empty && remote.Main == t.Branch {
		return refuse("the session's branch is the remote's main branch, which the engine never pushes")
	}
	if remote.BranchCommit == head {
		return x.end(ResultUpToDate, "the remote's branch "+t.Branch+" already has commit "+shortSHA7(head)+", so there's nothing to push", nil, ""), gate.Decision{}
	}
	main := ""
	if !remote.Empty {
		main = remote.MainCommit
		fetched, err := p.cfg.Repos.Fresh(ctx, t.Name)
		if err != nil {
			why := "the engine couldn't fetch the remote's main branch"
			if e, ok := errors.AsType[*Error](err); ok && e.Message != "" {
				why += ": " + e.Message
			}
			return refuse(why)
		}
		if fetched != main {
			return refuse("the remote's main branch moved while the engine checked it; push again")
		}
		if now, ok := p.cfg.Targets(sid); ok && now.SyncFailed != "" && now.SyncFailed == fetched {
			return refuse(CloneBehind(fetched, now.SyncWhy) + "; the engine checks a push against the main it fetched, so it pushed nothing; push again")
		}
	}
	facts, refusal, err := p.facts(sid, t, head, main, remote.Main, by)
	if err != nil {
		return refuse(err.Error())
	}
	if refusal != "" {
		return refuse(refusal)
	}
	facts.Answer = answer
	for _, n := range facts.Capped {
		if n > 0 {
			x.handoff = true
		}
	}
	request := gate.Request{Commit: head, Branch: t.Branch, Remote: url}
	d := gate.Push(request, facts)
	if d.Result == gate.Allowed {
		for _, q := range facts.Required {
			recorded, passed, overridden := review.Outcome(facts.Reviews, review.Key{Commit: head, Branch: t.Branch, Agent: q.Agent}, q.RunCount())
			switch {
			case !recorded && !overridden:
			case passed:
				x.passed++
			case overridden:
				x.overridden++
			}
		}
	}
	note := ""
	if answer != nil && answer.Allow && answer.Commit != head {
		note = "the developer's Allow was for commit " + shortSHA(answer.Commit) + ", and the branch is at " + shortSHA(head) + " now, so it doesn't count; "
	}
	switch {
	case d.Result == gate.Allowed && x.refresh:
		return x.end(gate.Held, "every review of commit "+shortSHA(head)+" now passes and the push needs nothing more; the engine doesn't push a held card when its reviews finish, so press Push to push it", d.Missing, ""), d
	case d.Result == gate.Allowed:
		pre := x.entry
		pre.Result, pre.Reason, pre.Missing, pre.PersonReason = string(gate.Allowed), "pushing "+shortSHA(head)+" to "+t.Branch+" at "+url+"; the next entry says whether it went out", slices.Clone(d.Missing), x.clean(d.Exception)
		if err := p.cfg.Audit.Append(pre); err != nil {
			return x.end(gate.Refused, "the engine couldn't write its audit log, so it didn't push", d.Missing, d.Exception), d
		}
		res := p.cfg.Pusher.Push(ctx, push.Request{Repo: t.Repo, Target: target, Commit: head, Expected: remote.BranchCommit})
		if res.Outcome == push.Unclear {
			return x.unclear(target, remote.BranchCommit, res.Reason, d.Missing, d.Exception), d
		}
		if res.Outcome != push.Pushed {
			return x.end(gate.Refused, "the push failed: "+res.Reason, d.Missing, d.Exception), d
		}
		why := "pushed " + shortSHA(head) + " to " + t.Branch + " at " + url
		if res.Reason != "" {
			why += " (" + res.Reason + ")"
		}
		out := x.end(gate.Pushed, why, d.Missing, d.Exception)
		p.announcePushed(x, t, facts, len(d.Missing))
		return out, d
	case d.Result == gate.Held:
		x.findings = !x.handoff && p.hasFindings(sid, t, head)
		x.unmet = d.Unmet
		return x.end(gate.Held, note+p.afterReviews(by, sid, t, head)+d.Reason, d.Missing, ""), d
	case d.Result == gate.Denied:
		person := ""
		if answer != nil {
			person = gate.Reason(answer.Reason)
		}
		return x.end(gate.Denied, d.Reason, d.Missing, person), d
	}
	return x.end(gate.Refused, d.Reason, d.Missing, ""), d
}

func (p *Pushes) facts(sid string, t PushTarget, head, main, mainName, by string) (gate.Facts, string, error) {
	f := gate.Facts{Type: p.kind, Main: mainName, Now: p.cfg.Now(), Offers: map[string]review.Offer{}, Reviews: map[review.Key]review.Record{}, Capped: map[string]int{}}
	if lv, ok := p.cfg.Level(sid); ok {
		f.Level = lv
	}
	wf, worked := WorkFacts{}, false
	if p.cfg.Work != nil {
		wf, worked = p.cfg.Work.PushFacts(sid, t, head, main)
	}
	limit := DefaultRounds
	if p.cfg.Work != nil && !worked {
		f.Items = append(f.Items, "a work record for this session, which the engine doesn't have; stop the session and wake it from the page")
	}
	if worked {
		f.Type, f.Required, f.Items, f.NeedsApproval = wf.Type, wf.Required, slices.Clone(wf.Items), wf.NeedsApproval
		limit = roundLimit(wf.Rounds)
	} else {
		roles := workflow.MinimumRoles(p.kind.Edits)
		for _, role := range p.kind.Roles {
			if !slices.Contains(roles, role) {
				roles = append(roles, role)
			}
		}
		for _, role := range roles {
			if agent := p.cfg.Agents[role]; agent != "" {
				f.Required = append(f.Required, workflow.Requirement{Agent: agent, Role: role})
			}
		}
	}
	for _, q := range f.Required {
		f.Offers[q.Agent] = offerFor(p.cfg.RoleAgents, q.Agent)
	}
	var entries map[review.Key]ReviewEntry
	if p.cfg.Reviews != nil {
		var err error
		if entries, err = p.cfg.Reviews.Entries(); err != nil {
			return f, "", errors.New("the engine couldn't read its review records")
		}
	}
	if worked {
		wf.Approval.hold(entries, t.Branch, head)
	}
	for k, e := range entries {
		f.Reviews[k] = e.Record
	}
	for _, q := range f.Required {
		if rd := RoundOf(entries, t.Branch, q.Agent, false); rd.Capped(limit) {
			f.Capped[q.Agent] = rd.Count
		}
		for _, e := range runsOf(entries, review.Key{Commit: head, Branch: t.Branch, Agent: q.Agent}) {
			if why := coverProblem(p.cfg.History, t.Repo, e.Coverage(), head, main); why != "" {
				f.Items = append(f.Items, "a review by "+q.Agent+" of what would be pushed: review again, the remote's main moved ("+why+")")
				break
			}
		}
	}
	if by != ByPage && worked {
		f.Items = append(f.Items, p.holdItems(t, wf.Hold, entries, f.Required, head, main)...)
	}
	return f, wf.Refusal, nil
}

func (p *Pushes) Changes(sid string) (ChangeView, error) {
	t, ok := p.cfg.Targets(sid)
	if !ok {
		return ChangeView{}, explained(conflict("not started here"), "Only a session this engine started has a changes view here.")
	}
	head, err := p.cfg.Heads.Head(t.Repo, t.Branch)
	if err != nil {
		return ChangeView{}, explained(conflict("branch"), clip("The engine couldn't read the session's branch "+t.Branch+": "+err.Error(), maxOutput))
	}
	main, err := p.cfg.Repos.Main(t.Name)
	if err != nil {
		return ChangeView{}, explained(conflict("records"), "The engine couldn't read the repository's record.")
	}
	base := ""
	if main != "" {
		if base, err = p.cfg.History.MergeBase(t.Repo, head, main); err != nil {
			return ChangeView{}, explained(conflict("merge base"), clip("The engine couldn't find where "+t.Branch+" leaves the main branch: "+err.Error(), maxOutput))
		}
	}
	cs, err := p.cfg.Changes.Changes(t.Repo, base, head)
	if err != nil {
		return ChangeView{}, explained(conflict("changes"), clip("The engine couldn't read the change: "+err.Error(), maxOutput))
	}
	return ChangeView{Branch: t.Branch, Head: head, Base: base, Main: main, ChangeSet: maskChanges(cs)}, nil
}
