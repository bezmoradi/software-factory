package session

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

type ForgeKind string

const (
	ForgeIssue ForgeKind = "issue"
	ForgePull  ForgeKind = "pull-request"

	ForgeWaiting = "waiting"
	ForgeSending = "sending"
	ForgeOpened  = "opened"
	ForgeExists  = "exists"
	ForgeDenied  = "denied"
	ForgeFailed  = "failed"

	ForgeUnclear = "unclear"

	ForgeHeld    = "held"
	ForgeRefused = "refused"
	ForgeReady   = "ok"

	ForgeCalls     = 5
	ForgeWindow    = 10 * time.Minute
	CheckCalls     = 10
	CheckWindow    = 10 * time.Minute
	ForgeFor       = 45 * time.Second
	ForgeCardFor   = 24 * time.Hour
	MaxForgeTitle  = 256
	MaxForgeBody   = 20000
	maxForgeWait   = 5
	maxForgeKept   = 10
	maxForgeReason = 600

	NeedContents = "Contents: Read and write"
	NeedMetadata = "Metadata: Read-only"
	NeedIssues   = "Issues: Read and write"
	NeedPulls    = "Pull requests: Read and write"
	NeedActions  = "Actions: Read-only"
	NeedStatuses = "Commit statuses: Read-only"
)

type ForgeRepo struct {
	Host  string
	Owner string
	Name  string
	Token string
}

func (r ForgeRepo) Slug() string { return r.Owner + "/" + r.Name }

type ForgeLink struct {
	Number int
	URL    string
}

type ForgeError struct {
	Status  int
	Need    string
	Message string
}

func (e *ForgeError) Error() string { return e.Message }

var ErrNoBranch = errors.New("the branch isn't on the git host")

type CheckRun struct {
	Name       string
	App        string
	Status     string
	Conclusion string
	URL        string
	Log        []string
	LogNote    string
}

type CommitStatus struct {
	Context     string
	State       string
	Description string
	URL         string
}

type CheckReport struct {
	Runs      []CheckRun
	Statuses  []CommitStatus
	State     string
	MoreRuns  bool
	RunsNote  string
	StateNote string
}

type ForgeAPI interface {
	Supports(host string) bool
	Repository(ctx context.Context, r ForgeRepo) error
	Branch(ctx context.Context, r ForgeRepo, branch string) (string, error)
	OpenPull(ctx context.Context, r ForgeRepo, head string) (ForgeLink, bool, error)
	CreateIssue(ctx context.Context, r ForgeRepo, title, body string) (ForgeLink, error)
	CreatePull(ctx context.Context, r ForgeRepo, title, body, head, base string, draft bool) (ForgeLink, bool, error)
	Checks(ctx context.Context, r ForgeRepo, commit string) (CheckReport, error)
	Comment(ctx context.Context, r ForgeRepo, number int, body string) (ForgeLink, error)
	Login(ctx context.Context, r ForgeRepo) (string, error)
	PullComments(ctx context.Context, r ForgeRepo, number int) (CommentReport, error)
	RecentIssues(ctx context.Context, r ForgeRepo) ([]IssueFound, error)
}

type ForgeTargets interface {
	ForgeTarget(name string) (url, token, main string, err error)
}

type ForgeView struct {
	Card   string
	Kind   ForgeKind
	State  string
	Title  string
	Body   string
	Head   string
	Base   string
	Host   string
	Repo   string
	Number int
	URL    string
	Reason string
	Draft  bool
	Asked  time.Time
	At     time.Time

	Matches []ForgeMatch
}

type ForgeReply struct {
	Result string
	Reason string
}

type ForgeService interface {
	Request(ctx context.Context, sid string, kind ForgeKind, title, body string) ForgeReply
	Checks(ctx context.Context, sid string) ForgeReply
	Comments(ctx context.Context, sid string) ForgeReply
	Answer(ctx context.Context, sid, card string, approve, draft bool) error
}

type ForgesConfig struct {
	API        ForgeAPI
	Repos      ForgeTargets
	Targets    func(sid string) (PushTarget, bool)
	Show       func(sid string, list []ForgeView)
	Note       func(sid, text string)
	Audit      Audit
	Base       context.Context
	Now        func() time.Time
	Logger     shared.Logger
	Version    string
	PRComments func(name string) bool
}

type forgeCard struct {
	view ForgeView
	repo ForgeRepo
	sid  string
}

