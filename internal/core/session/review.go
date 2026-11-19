package session

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/gate"
	"github.com/bezmoradi/software-factory/internal/core/level"
	"github.com/bezmoradi/software-factory/internal/core/review"
	"github.com/bezmoradi/software-factory/internal/core/shared"
	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

const (
	ReviewFor       = 20 * time.Minute
	ReviewProvider  = "knackpack@knackpack"
	MaxReviewerRuns = 8
	SignInKeptFor   = 10 * time.Minute
	maxReviewText   = 4000
	MaxFullReply    = 64 << 10
	MaxEarlierFile  = MaxFullReply + 16<<10
	clippedMiddle   = "[Software Factory: the middle of the reply is left out.]"
	keptRecords     = 500
	reviewCheck     = 90 * time.Second
	reviewSlack     = 30 * time.Second
	stopped         = "the engine stopped before this review finished"
)

var DefaultReviewers = map[review.Role]string{
	review.BugHunter:  "knackpack:adversarial-reviewer",
	review.Simplicity: "knackpack:slop-reviewer",
}

type ReviewEntry struct {
	Key          review.Key
	Role         review.Role
	Record       review.Record
	Base         string
	Since        string
	Covers       string
	Message      string
	Reason       string
	CostUSD      float64
	StartedAt    time.Time
	EndedAt      time.Time
	Clipped      bool
	Auto         bool
	PluginCommit string
	ClaudeCode   string
	Models       []string
	RunModel     string
	RunEffort    string
	Turns        int
	Denied       []string
	Failure      string
	Attempt      int
	AutoAttempts int
	Cited        CitedIDs
	Approved     string
	Team         string
	Reply        string
	ReplyClipped bool
}

type ReviewRecords interface {
	Load() ([]ReviewEntry, error)
	Save([]ReviewEntry) error
}

type ReviewReplies interface {
	Put(sid string, e ReviewEntry) error
	Get(sid string, k review.Key, started time.Time) (string, bool, bool)
	Keep(keys []review.Key) error
	Forget(sid string) error
	PutGiven(sid string, e ReviewEntry, g KeptGiven) error
	GetGiven(sid string, k review.Key, started time.Time) (KeptGiven, bool)
}

type Snapshot struct {
	ID       string
	Path     string
	Added    string
	TooLarge bool
	Commits  string
}

type SnapshotFile struct {
	Name string
	Text string
}

type Snapshots interface {
	Head(repo, branch string) (string, error)
	Write(repo, base, head string, extra []SnapshotFile) (Snapshot, error)
	WriteMade(base, head []SnapshotFile, commits string) (Snapshot, error)
	Rules(repo, commit string) (RuleSet, error)
	Remove(id string) error
}

type PluginSource interface {
	PluginPath(id string) (string, error)
}

type RoleReview struct {
	Role     review.Role
	Name     string
	Agent    string
	State    review.State
	Verdict  string
	Message  string
	HasFull  bool
	Reason   string
	CostUSD  float64
	Why      string
	Round    int
	Limit    int
	Since    string
	Previous bool
	Override string
	Run      int
	Runs     int
	Advisory bool
	Cited    CitedIDs
	Findings []FindingRef

	PluginCommit string
	ClaudeCode   string
	Models       []string

	Progress  *ReviewProgress
	Given     *RunGiven
	Unchecked bool
	started   time.Time
	approved  string
	team      string
}

type ReviewView struct {
	Commit         string
	Base           string
	Branch         string
	Running        bool
	Note           string
	Roles          []RoleReview
	SignIn         *SignIn
	CostUSD        float64
	SessionCostUSD float64
	TestList       *TestListView

	PlanReview *AsideView
	Explain    *AsideView

	Req string
}

func (v ReviewView) clone() ReviewView {
	v.Roles = slices.Clone(v.Roles)
	if v.TestList != nil {
		t := *v.TestList
		v.TestList = &t
	}
	v.PlanReview, v.Explain = v.PlanReview.copied(), v.Explain.copied()
	if v.SignIn != nil {
		s := *v.SignIn
		v.SignIn = &s
	}
	return v
}

type ReviewTarget struct {
	SID    string
	Repo   string
	Name   string
	Branch string
	CostOK bool
	Plan   func(ctx context.Context, base, head string) ([]workflow.Requirement, error)
	Rounds int
	Auto   bool
	Scope  ReviewScope

	Approved func(ctx context.Context, head, main string) (ApprovedText, error)
	Claims   string

	TeamRules func(ctx context.Context, base, head string) (TeamRules, error)
	CloneSync func(main string) string

	Req string
}

