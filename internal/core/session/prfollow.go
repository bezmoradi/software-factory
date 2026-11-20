package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/gate"
	"github.com/bezmoradi/software-factory/internal/core/review"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	FollowEvery     = time.Minute
	FollowLook      = 7 * 24 * time.Hour
	MaxFollowRounds = 5
	MaxFollowBots   = 20
	MaxFollowed     = 500
	MaxFollowText   = 19000
	FollowLowShare  = 10
	followTick      = 15 * time.Second
	followRateWait  = 5 * time.Minute
	followMaxWait   = time.Hour
	followLoginFor  = time.Hour
	maxFollowShown  = 20
	MaxFollowSeen   = 50
	maxFollowNote   = 300
	FollowSendCalls = 10

	FollowLooking = "looking"
	FollowOpen    = "open"
	FollowMerged  = "merged"
	FollowClosed  = "closed"

	AuditPRFollow = "pr-follow"
	AuditPRReady  = "pr-ready"
	AuditAssign   = "assign"
	ResultReady   = "ready"
	ResultDone    = "done"
)

var BotLogin = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})(?:\[bot\])?$`)

type PullStatus struct {
	Number     int
	URL        string
	NodeID     string
	State      string
	Merged     bool
	Draft      bool
	Head       string
	HeadRef    string
	Base       string
	Mergeable  string
	MergeState string
}

type HostRate struct {
	Limit     int
	Remaining int
	Reset     time.Time
	Until     time.Time
}

type PullAPI interface {
	ForgeAPI
	PullStatus(ctx context.Context, r ForgeRepo, number int) (PullStatus, error)
	CheckStates(ctx context.Context, r ForgeRepo, commit string) (CheckReport, error)
	MarkReady(ctx context.Context, r ForgeRepo, nodeID string) (bool, error)
	AssignTo(ctx context.Context, r ForgeRepo, number int, login string) error
	OpenIssues(ctx context.Context, r ForgeRepo, label string, page int) ([]IssueFound, bool, error)
	IssueByNumber(ctx context.Context, r ForgeRepo, number int) (IssueFound, error)
	Rate(host string) HostRate
}

type FollowSettings struct {
	Bots   []string
	Auto   bool
	Assign bool
}

type SavedFollow struct {
	SID       string
	Project   string
	Branch    string
	Issue     int
	Watch     bool
	Pushed    time.Time
	Number    int
	URL       string
	Opened    bool
	Done      string
	DoneAt    time.Time
	CheckSent string
	SeenAt    time.Time
	SeenURLs  []string
	Rounds    int
	AutoStop  string
	IssueSent bool
	Reviewed  []ReviewedPush
	Counted   []string
}

type FollowRecord struct {
	Follows  []SavedFollow
	Projects map[ProjectID]FollowSettings
}

type FollowStore interface {
	Load() (FollowRecord, error)
	Save(FollowRecord) error
}

type FollowCheck struct {
	Name   string
	Result string
	URL    string
	Failed bool
}

type FollowView struct {
	Number     int
	URL        string
	State      string
	Draft      bool
	Opened     bool
	Head       string
	Conflict   bool
	Mergeable  string
	MergeState string
	Checks     []FollowCheck
	Failing    int
	Passing    int
	Running    int
	ChecksNew  bool
	Comments   []PullComment
	More       int
	Dropped    int
	Rounds     int
	Auto       bool
	AutoStop   string
	Polled     time.Time
	Note       string
	Issue      int
	IssueSent  bool
	Sent       time.Time
	Done       bool
}

type PullsPage interface {
	Send(sid string) error
	OpenPull(ctx context.Context, sid string) error
	Ready(ctx context.Context, sid string) error
	Settings(sid string) (string, FollowSettings, error)
	SetSettings(sid string, s FollowSettings) error
	Findings(sid string) (FindingOffer, error)
	OpenFindings(ctx context.Context, sid, commit string) (int, error)
}

var _ PullsPage = (*Follows)(nil)

type FollowsConfig struct {
	API        PullAPI
	Forges     *Forges
	Store      FollowStore
	Tell       func(sid, text string) error
	SignIn     func(sid string) (SignIn, bool)
	Sessions   func() []Info
	Show       func(sid string, v *FollowView)
	Note       func(sid, text string)
	Audit      Audit
	MainCommit func(name string) (string, error)
	Approved   func(ctx context.Context, sid string) string
	Reviews    func() (map[review.Key]ReviewEntry, error)
	Replies    func(sid string, e ReviewEntry) (string, int)
	Now        func() time.Time
	Logger     shared.Logger
}

type follow struct {
	saved    SavedFollow
	polled   time.Time
	pull     PullStatus
	checks   CheckReport
	checkKey string
	failing  bool
	pending  []PullComment
	dropped  int
	newest   time.Time
	atNewest []string
	note     string
	sent     time.Time
}

type loginAt struct {
	login string
	at    time.Time
}

type Follows struct {
	cfg      FollowsConfig
	skips    doubledSkips
	mu       sync.Mutex
	list     map[string]*follow
	projects map[ProjectID]FollowSettings
	logins   map[string]loginAt
	offered  map[string]string
	sends    *limiter
	readies  *limiter
	closed   bool
	saveMu   sync.Mutex
	version  uint64
	written  uint64
	wg       sync.WaitGroup
}

func NewFollows(cfg FollowsConfig) (*Follows, error) {
	if cfg.API == nil || cfg.Forges == nil {
		return nil, errors.New("follows: need the git host API and the forges")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = quiet{}
	}
	if cfg.Show == nil {
		cfg.Show = func(string, *FollowView) {}
	}
	if cfg.Note == nil {
		cfg.Note = func(string, string) {}
	}
	if cfg.Sessions == nil {
		cfg.Sessions = func() []Info { return nil }
	}
	cfg.Audit = masking(cfg.Audit)
	fl := &Follows{cfg: cfg, list: map[string]*follow{}, projects: map[ProjectID]FollowSettings{}, logins: map[string]loginAt{}, offered: map[string]string{},
		sends: newLimiter(FollowSendCalls, 10*time.Minute), readies: newLimiter(FollowSendCalls, 10*time.Minute)}
	cfg.Forges.useFollows(fl)
	return fl, nil
}

func (fl *Follows) Close() {
	fl.mu.Lock()
	fl.closed = true
	fl.mu.Unlock()
}

func (fl *Follows) Wait() { fl.wg.Wait() }

func (fl *Follows) Restore() int {
	if fl.cfg.Store == nil {
		return 0
	}
	rec, err := fl.cfg.Store.Load()
	if err != nil {
		fl.cfg.Logger.Warn("pull follow", "event", "state-unreadable")
		return 0
	}
	n := 0
	fl.mu.Lock()
	for id, s := range rec.Projects {
		if id.Name != "" && id.URL != "" {
			fl.projects[id] = cleanSettings(s)
		}
	}
	for _, s := range rec.Follows {
		t, ok := fl.cfg.Forges.cfg.Targets(s.SID)
		held := !ok && isDoubled(fl.cfg.Forges.cfg.Doubled, s.SID)
		if !held && (!ok || t.Name != s.Project || t.Branch != s.Branch) || fl.list[s.SID] != nil {
			continue
		}
		fl.list[s.SID] = &follow{saved: s}
		n++
	}
	sids := fl.sidsLocked()
	fl.mu.Unlock()
	for _, sid := range sids {
		fl.show(sid)
	}
	if n > 0 {
		fl.cfg.Logger.Info("pull follow", "event", "restored", "count", n)
	}
	return n
}

func (fl *Follows) sidsLocked() []string {
	out := make([]string, 0, len(fl.list))
	for sid := range fl.list {
		out = append(out, sid)
	}
	slices.Sort(out)
	return out
}

func cleanSettings(s FollowSettings) FollowSettings {
	var bots []string
	for _, b := range s.Bots {
		if BotLogin.MatchString(b) && len(bots) < MaxFollowBots && !slices.ContainsFunc(bots, func(x string) bool { return strings.EqualFold(x, b) }) {
			bots = append(bots, b)
		}
	}
	return FollowSettings{Bots: bots, Auto: s.Auto, Assign: s.Assign}
}

func (fl *Follows) projectID(project string) (ProjectID, bool) {
	if project == "" {
		return ProjectID{}, false
	}
	url, _, _, err := fl.cfg.Forges.cfg.Repos.ForgeTarget(project)
	if err != nil || url == "" {
		return ProjectID{}, false
	}
	return ProjectID{Name: project, URL: url}, true
}

func (fl *Follows) settingsOf(project string) FollowSettings {
	id, ok := fl.projectID(project)
	if !ok {
		return FollowSettings{}
	}
	fl.mu.Lock()
	defer fl.mu.Unlock()
	s := fl.projects[id]
	s.Bots = slices.Clone(s.Bots)
	return s
}

func (fl *Follows) sessionSettings(sid string) (string, FollowSettings) {
	t, ok := fl.cfg.Forges.cfg.Targets(sid)
	if !ok {
		return "", FollowSettings{}
	}
	return t.Name, fl.settingsOf(t.Name)
}

func (fl *Follows) ForgetProject(id ProjectID) {
	fl.mu.Lock()
	_, had := fl.projects[id]
	delete(fl.projects, id)
	var sids []string
	for sid, x := range fl.list {
		if x.saved.Project == id.Name {
			sids = append(sids, sid)
		}
	}
	fl.mu.Unlock()
	if !had {
		return
	}
	fl.save()
	for _, sid := range sids {
		fl.show(sid)
	}
}

func (fl *Follows) Settings(sid string) (string, FollowSettings, error) {
	t, ok := targetOrHeld(fl.cfg.Forges.cfg.Targets, fl.cfg.Forges.cfg.Held, sid)
	if !ok {
		return "", FollowSettings{}, notStartedHere(fl.cfg.Forges.cfg.Doubled, sid, "Only a session this engine started has pull request settings.")
	}
	return t.Name, fl.settingsOf(t.Name), nil
}

func (fl *Follows) SetSettings(sid string, s FollowSettings) error {
	t, ok := fl.cfg.Forges.cfg.Targets(sid)
	if !ok {
		return notStartedHere(fl.cfg.Forges.cfg.Doubled, sid, "Only a session this engine started has pull request settings.")
	}
	if len(s.Bots) > MaxFollowBots {
		return explained(invalid("bots"), "A project lists at most "+strconv.Itoa(MaxFollowBots)+" bot logins.")
	}
	for _, b := range s.Bots {
		if !BotLogin.MatchString(b) {
			return explained(invalid("bots"), "Each bot login is a GitHub login: letters, digits and hyphens, at most 39 characters, optionally ending in [bot].")
		}
	}
	s = cleanSettings(s)
	id, ok := fl.projectID(t.Name)
	if !ok {
		return explained(conflict("repository"), "The project's repository isn't in the engine's list any more.")
	}
	fl.mu.Lock()
	if s.Auto || s.Assign || len(s.Bots) > 0 {
		if _, known := fl.projects[id]; !known && len(fl.projects) >= MaxFollowed {
			fl.mu.Unlock()
			return explained(limited("projects"), "The engine keeps pull request settings for at most "+strconv.Itoa(MaxFollowed)+" projects.")
		}
		fl.projects[id] = s
	} else {
		delete(fl.projects, id)
	}
	var sids []string
	for sid, x := range fl.list {
		if x.saved.Project == t.Name {
			sids = append(sids, sid)
		}
	}
	fl.mu.Unlock()
	fl.save()
	for _, sid := range sids {
		fl.show(sid)
	}
	fl.cfg.Logger.Info("pull follow", "event", "settings", "bots", len(s.Bots), "auto", s.Auto, "assign", s.Assign)
	return nil
}

func (fl *Follows) assignOn(project string) bool {
	return fl.settingsOf(project).Assign
}

func (fl *Follows) recordLocked(sid string, t PushTarget) *follow {
	x := fl.list[sid]
	if x != nil && (x.saved.Project != t.Name || x.saved.Branch != t.Branch) {
		x = nil
	}
	if x == nil {
		if len(fl.list) >= MaxFollowed {
			return nil
		}
		x = &follow{saved: SavedFollow{SID: sid, Project: t.Name, Branch: t.Branch}}
		fl.list[sid] = x
	}
	return x
}

func (fl *Follows) SetIssue(sid string, number int) {
	t, ok := fl.cfg.Forges.cfg.Targets(sid)
	if !ok || number <= 0 {
		return
	}
	fl.mu.Lock()
	x := fl.recordLocked(sid, t)
	if x != nil {
		x.saved.Issue = number
	}
	fl.mu.Unlock()
	fl.save()
	fl.show(sid)
}

func (fl *Follows) issueSent(sid string, number int) {
	fl.mu.Lock()
	x := fl.list[sid]
	if x != nil && x.saved.Issue == number {
		x.saved.IssueSent = true
	}
	fl.mu.Unlock()
	fl.save()
	fl.show(sid)
}

func (fl *Follows) IssueOf(sid string) int {
	fl.mu.Lock()
	defer fl.mu.Unlock()
	if x := fl.list[sid]; x != nil {
		return x.saved.Issue
	}
	return 0
}

func (fl *Follows) pushed(p PushedFacts) {
	t, ok := fl.cfg.Forges.cfg.Targets(p.SID)
	if !ok || t.Branch != p.Branch {
		return
	}
	fl.mu.Lock()
	x := fl.recordLocked(p.SID, t)
	if x != nil && x.saved.Done == "" {
		if !x.saved.Watch {
			x.saved.SeenAt = fl.cfg.Now()
		}
		x.saved.Watch, x.saved.Pushed = true, fl.cfg.Now()
		x.polled = time.Time{}
		if r, ok := reviewedOf(p, fl.cfg.Now()); ok {
			x.saved.Reviewed = addReviewed(x.saved.Reviewed, r)
		}
	}
	fl.mu.Unlock()
	fl.save()
	fl.show(p.SID)
}

func (fl *Follows) opened(sid string, number int, url string) {
	t, ok := fl.cfg.Forges.cfg.Targets(sid)
	if !ok || number <= 0 {
		return
	}
	now := fl.cfg.Now()
	fl.mu.Lock()
	x := fl.recordLocked(sid, t)
	if x != nil {
		if !x.saved.Watch {
			x.saved.SeenAt = now
		}
		x.saved.Watch, x.saved.Number, x.saved.URL, x.saved.Opened, x.saved.Done = true, number, url, true, ""
		if x.saved.Pushed.IsZero() {
			x.saved.Pushed = now
		}
		x.polled = time.Time{}
	}
	fl.mu.Unlock()
	fl.save()
	fl.show(sid)
}

func (fl *Follows) Run(ctx context.Context) {
	t := time.NewTicker(followTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fl.round(ctx)
		}
	}
}

func (fl *Follows) round(ctx context.Context) {
	now := fl.cfg.Now()
	fl.mu.Lock()
	if fl.closed {
		fl.mu.Unlock()
		return
	}
	var due []string
	var gone []string
	var held []string
	for sid, x := range fl.list {
		if _, ok := fl.cfg.Forges.cfg.Targets(sid); !ok {
			if isDoubled(fl.cfg.Forges.cfg.Doubled, sid) {
				held = append(held, sid)
			} else {
				gone = append(gone, sid)
			}
			continue
		}
		if x.saved.Watch && x.saved.Done == "" && now.Sub(x.polled) >= FollowEvery {
			due = append(due, sid)
		}
	}
	for _, sid := range gone {
		delete(fl.list, sid)
	}
	fl.mu.Unlock()
	for _, sid := range held {
		fl.skips.skip(fl.cfg.Forges.cfg.Doubled, fl.cfg.Logger, sid, "pull-follow")
	}
	if len(gone) > 0 {
		fl.save()
	}
	slices.Sort(due)
	for _, sid := range due {
		if ctx.Err() != nil {
			return
		}
		fl.poll(ctx, sid)
	}
}

func (fl *Follows) backoff(host string) string {
	r := fl.cfg.API.Rate(host)
	now := fl.cfg.Now()
	if now.Before(r.Until) {
		return "GitHub's rate limit for the token was reached, so the engine waits until " + r.Until.UTC().Format("15:04 UTC") + " before it reads the pull request again"
	}
	if r.Limit > 0 && r.Remaining*100 < r.Limit*FollowLowShare && now.Before(r.Reset) {
		return "less than " + strconv.Itoa(FollowLowShare) + "% of the token's GitHub rate limit is left, so the engine waits until " + r.Reset.UTC().Format("15:04 UTC") + " before it reads the pull request again"
	}
	return ""
}

func (fl *Follows) setNote(sid, note string) {
	fl.mu.Lock()
	if x := fl.list[sid]; x != nil {
		x.note = clip(note, maxFollowNote)
	}
	fl.mu.Unlock()
	fl.show(sid)
}

func (fl *Follows) login(ctx context.Context, repo ForgeRepo) (string, error) {
	key := repo.Host + "\n" + ForgeHash(repo.Token, "")
	now := fl.cfg.Now()
	fl.mu.Lock()
	l, ok := fl.logins[key]
	fl.mu.Unlock()
	if ok && now.Sub(l.at) < followLoginFor {
		return l.login, nil
	}
	login, err := fl.cfg.API.Login(ctx, repo)
	if err != nil {
		return "", err
	}
	fl.mu.Lock()
	if len(fl.logins) > 100 {
		clear(fl.logins)
	}
	fl.logins[key] = loginAt{login: login, at: now}
	fl.mu.Unlock()
	return login, nil
}

func (fl *Follows) poll(ctx context.Context, sid string) {
	f := fl.cfg.Forges
	ft, why := f.target(sid, NeedPulls)
	now := fl.cfg.Now()
	fl.mu.Lock()
	x := fl.list[sid]
	if x == nil {
		fl.mu.Unlock()
		return
	}
	x.polled = now
	saved := x.saved
	fl.mu.Unlock()
	if why != "" {
		fl.setNote(sid, why)
		return
	}
	if ft.t.Branch != saved.Branch {
		return
	}
	if wait := fl.backoff(ft.repo.Host); wait != "" {
		fl.setNote(sid, wait)
		shared.For(ctx, fl.cfg.Logger).Info("pull follow", "event", "backoff")
		return
	}
	ctx, cancel := context.WithTimeout(ctx, ForgeFor)
	defer cancel()
	clean := func(s string) string { return f.clean(ft.repo.Token, s) }
	if saved.Number == 0 {
		if now.Sub(saved.Pushed) > FollowLook {
			fl.mu.Lock()
			x.saved.Watch = false
			fl.mu.Unlock()
			fl.save()
			fl.setNote(sid, "no pull request was opened for "+saved.Branch+" in the 7 days after its last push, so the engine stopped looking for one; it looks again after the next push")
			return
		}
		link, open, err := fl.cfg.API.OpenPull(ctx, ft.repo, saved.Branch)
		if err != nil {
			fl.setNote(sid, clean("the engine couldn't list the open pull requests: "+forgeWhy(err, NeedPulls)))
			return
		}
		if !open {
			fl.setNote(sid, "")
			return
		}
		fl.mu.Lock()
		x.saved.Number, x.saved.URL = link.Number, link.URL
		saved = x.saved
		fl.mu.Unlock()
		fl.save()
	}
	ps, err := fl.cfg.API.PullStatus(ctx, ft.repo, saved.Number)
	if err != nil {
		if fe, ok := errors.AsType[*ForgeError](err); ok && fe.Status == 404 {
			fl.mu.Lock()
			x.saved.Number, x.saved.URL, x.saved.Opened = 0, "", false
			fl.mu.Unlock()
			fl.save()
		}
		fl.setNote(sid, clean("the engine couldn't read pull request #"+strconv.Itoa(saved.Number)+": "+forgeWhy(err, NeedPulls)))
		return
	}
	if ps.State != "open" {
		fl.finish(sid, ft, ps)
		return
	}
	var report CheckReport
	key, failing, read := "", false, false
	note := ""
	if commitSHA.MatchString(ps.Head) {
		report, err = fl.cfg.API.CheckStates(ctx, ft.repo, ps.Head)
		if err != nil {
			note = clean("the engine couldn't read the CI results: " + forgeWhy(err, NeedStatuses))
		} else {
			key, failing = checkKey(ps.Head, report)
			read = true
		}
	}
	login, err := fl.login(ctx, ft.repo)
	var comments []PullComment
	if err != nil {
		note = clean("the engine couldn't read whose token it holds: " + forgeWhy(err, NeedMetadata))
	} else {
		rep, err := fl.cfg.API.PullComments(ctx, ft.repo, ps.Number)
		if err != nil {
			note = clean("the engine couldn't read the pull request's comments: " + forgeWhy(err, NeedPulls))
		} else {
			comments = rep.Comments
		}
	}
	bots := fl.settingsOf(saved.Project).Bots
	fl.mu.Lock()
	if fl.list[sid] != x {
		fl.mu.Unlock()
		return
	}
	x.pull, x.note = ps, note
	if read {
		x.checks, x.checkKey, x.failing = report, key, failing
	}
	if login != "" && comments != nil {
		x.pending, x.dropped, x.newest, x.atNewest = pendingComments(comments, login, bots, x.saved.SeenAt, x.saved.SeenURLs)
	}
	if x.saved.URL == "" {
		x.saved.URL = ps.URL
	}
	fl.mu.Unlock()
	fl.countAfterReview(sid, x, ft, ps.Head, report, read, comments)
	fl.show(sid)
	shared.For(ctx, fl.cfg.Logger).Info("pull follow", "event", "polled", "checks", len(report.Runs)+len(report.Statuses), "comments", len(comments))
	fl.auto(sid)
}

func (fl *Follows) finish(sid string, ft forgeTarget, ps PullStatus) {
	state := FollowClosed
	if ps.Merged {
		state = FollowMerged
	}
	fl.mu.Lock()
	x := fl.list[sid]
	if x == nil {
		fl.mu.Unlock()
		return
	}
	x.saved.Done, x.saved.DoneAt, x.pull, x.pending, x.note = state, fl.cfg.Now(), ps, nil, ""
	number, url := x.saved.Number, x.saved.URL
	fl.mu.Unlock()
	fl.save()
	fl.show(sid)
	word := "closed without merging"
	if state == FollowMerged {
		word = "merged"
	}
	fl.audit(sid, ft.t, ft.repo, ByEngine, ResultDone, randomHex(16), "pull request #"+strconv.Itoa(number)+" was "+word+" on the git host, so the engine marked the session done and stopped following it: "+url)
	fl.cfg.Note(sid, "Pull request #"+strconv.Itoa(number)+" for "+ft.t.Branch+" was "+word+" on the git host, so Software Factory stopped following it.")
	fl.cfg.Logger.Info("pull follow", "event", "done", "state", state)
}

func checkKey(head string, rep CheckReport) (string, bool) {
	var parts []string
	failing := false
	for _, r := range rep.Runs {
		if r.Status != "completed" {
			continue
		}
		parts = append(parts, "run\x00"+r.Name+"\x00"+r.Conclusion)
		failing = failing || failedRun(r)
	}
	for _, s := range rep.Statuses {
		if s.State == "pending" {
			continue
		}
		parts = append(parts, "status\x00"+s.Context+"\x00"+s.State)
		failing = failing || s.State == "failure" || s.State == "error"
	}
	if len(parts) == 0 {
		return "", false
	}
	slices.Sort(parts)
	sum := sha256.Sum256([]byte(head + "\x00" + strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:]), failing
}

func pendingComments(all []PullComment, login string, bots []string, seenAt time.Time, seenURLs []string) ([]PullComment, int, time.Time, []string) {
	var out []PullComment
	dropped := 0
	newest, atNewest := seenAt, slices.Clone(seenURLs)
	for _, c := range all {
		author := strings.EqualFold(c.Author, login) || slices.ContainsFunc(bots, func(b string) bool { return strings.EqualFold(b, c.Author) })
		if !author || strings.HasPrefix(strings.TrimSpace(c.Body), PushNoteMarker) {
			continue
		}
		if c.Created.Before(seenAt) || c.Created.Equal(seenAt) && slices.Contains(seenURLs, c.URL) {
			continue
		}
		switch {
		case c.Created.After(newest):
			newest, atNewest = c.Created, []string{c.URL}
		case c.Created.Equal(newest) && len(atNewest) < MaxFollowSeen:
			atNewest = append(atNewest, c.URL)
		}
		if secretText(c.Body) || flagText.MatchString(commentBody(c.Body)) {
			dropped++
			continue
		}
		out = append(out, c)
	}
	slices.SortStableFunc(out, func(a, b PullComment) int { return a.Created.Compare(b.Created) })
	return out, dropped, newest, atNewest
}

func (fl *Follows) viewLocked(x *follow, auto bool) *FollowView {
	s := x.saved
	v := &FollowView{Number: s.Number, URL: s.URL, Opened: s.Opened, Rounds: s.Rounds, AutoStop: s.AutoStop, Polled: x.polled, Note: x.note, Issue: s.Issue,
		IssueSent: s.IssueSent, Sent: x.sent, Auto: auto}
	switch {
	case s.Done != "":
		v.State, v.Done = s.Done, true
	case s.Number > 0:
		v.State = FollowOpen
	case s.Watch:
		v.State = FollowLooking
	}
	if x.pull.Number == s.Number && s.Number > 0 {
		p := x.pull
		v.Draft, v.Head, v.Mergeable, v.MergeState = p.Draft, p.Head, p.Mergeable, p.MergeState
		v.Conflict = s.Done == "" && (p.Mergeable == "no" || p.MergeState == "dirty")
	}
	if s.Done == "" {
		for _, r := range x.checks.Runs {
			c := FollowCheck{Name: CleanLine(r.Name, 200), URL: CleanLine(r.URL, 300), Result: CleanLine(r.Status, 40)}
			switch {
			case r.Status != "completed":
				v.Running++
			case failedRun(r):
				c.Failed, c.Result = true, CleanLine(r.Conclusion, 40)
				v.Failing++
			default:
				c.Result = CleanLine(r.Conclusion, 40)
				if r.Conclusion == "success" {
					v.Passing++
				}
			}
			if len(v.Checks) < 100 {
				v.Checks = append(v.Checks, c)
			}
		}
		for _, st := range x.checks.Statuses {
			c := FollowCheck{Name: CleanLine(st.Context, 200), URL: CleanLine(st.URL, 300), Result: CleanLine(st.State, 40)}
			switch st.State {
			case "failure", "error":
				c.Failed = true
				v.Failing++
			case "pending":
				v.Running++
			case "success":
				v.Passing++
			}
			if len(v.Checks) < 100 {
				v.Checks = append(v.Checks, c)
			}
		}
		v.ChecksNew = x.failing && x.checkKey != "" && x.checkKey != s.CheckSent
		list := x.pending
		if len(list) > maxFollowShown {
			v.More = len(list) - maxFollowShown
			list = list[len(list)-maxFollowShown:]
		}
		for _, c := range list {
			c.Author, c.Body, c.URL, c.Path, c.State, c.Commit = CleanLine(c.Author, 100), commentBody(c.Body), CleanLine(c.URL, 300), CleanLine(c.Path, 300), CleanLine(c.State, 40), CleanLine(c.Commit, 64)
			v.Comments = append(v.Comments, c)
		}
		v.Dropped = x.dropped
	}
	return v
}

func (fl *Follows) show(sid string) {
	project, settings := fl.sessionSettings(sid)
	fl.mu.Lock()
	x := fl.list[sid]
	var v *FollowView
	if x != nil {
		v = fl.viewLocked(x, x.saved.Project == project && settings.Auto)
	}
	fl.mu.Unlock()
	fl.cfg.Show(sid, v)
}

func (fl *Follows) save() {
	if fl.cfg.Store == nil {
		return
	}
	fl.mu.Lock()
	fl.version++
	version := fl.version
	rec := FollowRecord{Projects: map[ProjectID]FollowSettings{}}
	for _, sid := range fl.sidsLocked() {
		s := fl.list[sid].saved
		s.SeenURLs, s.Counted = slices.Clone(s.SeenURLs), slices.Clone(s.Counted)
		s.Reviewed = slices.Clone(s.Reviewed)
		for i := range s.Reviewed {
			s.Reviewed[i].Passed = slices.Clone(s.Reviewed[i].Passed)
		}
		rec.Follows = append(rec.Follows, s)
	}
	for id, s := range fl.projects {
		s.Bots = slices.Clone(s.Bots)
		rec.Projects[id] = s
	}
	fl.mu.Unlock()
	fl.saveMu.Lock()
	defer fl.saveMu.Unlock()
	if version <= fl.written {
		return
	}
	fl.written = version
	if err := fl.cfg.Store.Save(rec); err != nil {
		fl.cfg.Logger.Warn("pull follow", "event", "state-not-saved")
	}
}

func (fl *Follows) audit(sid string, t PushTarget, repo ForgeRepo, by, result, request, reason string) {
	fl.auditKind(AuditPRFollow, sid, t, repo, by, result, request, reason)
}

func (fl *Follows) auditKind(kind, sid string, t PushTarget, repo ForgeRepo, by, result, request, reason string) {
	if fl.cfg.Audit == nil {
		return
	}
	e := AuditEntry{Time: fl.cfg.Now(), Session: sid, Repository: t.Name, Branch: t.Branch, Remote: repo.Host + "/" + repo.Slug(), By: by, Result: result,
		Reason: clip(reason, maxForgeReason), Kind: kind, Request: request}
	if err := fl.cfg.Audit.Append(e); err != nil {
		fl.cfg.Logger.Warn("pull follow", "event", "audit-not-written")
	}
}

func (fl *Follows) Send(sid string) error {
	if err := heldBranch(fl.cfg.Forges.cfg.Doubled, sid); err != nil {
		return err
	}
	return fl.send(sid, ByPage)
}

func (fl *Follows) send(sid, by string) error {
	ft, why := fl.cfg.Forges.target(sid, NeedPulls)
	if why != "" {
		return explained(conflict("target"), why)
	}
	now := fl.cfg.Now()
	settings := fl.settingsOf(ft.t.Name)
	fl.mu.Lock()
	x := fl.list[sid]
	switch {
	case fl.closed:
		fl.mu.Unlock()
		return explained(conflict("stopping"), stoppingMessage)
	case x == nil || x.saved.Number == 0 || x.saved.Done != "":
		fl.mu.Unlock()
		return explained(conflict("not followed"), "The engine follows no open pull request for this session.")
	case by == ByPage && !fl.sends.allow(sid, now):
		fl.mu.Unlock()
		return explained(limited("send"), "What's new on the pull request was sent "+strconv.Itoa(FollowSendCalls)+" times in 10 minutes. Wait a few minutes.")
	}
	checksNew := x.failing && x.checkKey != "" && x.checkKey != x.saved.CheckSent
	if !checksNew && len(x.pending) == 0 {
		fl.mu.Unlock()
		return explained(conflict("nothing new"), "Nothing new on the pull request waits to be sent.")
	}
	pending := slices.Clone(x.pending)
	login := ""
	if l, ok := fl.logins[ft.repo.Host+"\n"+ForgeHash(ft.repo.Token, "")]; ok {
		login = l.login
	}
	var bots []string
	if x.saved.Project == ft.t.Name {
		bots = settings.Bots
	}
	checks, head, key, dropped := x.checks, x.pull.Head, x.checkKey, x.dropped
	newest, atNewest := x.newest, slices.Clone(x.atNewest)
	number, url := x.saved.Number, x.saved.URL
	fl.mu.Unlock()
	if !checksNew {
		checks = CheckReport{}
	}
	text, ok := FollowText(FollowFacts{Number: number, URL: url, Branch: ft.t.Branch, Repo: ft.repo, Login: login, Bots: bots, Head: head, Checks: checks, ChecksNew: checksNew,
		Comments: pending, Dropped: dropped})
	request := randomHex(16)
	if !ok {
		fl.audit(sid, ft.t, ft.repo, by, ResultWithheld, request, "what's new on pull request #"+strconv.Itoa(number)+" held a token-shaped string, so the engine didn't send it")
		return explained(conflict("withheld"), "What's new on the pull request holds what looks like a token, so the engine didn't send it to the session. Read it on the git host instead.")
	}
	if fl.cfg.Tell == nil {
		return explained(conflict("no session"), "This engine can't send the session a message.")
	}
	if err := fl.cfg.Tell(sid, strings.ReplaceAll(text, ft.repo.Token, "[token]")); err != nil {
		fl.audit(sid, ft.t, ft.repo, by, ResultNotSent, request, "what's new on pull request #"+strconv.Itoa(number)+" couldn't reach the session")
		if e, ok := errors.AsType[*Error](err); ok && e.Message != "" {
			return e
		}
		return explained(conflict("not sent"), "What's new couldn't reach the session, which must be running and controlled from this page.")
	}
	fl.mu.Lock()
	if fl.list[sid] == x {
		if checksNew {
			x.saved.CheckSent = key
		}
		if newest.After(x.saved.SeenAt) || newest.Equal(x.saved.SeenAt) {
			x.saved.SeenAt, x.saved.SeenURLs = newest, atNewest
		}
		x.pending, x.dropped, x.sent = nil, 0, fl.cfg.Now()
		if by == ByEngine {
			x.saved.Rounds++
		} else {
			x.saved.AutoStop = ""
		}
	}
	fl.mu.Unlock()
	fl.save()
	fl.show(sid)
	what := strconv.Itoa(len(pending)) + " " + plural(len(pending), "comment", "comments")
	if checksNew {
		what += " and the failed CI results of commit " + shortSHA(head)
	}
	fl.audit(sid, ft.t, ft.repo, by, ResultSent, request, "sent "+what+" from pull request #"+strconv.Itoa(number)+" to the session")
	fl.cfg.Logger.Info("pull follow", "event", "sent", "by", by, "comments", len(pending), "checks", checksNew)
	return nil
}

func (fl *Follows) autoStop(sid, why string) {
	fl.mu.Lock()
	x := fl.list[sid]
	changed := x != nil && x.saved.AutoStop != why
	if changed {
		x.saved.AutoStop = clip(why, maxFollowNote)
	}
	fl.mu.Unlock()
	if changed {
		fl.save()
		fl.show(sid)
		fl.cfg.Logger.Info("pull follow", "event", "auto-stopped")
	}
}

func (fl *Follows) auto(sid string) {
	project, settings := fl.sessionSettings(sid)
	fl.mu.Lock()
	x := fl.list[sid]
	if x == nil || fl.closed {
		fl.mu.Unlock()
		return
	}
	on := x.saved.Project == project && settings.Auto
	waiting := len(x.pending) > 0 || x.failing && x.checkKey != "" && x.checkKey != x.saved.CheckSent
	rounds, stopped := x.saved.Rounds, x.saved.AutoStop
	fl.mu.Unlock()
	if !on || !waiting || stopped != "" {
		return
	}
	if rounds >= MaxFollowRounds {
		fl.autoStop(sid, "the engine sent "+strconv.Itoa(MaxFollowRounds)+" rounds for this pull request on its own, its limit; send the rest yourself")
		return
	}
	if fl.cfg.SignIn == nil {
		fl.autoStop(sid, "the engine can't tell how the agent is signed in, so it sends nothing on its own")
		return
	}
	if s, ok := fl.cfg.SignIn(sid); !ok || s.Method != SignInSubscription {
		fl.autoStop(sid, "the agent isn't signed in on a subscription, so each round would be billed; send it yourself")
		return
	}
	var info *Info
	for _, i := range fl.cfg.Sessions() {
		if i.SID == sid {
			info = &i
			break
		}
	}
	switch {
	case info == nil:
		return
	case info.Login:
		fl.autoStop(sid, "the agent reported a sign-in problem")
		return
	case info.Needs > 0:
		fl.autoStop(sid, "a card waits for you on the page")
		return
	case info.Push != nil && info.Push.Result == string(gate.Held):
		fl.autoStop(sid, "a push waits for you on the page")
		return
	case info.StopReason != "" || strings.EqualFold(info.State, "failed") || strings.EqualFold(info.Status, "failed"):
		fl.autoStop(sid, "the agent's last turn failed or was stopped")
		return
	case !info.Running || info.Busy || !info.Controlled:
		return
	}
	if err := fl.send(sid, ByEngine); err != nil {
		why := "the engine couldn't send it"
		if e, ok := errors.AsType[*Error](err); ok && e.Message != "" {
			why += ": " + strings.TrimSuffix(e.Message, ".")
		}
		fl.autoStop(sid, why)
	}
}