type Forges struct {
	cfg    ForgesConfig
	mu     sync.Mutex
	cards  map[string][]*forgeCard
	asks   *limiter
	checks *limiter
	own    forgeOwn
	wg     sync.WaitGroup
	closed bool
}

var _ ForgeService = (*Forges)(nil)

func NewForges(cfg ForgesConfig) (*Forges, error) {
	if cfg.API == nil || cfg.Repos == nil || cfg.Targets == nil {
		return nil, errors.New("forges: need the git host API, the repositories and the sessions")
	}
	if cfg.Show == nil {
		cfg.Show = func(string, []ForgeView) {}
	}
	if cfg.Note == nil {
		cfg.Note = func(string, string) {}
	}
	if cfg.Base == nil {
		cfg.Base = context.Background()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = quiet{}
	}
	return &Forges{cfg: cfg, cards: map[string][]*forgeCard{}, asks: newLimiter(ForgeCalls, ForgeWindow), checks: newLimiter(CheckCalls, CheckWindow)}, nil
}

func (f *Forges) Wait() { f.wg.Wait() }

func (f *Forges) note(sid, text string) {
	f.cfg.Note(sid, strings.ToUpper(text[:1])+text[1:])
}

func ForgeRepoOf(raw string) (host, owner, name string, ok bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", "", "", false
	}
	h, err := hostOf(u)
	if err != nil {
		return "", "", "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 {
		return "", "", "", false
	}
	owner, name = parts[0], strings.TrimSuffix(parts[1], ".git")
	if owner == "" || name == "" || name == "." || name == ".." {
		return "", "", "", false
	}
	return h, owner, name, true
}

func (c *Repos) ForgeTarget(name string) (string, string, string, error) {
	rec, err := c.record(name)
	if err != nil {
		return "", "", "", err
	}
	host, err := HostOf(rec.URL)
	if err != nil {
		return "", "", "", err
	}
	token, _, err := c.cfg.Tokens.Token(host)
	if err != nil {
		return "", "", "", err
	}
	return rec.URL, token, rec.Branch, nil
}

func ForgeText(s string, max int, lines bool) (string, string) {
	if !utf8.ValidString(s) {
		return "", "isn't valid UTF-8"
	}
	if utf8.RuneCountInString(s) > max {
		return "", "is longer than " + strconv.Itoa(max) + " characters"
	}
	for _, r := range s {
		if lines && (r == '\n' || r == '\t' || r == '\r') {
			continue
		}
		if escaped(r) {
			return "", "has a control or invisible character (U+" + strings.ToUpper(strconv.FormatInt(int64(r), 16)) + ")"
		}
	}
	return s, ""
}

type forgeTarget struct {
	t    PushTarget
	repo ForgeRepo
	main string
}

func (f *Forges) target(sid string, need string) (forgeTarget, string) {
	t, ok := f.cfg.Targets(sid)
	if !ok {
		return forgeTarget{}, "only a session this engine started can use this tool"
	}
	raw, token, main, err := f.cfg.Repos.ForgeTarget(t.Name)
	if err != nil {
		return forgeTarget{}, "the session's repository isn't in the engine's list any more"
	}
	host, owner, name, ok := ForgeRepoOf(raw)
	if !ok || !f.cfg.API.Supports(host) {
		shown := host
		if shown == "" {
			shown, _ = HostOf(raw)
		}
		return forgeTarget{}, "this tool isn't supported for " + strings.TrimPrefix(shown, "https://") + ": Software Factory talks only to GitHub's API (github.com or a GitHub Enterprise server); tell the person what to do on the git host instead"
	}
	if token == "" {
		return forgeTarget{}, "no token is saved for " + host + ", so the engine can't call its API; ask the person to save one on the page (Projects, Git host tokens) with " + need
	}
	return forgeTarget{t: t, repo: ForgeRepo{Host: host, Owner: owner, Name: name, Token: token}, main: main}, ""
}

func (f *Forges) clean(token, s string) string {
	if token != "" {
		s = strings.ReplaceAll(s, token, "[token]")
	}
	return clip(s, maxForgeReason)
}

func forgeWhy(err error, need string) string {
	if fe, ok := errors.AsType[*ForgeError](err); ok {
		msg := fe.Message
		if fe.Need != "" {
			msg += "; the token needs " + fe.Need
		} else if fe.Status == 403 || fe.Status == 404 {
			msg += "; the token may need " + need
		}
		return msg
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "the git host didn't answer in time"
	}
	return "the git host couldn't be reached: " + err.Error()
}