type ReviewStarter interface {
	Review(ctx context.Context, sid string, costOK, every bool) error
	Override(ctx context.Context, sid, agent, commit, reason string) error
	TestList(ctx context.Context, sid string, costOK bool) error
}

type ReviewsConfig struct {
	Host       Host
	Sandbox    Sandbox
	Snapshots  Snapshots
	Records    ReviewRecords
	Replies    ReviewReplies
	Minimum    review.Minimum
	Plugins    PluginSource
	Mains      Mains
	History    History
	Plugin     string
	Dir        string
	Command    string
	Agents     map[review.Role]string
	Timeout    time.Duration
	Base       context.Context
	Now        func() time.Time
	Show       func(sid string, v ReviewView)
	SignIns    SignInChecker
	Expected   *Expected
	ShowSignIn func(SignIn)
	Audit      Audit
	AgentFiles ReviewAgentFiles
	Logger     shared.Logger

	Walkthroughs Walkthroughs

	PluginCommit string
	ClaudeCode   string
}

type Reviews struct {
	cfg        ReviewsConfig
	mu         sync.Mutex
	running    map[string]int
	views      map[string]ReviewView
	rebuilt    map[string]string
	approvals  map[string]viewApproval
	approvalAt uint64
	store      sync.Mutex
	claude     string
	wg         sync.WaitGroup
	reviewing  int
	idle       chan struct{}
	done       func(sid, head string)
	signIn     *SignIn
	signStale  bool
	signGen    uint64
	tests      map[string]TestListView
	keyGate    func(context.Context) error

	rateHandlers []func(side string, w []RateWindow)
	testStore    sync.Mutex
	changeFacts  ChangeBlobs
	changeLocks  TestLocks
	changeSpecs  SpecProjects
	changeTypes  TypeChanges

	asides     map[string]AsideView
	roleModels func(path string, role review.Role) ModelChoice
	cited      CitedLookup
	citedNow   map[review.Key]citedRun
	lessons    *Lessons
	comments   func(ctx context.Context, project string) ([]PullComment, []string)
}

func (r *Reviews) OnDone(fn func(sid, head string)) {
	r.mu.Lock()
	r.done = fn
	r.mu.Unlock()
}

func (r *Reviews) finished(sid, head string) {
	r.mu.Lock()
	fn := r.done
	r.mu.Unlock()
	if fn != nil {
		fn(sid, head)
	}
}