func kindWord(k ForgeKind) string {
	if k == ForgePull {
		return "pull request"
	}
	return "issue"
}

func (f *Forges) Request(ctx context.Context, sid string, kind ForgeKind, title, body string) ForgeReply {
	now := f.cfg.Now()
	tool := "factory_issue"
	need := NeedIssues
	if kind == ForgePull {
		tool, need = "factory_pull_request", NeedPulls
	}
	refuse := func(why string) ForgeReply { return ForgeReply{Result: ForgeRefused, Reason: why} }
	f.mu.Lock()
	allowed, closed := f.asks.allow(sid, now), f.closed
	f.mu.Unlock()
	if closed {
		return refuse(forgeStopping)
	}
	if !allowed {
		return refuse("factory_issue and factory_pull_request were called more than " + strconv.Itoa(ForgeCalls) + " times in " + strconv.Itoa(int(ForgeWindow/time.Minute)) + " minutes for this session; end your turn and let the person act on the page")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return refuse("the title is empty")
	}
	if _, why := ForgeText(title, MaxForgeTitle, false); why != "" {
		return refuse("the title " + why + "; give one line of plain text")
	}
	if _, why := ForgeText(body, MaxForgeBody, true); why != "" {
		return refuse("the body " + why)
	}
	ft, why := f.target(sid, need)
	if why != "" {
		return refuse(why)
	}
	ctx, cancel := context.WithTimeout(ctx, ForgeFor)
	defer cancel()
	v := ForgeView{Kind: kind, State: ForgeWaiting, Title: title, Body: body, Host: ft.repo.Host, Repo: ft.repo.Slug(), Asked: now, At: now}
	if kind == ForgePull {
		if ft.main == "" {
			return refuse("the engine doesn't know the repository's main branch yet")
		}
		v.Head, v.Base = ft.t.Branch, ft.main
		if _, err := f.cfg.API.Branch(ctx, ft.repo, ft.t.Branch); err != nil {
			if errors.Is(err, ErrNoBranch) {
				return refuse("the branch " + ft.t.Branch + " isn't on " + ft.repo.Host + " yet; call factory_push first, and once it says pushed, ask for the pull request again")
			}
			return refuse(f.clean(ft.repo.Token, "the engine couldn't read the branch on the git host: "+forgeWhy(err, NeedContents)))
		}
		link, open, err := f.cfg.API.OpenPull(ctx, ft.repo, ft.t.Branch)
		if err != nil {
			return refuse(f.clean(ft.repo.Token, "the engine couldn't list the open pull requests: "+forgeWhy(err, NeedPulls)))
		}
		if open {
			return ForgeReply{Result: ForgeExists, Reason: "pull request #" + strconv.Itoa(link.Number) + " for " + ft.t.Branch + " is already open: " + link.URL + "; pushing the branch again updates it, so don't ask for another"}
		}
	} else if err := f.cfg.API.Repository(ctx, ft.repo); err != nil {
		return refuse(f.clean(ft.repo.Token, "the engine couldn't read the repository on the git host: "+forgeWhy(err, NeedMetadata)))
	}
	f.mu.Lock()
	list := f.cards[sid]
	waiting := 0
	for _, c := range list {
		if c.view.State != ForgeWaiting && c.view.State != ForgeSending {
			continue
		}
		if c.view.Kind == kind && (kind == ForgePull || c.view.Title == title && c.view.Body == body) {
			f.mu.Unlock()
			return ForgeReply{Result: ForgeHeld, Reason: "the same " + kindWord(kind) + " already waits for the person on Software Factory's page; end your turn and don't call " + tool + " again for it"}
		}
		waiting++
	}
	if waiting >= maxForgeWait {
		f.mu.Unlock()
		return refuse(strconv.Itoa(maxForgeWait) + " issues and pull requests already wait for the person on the page; end your turn")
	}
	v.Card = randomHex(16)
	f.cards[sid] = append(list, &forgeCard{view: v, repo: ft.repo, sid: sid})
	f.trim(sid)
	f.mu.Unlock()
	f.show(sid)
	f.audit(sid, ft.t, v, ByAgent, ForgeHeld, "asked to open "+kindWord(kind)+": "+title)
	f.cfg.Logger.Info("forge", "event", "held", "kind", string(kind))
	return ForgeReply{Result: ForgeHeld, Reason: "the " + kindWord(kind) + " waits for the person on Software Factory's page, who approves or denies it; only then does the engine open it on " + ft.repo.Host + ". End your turn and don't call " + tool + " again for it: the result (its number and link, or why not) reaches you with your next message"}
}

func (f *Forges) trim(sid string) {
	list := f.cards[sid]
	now := f.cfg.Now()
	kept := list[:0]
	for _, c := range list {
		if c.view.State == ForgeWaiting && now.Sub(c.view.Asked) >= ForgeCardFor {
			c.view.State, c.view.Reason, c.view.Body = ForgeFailed, "the card expired after 24 hours without an answer", ""
			f.expired(c)
		}
		kept = append(kept, c)
	}
	for _, comments := range []bool{false, true} {
		group := func(c *forgeCard) bool { return (c.view.Kind == ForgeComment) == comments }
		for countWhere(kept, group) > maxForgeKept {
			i := slices.IndexFunc(kept, func(c *forgeCard) bool { return group(c) && finishedCard(c) })
			if i < 0 {
				break
			}
			kept = slices.Delete(kept, i, i+1)
		}
	}
	f.cards[sid] = kept
}

func (f *Forges) show(sid string) {
	f.mu.Lock()
	list := f.cards[sid]
	out := make([]ForgeView, 0, len(list))
	for _, c := range list {
		out = append(out, c.view)
	}
	f.mu.Unlock()
	f.cfg.Show(sid, out)
}

func (f *Forges) audit(sid string, t PushTarget, v ForgeView, by, result, reason string) {
	if f.cfg.Audit == nil {
		return
	}
	e := AuditEntry{Time: f.cfg.Now(), Session: sid, Repository: t.Name, Branch: t.Branch, Remote: v.Host + "/" + v.Repo, By: by, Result: result, Reason: clip(reason, maxForgeReason), Kind: string(v.Kind), Request: v.Card}
	if err := f.cfg.Audit.Append(e); err != nil {
		f.cfg.Logger.Warn("forge", "event", "audit-not-written")
	}
}

func (f *Forges) Answer(_ context.Context, sid, card string, approve, draft bool) error {
	f.mu.Lock()
	f.trim(sid)
	var c *forgeCard
	for _, x := range f.cards[sid] {
		if x.view.Card == card {
			c = x
		}
	}
	if c == nil || c.view.State != ForgeWaiting {
		f.mu.Unlock()
		f.show(sid)
		return explained(conflict("card"), "That card isn't waiting any more.")
	}
	if f.closed {
		f.mu.Unlock()
		return explained(conflict("stopping"), stoppingMessage)
	}
	if draft && c.view.Kind != ForgePull {
		f.mu.Unlock()
		return explained(invalid("draft"), "Only a pull request can open as a draft.")
	}
	t, _ := f.cfg.Targets(sid)
	word := kindWord(c.view.Kind)
	if !approve {
		c.view.State, c.view.Reason, c.view.At, c.view.Body = ForgeDenied, "you denied it", f.cfg.Now(), ""
		v := c.view
		f.mu.Unlock()
		f.show(sid)
		f.audit(sid, t, v, ByPage, ForgeDenied, "denied opening "+word+": "+v.Title)
		f.note(sid, "the person denied opening the "+word+" \""+clip(v.Title, 200)+"\" on the page, so the engine didn't open it. Don't ask for it again unless they say so.")
		f.cfg.Logger.Info("forge", "event", "denied", "kind", string(v.Kind))
		return nil
	}
	c.view.State, c.view.At, c.view.Draft = ForgeSending, f.cfg.Now(), approve && draft
	f.wg.Go(func() {
		ctx, cancel := context.WithTimeout(f.cfg.Base, ForgeFor)
		defer cancel()
		f.send(ctx, t, c)
	})
	f.mu.Unlock()
	f.show(sid)
	return nil
}