func NewReviews(cfg ReviewsConfig) (*Reviews, error) {
	if cfg.Host == nil || cfg.Snapshots == nil || cfg.Records == nil || cfg.Plugins == nil || cfg.Mains == nil || cfg.History == nil {
		return nil, errors.New("reviews: need the reviewer's agent-host, snapshots, records, the plugin source, the main branch fetch and the history reader")
	}
	if !absolute(cfg.Dir) {
		return nil, errors.New("reviews: the reviews folder must be an absolute path")
	}
	cfg.Audit = masking(cfg.Audit)
	if cfg.Plugin == "" {
		cfg.Plugin = "knackpack"
	}
	if cfg.Command == "" {
		cfg.Command = "claude"
	}
	if cfg.Agents == nil {
		cfg.Agents = DefaultReviewers
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = ReviewFor
	}
	if cfg.Base == nil {
		cfg.Base = context.Background()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Show == nil {
		cfg.Show = func(string, ReviewView) {}
	}
	if cfg.ShowSignIn == nil {
		cfg.ShowSignIn = func(SignIn) {}
	}
	if cfg.Logger == nil {
		cfg.Logger = quiet{}
	}
	r := &Reviews{cfg: cfg, running: map[string]int{}, views: map[string]ReviewView{}, rebuilt: map[string]string{}, approvals: map[string]viewApproval{}, claude: cfg.ClaudeCode}
	if err := r.settle(); err != nil {
		return nil, err
	}
	r.loadTests()
	return r, nil
}

func (r *Reviews) settle() error {
	r.store.Lock()
	defer r.store.Unlock()
	list, err := r.cfg.Records.Load()
	if err != nil {
		return fmt.Errorf("reviews: the review records can't be read: %w", err)
	}
	changed := false
	for i := range list {
		if list[i].Record.State == review.Running {
			list[i].Record.State = review.Failed
			list[i].Reason = stopped
			list[i].EndedAt = r.cfg.Now()
			changed = true
		}
		if list[i].Record.State == review.Failed && list[i].Reason == stopped && list[i].Failure == "" {
			list[i].Failure = FailureNoOutcome
			changed = true
		}
	}
	if changed {
		return r.cfg.Records.Save(list)
	}
	return nil
}

func (r *Reviews) Records() (map[review.Key]review.Record, error) {
	r.store.Lock()
	defer r.store.Unlock()
	list, err := r.cfg.Records.Load()
	if err != nil {
		return nil, err
	}
	list = r.counted(list)
	out := make(map[review.Key]review.Record, len(list))
	for _, e := range list {
		out[e.Key] = e.Record
	}
	return out, nil
}

func (r *Reviews) Entries() (map[review.Key]ReviewEntry, error) {
	r.store.Lock()
	defer r.store.Unlock()
	list, err := r.cfg.Records.Load()
	if err != nil {
		return nil, err
	}
	list = r.counted(list)
	out := make(map[review.Key]ReviewEntry, len(list))
	for _, e := range list {
		out[e.Key] = e
	}
	return out, nil
}

func (r *Reviews) View(sid string) (ReviewView, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.views[sid]
	return r.asideLocked(sid, v.clone()), ok
}

func (r *Reviews) Forget(sid string) {
	r.mu.Lock()
	delete(r.views, sid)
	delete(r.rebuilt, sid)
	delete(r.approvals, sid)
	r.mu.Unlock()
}

func (r *Reviews) Wait() { r.wg.Wait() }

func cancelled(ctx context.Context) bool {
	return errors.Is(ctx.Err(), context.Canceled)
}

func loggedRole(role review.Role, agent string) string {
	if role == "" {
		return agent
	}
	return string(role)
}

func (r *Reviews) began() {
	r.mu.Lock()
	r.reviewing++
	r.mu.Unlock()
}

func (r *Reviews) ended() {
	r.mu.Lock()
	r.reviewing--
	if r.reviewing == 0 && r.idle != nil {
		close(r.idle)
		r.idle = nil
	}
	r.mu.Unlock()
}

func (r *Reviews) WaitRunning(limit time.Duration) int {
	r.mu.Lock()
	if r.reviewing == 0 {
		r.mu.Unlock()
		return 0
	}
	if r.idle == nil {
		r.idle = make(chan struct{})
	}
	idle := r.idle
	r.mu.Unlock()
	t := time.NewTimer(limit)
	defer t.Stop()
	select {
	case <-idle:
		return 0
	case <-t.C:
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reviewing
}

func (r *Reviews) roles() []review.Role {
	out := []review.Role{}
	for _, role := range review.Roles {
		if r.cfg.Agents[role] != "" {
			out = append(out, role)
		}
	}
	return out
}

func (r *Reviews) show(sid string, v ReviewView) {
	r.mu.Lock()
	v = r.keptLocked(sid, v)
	r.mu.Unlock()
	r.cfg.Show(sid, v)
}

func (r *Reviews) keptLocked(sid string, v ReviewView) ReviewView {
	r.citedOnto(&v)
	v.TestList = r.testListOf(sid)
	v.PlanReview, v.Explain = r.asideOf(AsidePlanReview, sid), r.asideOf(AsideExplain, sid)
	r.views[sid] = v.clone()
	return r.asideLocked(sid, v.clone())
}

type viewApproval struct {
	commit string
	given  ApprovalGiven
	ticket uint64
}

func (r *Reviews) asideLocked(sid string, v ReviewView) ReviewView {
	a, ok := r.approvals[sid]
	if !ok || a.commit != v.Commit {
		return r.uncheckedLocked(sid, v)
	}
	for i, role := range v.Roles {
		if why := a.given.setsAside(role.State, role.Advisory, role.approved, role.team); why != "" {
			v.Roles[i].State, v.Roles[i].Reason = review.Unknown, why
		}
	}
	return v
}

func (r *Reviews) approvalTicket() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.approvalAt++
	return r.approvalAt
}

func (r *Reviews) approvalIs(sid, commit string, ticket uint64, a ApprovalGiven) {
	r.mu.Lock()
	r.approvalLocked(sid, commit, ticket, a)
	r.mu.Unlock()
}

func (r *Reviews) approvalLocked(sid, commit string, ticket uint64, a ApprovalGiven) bool {
	if old, ok := r.approvals[sid]; ok && old.ticket > ticket {
		return false
	}
	r.approvals[sid] = viewApproval{commit: commit, given: a, ticket: ticket}
	delete(r.rebuilt, sid)
	return true
}

func (r *Reviews) ApprovalMoved(sid string, at func(commit string) (ApprovalGiven, bool)) {
	r.mu.Lock()
	v, ok := r.views[sid]
	r.mu.Unlock()
	if !ok || v.Commit == "" {
		return
	}
	commit := v.Commit
	ticket := r.approvalTicket()
	now, ok := at(commit)
	if !ok {
		return
	}
	r.mu.Lock()
	v, ok = r.views[sid]
	if !ok || v.Commit != commit || !r.approvalLocked(sid, commit, ticket, now) {
		r.mu.Unlock()
		return
	}
	shown := r.asideLocked(sid, v.clone())
	r.mu.Unlock()
	r.cfg.Show(sid, shown)
}

func (r *Reviews) run(t ReviewTarget, head, claude string, plans []plannedRole, plugin string, view ReviewView, held string, given reviewGiven) {
	ctx := shared.WithRequest(r.cfg.Base, t.Req)
	main := given.rules.main
	if why := r.check(ctx); why != "" {
		view.Running = false
		view.Note = strings.TrimSpace("Reviews didn't start: " + why + " " + held)
		for i := range plans {
			view.Roles[i].State = review.Failed
			view.Roles[i].Reason = "didn't start: " + why
		}
		r.show(t.SID, view)
		if cancelled(ctx) {
			shared.For(ctx, r.cfg.Logger).Info("review", "event", "stopped", "why", "sandbox")
		} else {
			shared.For(ctx, r.cfg.Logger).Warn("review", "event", "not-started", "why", "sandbox")
		}
		return
	}
	started := r.cfg.Now()
	entries := make([]ReviewEntry, len(plans))
	for i, p := range plans {
		entries[i] = ReviewEntry{Key: review.Key{Commit: head, Branch: t.Branch, Agent: p.q.Agent}.AtRun(p.run), Role: p.q.Role, Base: p.base, Since: p.since, Covers: p.covers, StartedAt: started,
			Record: review.Record{Provider: providerFor(r.cfg.AgentFiles, p.q.Agent), State: review.Running, Advisory: p.q.Advisory}, Auto: t.Auto || p.auto, PluginCommit: r.cfg.PluginCommit, ClaudeCode: claude,
			Attempt: p.attempt, AutoAttempts: p.autoAttempts + autoAttempt(t.Auto), Approved: given.approved.Given().Digest, Team: given.team.Digest()}
	}
	r.put(entries...)
	r.mu.Lock()
	for _, e := range entries {
		delete(r.citedNow, e.Key)
	}
	r.mu.Unlock()
	view.Note = held
	r.show(t.SID, view)

	scanned := slices.Clone(given.approved.Files)
	if f, ok := claimsFile(t.Claims); ok {
		scanned = append(scanned, f)
		given.claims = true
	}
	if given.rules.main != "" {
		set, err := r.cfg.Snapshots.Rules(t.Repo, given.rules.main)
		if err != nil {
			shared.For(ctx, r.cfg.Logger).Warn("review", "event", "rules-unreadable")
		}
		given.rules = rulesOf(given.rules.main, set, err)
	}
	common := append(slices.Clone(scanned), given.rules.files...)
	givens := make([]reviewGiven, len(plans))
	extra := map[string][]SnapshotFile{}
	facts := map[string]bool{}
	for i, p := range plans {
		givens[i] = given
		givens[i].run, givens[i].runs = p.run, p.runs
		if _, ok := extra[p.base]; !ok {
			extra[p.base], facts[p.base] = r.withChangeFacts(slices.Clone(common), t.SID, t.Repo, p.base, head)
		}
		givens[i].facts = facts[p.base]
		var f SnapshotFile
		said := earlierNone
		if p.since != "" {
			f, said = earlierFile(p.since, p.previous, p.overridden)
		}
		if said == earlierNone && p.overridden != nil {
			f, said = SnapshotFile{Text: overriddenText(*p.overridden)}, earlierAccepted
		}
		if said != earlierNone {
			f.Name = earlierName(p.q.Agent, p.run)
			givens[i].earlier, givens[i].said = f.Name, said
			extra[p.base] = append(extra[p.base], f)
		}
	}
	dirs := map[string]string{}
	written := map[string]Snapshot{}
	for i, p := range plans {
		if _, done := dirs[p.base]; done {
			continue
		}
		snap, err := r.cfg.Snapshots.Write(t.Repo, p.base, head, extra[p.base])
		if err != nil {
			dirs[p.base] = ""
			why := clip("the engine couldn't write the snapshot to review: "+err.Error(), 400)
			for j := i; j < len(plans); j++ {
				if plans[j].base == p.base {
					entries[j] = r.failed(entries[j], why)
					entries[j].Failure = FailureError
					view.Roles[j] = roleOf(view.Roles[j], entries[j])
					r.audited(t, entries[j])
				}
			}
			shared.For(ctx, r.cfg.Logger).Warn("review", "event", "snapshot-failed")
			continue
		}
		dirs[p.base] = strings.TrimRight(r.cfg.Dir, "/") + "/" + snap.ID
		written[p.base] = snap
		defer func() {
			if err := r.cfg.Snapshots.Remove(snap.ID); err != nil {
				shared.For(ctx, r.cfg.Logger).Warn("review", "event", "snapshot-not-removed")
			}
		}()
	}
	for i, p := range plans {
		if snap, ok := written[p.base]; ok {
			entries[i].Record.Shaped = shapedFor(review.RuleFor(p.q.Agent), snap, scanned)
		}
	}
	if why := r.placeAgents(ctx, plans); why != "" {
		for i, p := range plans {
			if _, uploaded := r.cfg.AgentFiles.Uploaded(p.q.Agent); uploaded && dirs[p.base] != "" && entries[i].Record.State == review.Running {
				entries[i] = r.failed(entries[i], why)
				entries[i].Failure = FailureError
				view.Roles[i] = roleOf(view.Roles[i], entries[i])
				r.audited(t, entries[i])
			}
		}
	}
	r.put(entries...)
	prompts := make([]string, len(entries))
	for i := range entries {
		if dirs[plans[i].base] == "" || entries[i].Record.State != review.Running {
			continue
		}
		prompts[i] = promptFor(entries[i], givens[i])
		if providerFor(r.cfg.AgentFiles, entries[i].Key.Agent) == UploadedProvider {
			prompts[i] += AgentVerdictAsk
		}
		view.Roles[i].Given = givenOf(ReviewArgvWith(r.cfg.Command, plugin, entries[i].Key.Agent, prompts[i], r.roleModelFor(t.Repo, entries[i].Role)), plans[i].base, head, extra[plans[i].base])
	}
	for i := range entries {
		r.keepGiven(t.SID, entries[i], keptGivenOf(plans[i].q.Why, view.Roles[i].Given))
	}
	r.show(t.SID, view.clone())
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := range entries {
		dir := dirs[plans[i].base]
		if dir == "" || entries[i].Record.State != review.Running {
			continue
		}
		prompt := prompts[i]
		wg.Go(func() {
			e, why := r.reviewWith(ctx, t, entries[i], dir, plugin, prompt, r.progressShown(t.SID, &mu, &view, i))
			e = classified(e, why)
			r.put(e)
			r.keepReply(t.SID, e)
			r.audited(t, e)
			r.learned(t, e)
			if t.Approved != nil {
				ticket := r.approvalTicket()
				if a, err := t.Approved(ctx, head, main); err == nil {
					now := a.Given()
					now.Team = given.team.Digest()
					r.approvalIs(t.SID, head, ticket, now)
				}
			}
			mu.Lock()
			entries[i] = e
			view.Roles[i] = roleOf(view.Roles[i], e)
			view.CostUSD += e.CostUSD
			view.SessionCostUSD = r.branchCost(t.Branch)
			shown := view.clone()
			mu.Unlock()
			r.show(t.SID, shown)
			if why != "" {
				shared.For(ctx, r.cfg.Logger).Warn("review", "event", "finished", "role", loggedRole(e.Role, e.Key.Agent), "state", string(e.Record.State), "why", why)
			} else {
				shared.For(ctx, r.cfg.Logger).Info("review", "event", "finished", "role", loggedRole(e.Role, e.Key.Agent), "state", string(e.Record.State))
			}
			r.wg.Go(func() {
				if c, looked := r.lookUpCited(ctx, e); looked {
					r.markCited(e.Key, e.StartedAt, c)
					r.noteCited(t.SID, e.Key, e.StartedAt, c)
				}
			})
		})
	}
	wg.Wait()
	r.mu.Lock()
	if _, held := r.running[t.SID]; held {
		r.running[t.SID] = 0
	}
	r.mu.Unlock()
	view.Running = false
	mu.Lock()
	shown := view.clone()
	mu.Unlock()
	r.show(t.SID, shown)
}

func (r *Reviews) heldLocked() int {
	n := 0
	for _, runs := range r.running {
		n += runs
	}
	return n
}

func (r *Reviews) holdLocked(key string) error {
	return r.holdRunsLocked(key, 1)
}

func (r *Reviews) holdRunsLocked(key string, runs int) error {
	held := r.heldLocked()
	if held >= MaxReviewerRuns || held > 0 && held+runs > MaxReviewerRuns {
		return explained(limited("reviews"), fmt.Sprintf("The engine runs at most %d reviewer runs at once, each in a reviewer container of its own. Wait for one to finish.", MaxReviewerRuns))
	}
	r.running[key] = runs
	return nil
}

func (r *Reviews) growRuns(key string, runs int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	others := r.heldLocked() - r.running[key]
	if others > 0 && others+runs > MaxReviewerRuns {
		return false
	}
	r.running[key] = max(runs, 1)
	return true
}

func requestOr(ctx context.Context, req string) string {
	if req != "" {
		return req
	}
	return shared.RequestOf(ctx)
}

func (r *Reviews) check(ctx context.Context) string {
	if r.cfg.Sandbox == nil {
		return "this engine has no sandbox check for the reviewer containers"
	}
	if why := r.keyRouted(ctx); why != "" {
		return why
	}
	cctx, cancel := context.WithTimeout(ctx, reviewCheck)
	defer cancel()
	o := r.cfg.Sandbox.Check(cctx)
	if o.Event == level.CheckPassed {
		return ""
	}
	why := "the reviewers' sandbox check of egress failed"
	if o.Event != level.CheckFailed {
		why = "the reviewers' sandbox check of egress couldn't run"
	}
	if len(o.Parts) > 0 {
		why += ": " + strings.Join(o.Parts, "; ")
	}
	return clip(why, maxOutput)
}

func roleOf(v RoleReview, e ReviewEntry) RoleReview {
	v.State = e.Record.State
	v.Verdict = e.Record.Verdict
	v.Message = e.Message
	v.HasFull = e.Clipped && e.Reply != ""
	v.Reason = e.Reason
	v.CostUSD = e.CostUSD
	v.PluginCommit, v.ClaudeCode, v.Models = e.PluginCommit, e.ClaudeCode, slices.Clone(e.Models)
	v.Cited = e.Cited.clone()
	v.Findings = nil
	if e.Record.State == review.Pass || e.Record.State == review.Block || e.Record.State == review.Unknown {
		text := e.Message
		if e.Reply != "" {
			text = e.Reply
		}
		v.Findings = FindingRefs(text)
	}
	v.started = e.StartedAt
	v.approved, v.team = e.Approved, e.Team
	return v
}

func (r *Reviews) failed(e ReviewEntry, why string) ReviewEntry {
	e.Record.State = review.Failed
	e.Record.Verdict = ""
	e.Reason = why
	e.EndedAt = r.cfg.Now()
	return e
}

func (r *Reviews) put(entries ...ReviewEntry) {
	r.store.Lock()
	defer r.store.Unlock()
	list, err := r.cfg.Records.Load()
	if err != nil {
		r.cfg.Logger.Warn("review", "event", "records-unreadable")
		return
	}
	for _, e := range entries {
		if i := slices.IndexFunc(list, func(x ReviewEntry) bool { return x.Key.SameRole(e.Key) && x.Record.Override != "" }); i >= 0 && e.Record.Override == "" {
			e.Record.Override = list[i].Record.Override
		}
		list = slices.DeleteFunc(list, func(x ReviewEntry) bool { return x.Key == e.Key })
		list = append(list, e)
	}
	list = keptBranches(list, keptRecords)
	if err := r.cfg.Records.Save(list); err != nil {
		r.cfg.Logger.Warn("review", "event", "records-not-saved")
	}
	if r.cfg.Replies != nil {
		keys := make([]review.Key, 0, len(list))
		for _, e := range list {
			keys = append(keys, e.Key)
		}
		if err := r.cfg.Replies.Keep(keys); err != nil {
			r.cfg.Logger.Warn("review", "event", "replies-not-swept")
		}
	}
}

func (r *Reviews) keepReply(sid string, e ReviewEntry) {
	if r.cfg.Replies == nil {
		return
	}
	r.store.Lock()
	defer r.store.Unlock()
	if err := r.cfg.Replies.Put(sid, e); err != nil {
		r.cfg.Logger.Warn("review", "event", "reply-not-saved")
	}
}

func (r *Reviews) withReply(sid string, e ReviewEntry) ReviewEntry {
	if e.Reply != "" || e.Message == "" || r.cfg.Replies == nil {
		return e
	}
	if text, clipped, ok := r.cfg.Replies.Get(sid, e.Key, e.StartedAt); ok {
		e.Reply, e.ReplyClipped = text, clipped
	}
	return e
}

func (r *Reviews) ReplyOf(sid string, e ReviewEntry) (string, int) {
	e = r.withReply(sid, e)
	switch {
	case e.Reply == "" && e.Clipped:
		return e.Message, maxReviewText
	case e.Reply == "":
		return e.Message, 0
	case e.ReplyClipped:
		return e.Reply, MaxFullReply
	}
	return e.Reply, 0
}

func (r *Reviews) ForgetReplies(sid string) {
	if r.cfg.Replies == nil {
		return
	}
	r.store.Lock()
	defer r.store.Unlock()
	if err := r.cfg.Replies.Forget(sid); err != nil {
		r.cfg.Logger.Warn("review", "event", "replies-not-removed")
	}
}

func keptBranches(list []ReviewEntry, limit int) []ReviewEntry {
	for len(list) > limit {
		last := map[string]int{}
		for i, e := range list {
			last[e.Key.Branch] = i
		}
		if len(last) < 2 {
			return list[len(list)-limit:]
		}
		oldest, at := "", len(list)
		for b, i := range last {
			if i < at {
				oldest, at = b, i
			}
		}
		list = slices.DeleteFunc(list, func(e ReviewEntry) bool { return e.Key.Branch == oldest })
	}
	return list
}

func (r *Reviews) branchCost(branch string) float64 {
	r.store.Lock()
	defer r.store.Unlock()
	list, err := r.cfg.Records.Load()
	if err != nil {
		return 0
	}
	total := 0.0
	for _, e := range list {
		if e.Key.Branch == branch {
			total += e.CostUSD
		}
	}
	return total
}

func (r *Reviews) CheckSignIn(ctx context.Context) (SignIn, error) {
	if r.cfg.SignIns == nil {
		return SignIn{}, explained(conflict("no sign-in check"), "This engine can't check the reviewers' sign-in, so it doesn't start reviews.")
	}
	started := r.cfg.Now()
	r.mu.Lock()
	gen := r.signGen
	r.mu.Unlock()
	sctx, cancel := context.WithTimeout(ctx, signInFor)
	defer cancel()
	s, err := r.cfg.SignIns.SignIn(sctx, Command{Argv: []string{r.cfg.Command, "auth", "status"}, Dir: r.cfg.Dir, Pass: []string{"DISABLE_AUTOUPDATER"}, Timeout: signInFor}, "claude")
	if busy := reviewersBusy(err); busy != nil {
		return SignIn{}, busy
	}
	if err != nil {
		if cancelled(ctx) {
			shared.For(ctx, r.cfg.Logger).Info("review sign-in check", "event", "stopped")
		} else {
			shared.For(ctx, r.cfg.Logger).Warn("review sign-in check", "event", "failed")
		}
		return SignIn{}, explained(conflict("sign-in check"), "The engine couldn't check the reviewers' sign-in, so the review doesn't start: "+hostMessage(err))
	}
	s.CheckedAt = started
	r.mu.Lock()
	current := r.signGen == gen
	if current {
		r.signIn = &s
		r.signStale = false
	}
	r.mu.Unlock()
	if current {
		r.cfg.ShowSignIn(s)
	}
	shared.For(ctx, r.cfg.Logger).Info("review sign-in check", "method", string(s.Method), "kept", current)
	return s, nil
}

func (r *Reviews) KnownSignIn(ctx context.Context) (SignIn, error) {
	r.mu.Lock()
	s, stale := r.signIn, r.signStale
	r.mu.Unlock()
	if s != nil && !stale && r.cfg.Now().Sub(s.CheckedAt) < SignInKeptFor {
		return *s, nil
	}
	fresh, err := r.CheckSignIn(ctx)
	if err != nil && isBusy(err) && s != nil {
		kept := *s
		kept.Busy, kept.Stale = true, stale
		return kept, nil
	}
	return fresh, err
}

func (r *Reviews) signInChanging() {
	r.mu.Lock()
	r.signGen++
	r.mu.Unlock()
}

func (r *Reviews) SignInChanged() {
	r.mu.Lock()
	r.signStale = true
	r.signGen++
	var shown *SignIn
	if r.signIn != nil {
		s := *r.signIn
		s.Stale = true
		shown = &s
	}
	r.mu.Unlock()
	if shown != nil {
		r.cfg.ShowSignIn(*shown)
	}
}

func (r *Reviews) SignIn() (SignIn, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.signIn == nil {
		return SignIn{}, false
	}
	s := *r.signIn
	s.Stale = r.signStale
	return s, true
}

func (r *Reviews) Override(sid string, t ReviewTarget, agent, commit, reason string) error {
	clean := gate.Reason(reason)
	if clean == "" {
		return explained(invalid("reason"), "An override needs your reason, at most 500 characters with no control characters. The audit file keeps it.")
	}
	head, err := r.cfg.Snapshots.Head(t.Repo, t.Branch)
	if err != nil {
		return explained(conflict("branch"), clip("The engine couldn't read the session's branch "+t.Branch+": "+err.Error(), maxOutput))
	}
	if head != commit {
		return explained(conflict("moved"), "The branch moved on since the page showed this verdict, so it wasn't overridden. Read the latest review first.")
	}
	k := review.Key{Commit: head, Branch: t.Branch, Agent: agent}
	r.store.Lock()
	list, err := r.cfg.Records.Load()
	if err != nil {
		r.store.Unlock()
		return explained(conflict("records"), "The engine couldn't read its review records.")
	}
	var blocking []int
	found, running, overridden := false, false, false
	for i, e := range list {
		if !e.Key.SameRole(k) {
			continue
		}
		found = true
		running = running || e.Record.State == review.Running
		switch {
		case e.Record.State == review.Block && e.Record.Override == "":
			blocking = append(blocking, i)
		case e.Record.State == review.Block:
			overridden = true
		}
	}
	switch {
	case !found:
		r.store.Unlock()
		return explained(conflict("no review"), "There's no review by "+agent+" of this commit to override.")
	case running:
		r.store.Unlock()
		return explained(conflict("reviewing"), "A run of "+agent+" on this commit is still going. Wait for it to finish, then override.")
	case len(blocking) == 0 && overridden:
		r.store.Unlock()
		return explained(conflict("overridden"), "You already overrode this verdict.")
	case len(blocking) == 0:
		r.store.Unlock()
		return explained(conflict("not blocking"), "Only a blocking verdict can be overridden, and "+agent+"'s verdict on this commit doesn't block.")
	}
	for _, i := range blocking {
		list[i].Record.Override = clean
	}
	e := list[blocking[0]]
	if err := r.cfg.Records.Save(list); err != nil {
		r.store.Unlock()
		return explained(conflict("records"), "The engine couldn't save the override.")
	}
	r.store.Unlock()
	r.mu.Lock()
	v, shown := r.views[sid]
	if shown && v.Commit == head {
		v = v.clone()
		for j := range v.Roles {
			if v.Roles[j].Agent == agent && v.Roles[j].State == review.Block {
				v.Roles[j].Override = clean
			}
		}
	}
	r.mu.Unlock()
	if shown && v.Commit == head {
		r.show(sid, v)
	}
	if r.cfg.Audit != nil {
		ae := AuditEntry{Time: r.cfg.Now(), Session: sid, Repository: t.Name, Branch: t.Branch, Commit: head, By: ByPage, Kind: AuditOverride,
			Result: AuditOverride, Reason: "the developer overrode the verdict \"" + e.Record.Verdict + "\" from " + agent, Reviewer: agent, Verdict: e.Record.Verdict, PersonReason: clean, Req: t.Req}
		if err := r.cfg.Audit.Append(ae); err != nil {
			shared.Tagged(r.cfg.Logger, t.Req).Warn("review", "event", "audit-not-written")
		}
	}
	shared.Tagged(r.cfg.Logger, t.Req).Info("review", "event", "overridden")
	return nil
}