func (f *Forges) send(ctx context.Context, t PushTarget, c *forgeCard) {
	f.mu.Lock()
	v, repo := c.view, c.repo
	f.mu.Unlock()
	word := kindWord(v.Kind)
	var link ForgeLink
	var err error
	state := ForgeOpened
	asked, drafted := v.Draft, false
	if v.Kind == ForgePull {
		var open bool
		if link, open, err = f.cfg.API.OpenPull(ctx, repo, v.Head); err == nil && open {
			state = ForgeExists
		} else if err == nil {
			if err = f.sending(t, v); err == nil {
				link, drafted, err = f.cfg.API.CreatePull(ctx, repo, v.Title, v.Body, v.Head, v.Base, asked)
			}
		}
	} else if err = f.sending(t, v); err == nil {
		link, err = f.cfg.API.CreateIssue(ctx, repo, v.Title, v.Body)
	}
	if forgeUnclear(err) {
		if link, state, err = f.recheck(c, t, v, err); errors.Is(err, errSettled) {
			return
		}
		asked = asked && err != nil
	}
	need := NeedIssues
	if v.Kind == ForgePull {
		need = NeedPulls
	}
	f.mu.Lock()
	c.view.At, c.view.Body, c.view.Draft = f.cfg.Now(), "", drafted
	if err != nil {
		c.view.State, c.view.Reason = ForgeFailed, f.clean(repo.Token, forgeWhy(err, need))
	} else {
		c.view.State, c.view.Number, c.view.URL = state, link.Number, link.URL
		if state == ForgeOpened && asked && !drafted {
			c.view.Reason = noDrafts
		}
	}
	v = c.view
	f.mu.Unlock()
	f.show(c.sid)
	switch {
	case err != nil:
		f.audit(c.sid, t, v, ByPage, ForgeFailed, "couldn't open "+word+": "+v.Reason)
		f.note(c.sid, "the person approved the "+word+" \""+clip(v.Title, 200)+"\", but the engine couldn't open it: "+v.Reason+".")
		f.cfg.Logger.Warn("forge", "event", "failed", "kind", string(v.Kind))
	case state == ForgeExists:
		f.audit(c.sid, t, v, ByPage, ForgeExists, "pull request #"+strconv.Itoa(v.Number)+" was already open: "+v.URL)
		f.note(c.sid, "pull request #"+strconv.Itoa(v.Number)+" for "+v.Head+" was already open, so the engine didn't open another: "+v.URL)
		f.cfg.Logger.Info("forge", "event", "exists", "kind", string(v.Kind))
	default:
		f.audit(c.sid, t, v, ByPage, ForgeOpened, "opened "+word+" #"+strconv.Itoa(v.Number)+openedAs(v, asked)+": "+v.URL)
		f.note(c.sid, "the person approved, and the engine opened "+word+" #"+strconv.Itoa(v.Number)+" \""+clip(v.Title, 200)+"\""+openedAs(v, asked)+": "+v.URL)
		f.cfg.Logger.Info("forge", "event", "opened", "kind", string(v.Kind), "draft", v.Draft)
		if v.Kind == ForgePull {
			f.afterOpened(c.sid, repo, v.Head, v.Number)
		}
	}
}

func (f *Forges) Checks(ctx context.Context, sid string) ForgeReply {
	refuse := func(why string) ForgeReply { return ForgeReply{Result: ForgeRefused, Reason: why} }
	f.mu.Lock()
	allowed := f.checks.allow(sid, f.cfg.Now())
	f.mu.Unlock()
	if !allowed {
		return refuse("factory_checks was called more than " + strconv.Itoa(CheckCalls) + " times in " + strconv.Itoa(int(CheckWindow/time.Minute)) + " minutes for this session; wait a few minutes before asking again")
	}
	ft, why := f.target(sid, NeedActions+" and "+NeedStatuses)
	if why != "" {
		return refuse(why)
	}
	ctx, cancel := context.WithTimeout(ctx, ForgeFor)
	defer cancel()
	head, err := f.cfg.API.Branch(ctx, ft.repo, ft.t.Branch)
	if err != nil {
		if errors.Is(err, ErrNoBranch) {
			return refuse("the branch " + ft.t.Branch + " isn't on " + ft.repo.Host + " yet, so it has no CI results; call factory_push first")
		}
		return refuse(f.clean(ft.repo.Token, "the engine couldn't read the branch on the git host: "+forgeWhy(err, NeedContents)))
	}
	report, err := f.cfg.API.Checks(ctx, ft.repo, head)
	if err != nil {
		return refuse(f.clean(ft.repo.Token, "the engine couldn't read the CI results: "+forgeWhy(err, NeedActions)))
	}
	f.cfg.Logger.Info("forge", "event", "checks", "runs", len(report.Runs), "statuses", len(report.Statuses))
	return ForgeReply{Result: ForgeReady, Reason: strings.ReplaceAll(CheckText(ft.t.Branch, head, ft.repo, report), ft.repo.Token, "[token]")}
}
