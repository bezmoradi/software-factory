package session

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	GroupPair      = "pair"
	GroupGoal      = "goal"
	MaxPairs       = 200
	MaxPairAgents  = 2
	firstSendFor   = 10 * time.Minute
	firstSendEvery = 3 * time.Second
	DoubleUsage    = "Starting both runs two sessions on the same task, which doubles usage, even on a subscription."
)

const pairHistoryMessage = "This pair came from an export of another engine, so it is history only: its sessions' worktrees stayed on that engine. Start a new pair instead."

type PairAgent struct {
	Agent  string
	Model  string
	Effort string
}

type PairMember struct {
	PairAgent
	SID string
}

type Benchmark struct {
	Number int
	Title  string
	URL    string
	Base   string
	Head   string
	Merge  string
}

type Pair struct {
	ID      string
	Repo    string
	Path    string
	Commit  string
	Origin  string
	Name    string
	Type    string
	Message string
	Members []PairMember
	Kept    string
	Bench   *Benchmark
	Issue   int
	At      time.Time
	History bool
}

type PairStore interface {
	Load() ([]Pair, error)
	Save([]Pair) error
	Dropped(n int)
}

type PairStart struct {
	Path        string
	Name        string
	Type        string
	Message     string
	Agent       PairAgent
	SecondModel string
	Double      bool
	Bench       int
	BenchHash   string
	BenchBase   string
	From        StartFrom
	Issue       int
	IssueHash   string
	Caps        *Caps
	Consent     Consent
	Box         string
}

type PairStarted struct {
	Started
	Pair      string
	Next      bool
	Second    Started
	SecondErr error
}

type PairsPage interface {
	MergedPulls(ctx context.Context, path string, page int) (MergedList, error)
	ReadMerged(ctx context.Context, path string, number int) (MergedView, error)
	Start(ctx context.Context, q PairStart) (PairStarted, error)
	Next(ctx context.Context, id string, consent Consent, box string) (Started, error)
	View(id string) (Pair, error)
	MergedDiff(ctx context.Context, id string) (ChangeView, error)
	Keep(id, sid string) error
}

type PairsConfig struct {
	Dispatcher *Dispatcher
	Issues     *IssueStarts
	Store      PairStore
	Tell       func(sid, text string) error
	Changes    ChangeReader
	History    History
	Base       context.Context
	Now        func() time.Time
	Logger     shared.Logger
}

type Pairs struct {
	cfg   PairsConfig
	send  *firstSender
	mu    sync.Mutex
	pairs []*Pair
	busy  map[string]bool
	caps  map[string]Caps
	reads *limiter
}

var _ PairsPage = (*Pairs)(nil)

func NewPairs(cfg PairsConfig) (*Pairs, error) {
	if cfg.Dispatcher == nil {
		return nil, errors.New("pairs: need the dispatcher")
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
	return &Pairs{cfg: cfg, send: newFirstSender(cfg.Base, cfg.Tell, cfg.Now, cfg.Logger), busy: map[string]bool{}, caps: map[string]Caps{}, reads: newLimiter(MergedCalls, 10*time.Minute)}, nil
}

func (p *Pairs) Close() { p.send.close() }

func (p *Pairs) Wait() { p.send.wait() }

func (p *Pairs) Restore() {
	if p.cfg.Store == nil {
		return
	}
	list, err := p.cfg.Store.Load()
	if err != nil {
		p.cfg.Logger.Warn("pairs", "event", "not-read")
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range list {
		if len(p.pairs) >= MaxPairs {
			break
		}
		x := list[i]
		p.pairs = append(p.pairs, &x)
	}
}

func (p *Pairs) saveLocked() {
	if p.cfg.Store == nil {
		return
	}
	if n := len(p.pairs) - MaxPairs; n > 0 {
		p.pairs = p.pairs[n:]
		p.cfg.Store.Dropped(n)
	}
	out := make([]Pair, 0, len(p.pairs))
	for _, x := range p.pairs {
		c := *x
		c.Members = slices.Clone(x.Members)
		out = append(out, c)
	}
	if err := p.cfg.Store.Save(out); err != nil {
		p.cfg.Logger.Warn("pairs", "event", "not-saved")
	}
}

func samePair(a, b *admitted) bool {
	if a == nil || b == nil || a.Lineage == nil || b.Lineage == nil {
		return false
	}
	return a.Lineage.GroupKind == GroupPair && b.Lineage.GroupKind == GroupPair && a.Lineage.Group != "" && a.Lineage.Group == b.Lineage.Group
}

func firstMessage(text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" || utf16Len(text) > MaxText || !utf8.ValidString(text) || strings.HasPrefix(text, "!") {
		return "", invalid("message", "Needs up to 20,000 characters, not starting with !.")
	}
	return text, nil
}

func (d *Dispatcher) memberName(name string, a PairAgent) string {
	label := a.Agent
	if spec, ok := findAgent(d.cfg.Agents, a.Agent); ok {
		label = spec.Name
	}
	if a.Model != "" {
		label += " " + a.Model
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return clipUnits(label, maxName)
	}
	return clipUnits(name, maxName-utf16Len(" · "+label)) + " · " + label
}

func clipUnits(s string, n int) string {
	r := []rune(s)
	for len(r) > 0 && utf16Len(string(r)) > n {
		r = r[:len(r)-1]
	}
	return string(r)
}

func (d *Dispatcher) projectAt(path string) (Repository, error) {
	if d.cfg.Repositories == nil {
		return Repository{}, conflict("no agent-host", "This engine has no agent-host to start sessions in.")
	}
	repos, err := d.cfg.Repositories.List()
	if err != nil {
		return Repository{}, conflict("workspace", "The engine couldn't read the workspace's repositories.")
	}
	i := slices.IndexFunc(repos, func(r Repository) bool { return r.Path == path })
	if i < 0 {
		return Repository{}, notFound("project", "That project is no longer in the list.")
	}
	return repos[i], nil
}

func (d *Dispatcher) startedAt(sid string) (startPoint, string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r := d.runs[sid]
	if r == nil {
		return startPoint{}, "", false
	}
	return startPoint{commit: r.base, origin: r.origin}, r.repo, true
}

func (d *Dispatcher) waitStartGap(ctx context.Context) error {
	d.mu.Lock()
	last := d.started
	d.mu.Unlock()
	if last.IsZero() {
		return nil
	}
	wait := StartGap - d.reg.now().Sub(last) + 200*time.Millisecond
	if wait <= 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return limited("wait", "Wait a few seconds between starts.")
	case <-time.After(wait):
		return nil
	}
}

func (d *Dispatcher) pairAgents(q PairStart) ([]PairAgent, error) {
	list := []PairAgent{q.Agent}
	switch {
	case q.Double:
		list = append(list, PairAgent{Agent: q.Agent.Agent, Model: q.SecondModel})
	case q.SecondModel != "":
		return nil, conflict("double", DoubleUsage+" Confirm it to start them.")
	case q.Bench == 0:
		return nil, invalid("agents", "Start with both needs two sessions of one agent.")
	}
	if spec, ok := findAgent(d.cfg.Agents, q.Agent.Agent); ok && len(list) == 2 {
		if err := checkChoice(spec, q.SecondModel, ""); err != nil {
			return nil, onField("secondModel", err)
		}
	}
	return list, nil
}

func pairPoint(q PairStart) error {
	switch {
	case q.Bench != 0 && q.From.chosen() > 0:
		return onField("from", invalid("from", "Set it back to main for a benchmark."))
	case q.Bench != 0 && q.Issue != 0:
		return onField("issue", invalid("issue", "Clear it for a benchmark."))
	case q.Issue != 0 && strings.TrimSpace(q.Message) != "":
		return onField("message", invalid("message", "Clear it, or clear the issue."))
	case q.Bench != 0 && strings.TrimSpace(q.Message) != "":
		return onField("message", invalid("message", "Clear it for a benchmark."))
	}
	return nil
}

func (p *Pairs) firstText(ctx context.Context, q PairStart) (*startPoint, *Benchmark, string, error) {
	switch {
	case q.Bench != 0:
		at, b, t, err := p.benchPoint(ctx, q)
		if err != nil {
			return nil, nil, "", err
		}
		return &at, &b, t, nil
	case q.Issue != 0:
		if p.cfg.Issues == nil {
			return nil, nil, "", onField("issue", conflict("no issues", "This engine can't start a session from an issue."))
		}
		t, err := p.cfg.Issues.startText(ctx, q.Path, q.Issue, q.IssueHash)
		return nil, nil, t, err
	}
	t, err := firstMessage(q.Message)
	return nil, nil, t, onField("message", err)
}

func (p *Pairs) greet(sid string, issue int, text string) {
	if issue != 0 && p.cfg.Issues != nil {
		p.cfg.Issues.begin(sid, issue, text)
		return
	}
	p.send.send(sid, text, "the pair's first message")
}

func (p *Pairs) Start(ctx context.Context, q PairStart) (PairStarted, error) {
	d := p.cfg.Dispatcher
	agents, err := d.pairAgents(q)
	if err != nil {
		return PairStarted{}, err
	}
	if err := pairPoint(q); err != nil {
		return PairStarted{}, err
	}
	if q.Bench != 0 && (!ValidIssueHash(q.BenchHash) || !sha1Hex.MatchString(q.BenchBase)) {
		return PairStarted{}, benchUnread()
	}
	if q.Issue != 0 && !ValidIssueHash(q.IssueHash) {
		return PairStarted{}, onField("issue", invalid("issue", "Read the issue again before starting from it."))
	}
	pinned, bench, text, err := p.firstText(ctx, q)
	if err != nil {
		return PairStarted{}, onStartField(err)
	}
	from := q.From
	if pinned != nil {
		from = StartFrom{}
	}
	id := randomHex(8)
	a := agents[0]
	started, err := d.Dispatch(ctx, Dispatch{Path: q.Path, Agent: a.Agent, Name: d.memberName(q.Name, a), Model: a.Model, Effort: a.Effort, Type: q.Type, Consent: q.Consent,
		From: from, Box: q.Box, Caps: q.Caps, pinned: pinned, group: id, groupKind: GroupPair})
	if err != nil || started.SID == "" {
		return PairStarted{Started: started}, err
	}
	at, repo, _ := d.startedAt(started.SID)
	pr := &Pair{ID: id, Repo: repo, Path: q.Path, Commit: at.commit, Origin: at.origin, Name: q.Name, Type: q.Type, Message: text, Bench: bench, Issue: q.Issue, At: p.cfg.Now()}
	for i, x := range agents {
		m := PairMember{PairAgent: x}
		if i == 0 {
			m.SID = started.SID
		}
		pr.Members = append(pr.Members, m)
	}
	p.mu.Lock()
	p.pairs = append(p.pairs, pr)
	if q.Caps != nil && len(agents) == 2 {
		p.caps[id] = *q.Caps
	}
	p.saveLocked()
	p.mu.Unlock()
	p.greet(started.SID, q.Issue, text)
	shared.For(ctx, p.cfg.Logger).Info("pairs", "event", "started", "agents", len(agents), "benchmark", bench != nil, "issue", q.Issue != 0)
	out := PairStarted{Started: started, Pair: id}
	if len(agents) == 2 {
		out.Second, out.SecondErr = p.Next(ctx, id, Consent{}, "")
		out.Next = out.SecondErr != nil || out.Second.SID == ""
	}
	return out, nil
}

func benchUnread() error {
	return onField("benchmark", invalid("benchmark", "Read the pull request again before starting from it."))
}

func (p *Pairs) find(id string) *Pair {
	for _, x := range p.pairs {
		if x.ID == id {
			return x
		}
	}
	return nil
}

func (p *Pairs) Next(ctx context.Context, id string, consent Consent, box string) (Started, error) {
	p.mu.Lock()
	pr := p.find(id)
	switch {
	case pr == nil:
		p.mu.Unlock()
		return Started{}, notFound("pair", "That pair isn't one this engine started.")
	case len(pr.Members) < 2:
		p.mu.Unlock()
		return Started{}, conflict("one", "This benchmark runs one agent.")
	case pr.History:
		p.mu.Unlock()
		return Started{}, conflict("history only", pairHistoryMessage)
	case pr.Members[1].SID != "":
		p.mu.Unlock()
		return Started{}, conflict("started", "Both sessions of this pair started already.")
	case p.busy[id]:
		p.mu.Unlock()
		return Started{}, limited("busy", "The second session is starting.")
	}
	p.busy[id] = true
	a, path, name, typ, text, issue := pr.Members[1].PairAgent, pr.Path, pr.Name, pr.Type, pr.Message, pr.Issue
	at := startPoint{commit: pr.Commit, origin: pr.Origin}
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.busy, id)
		p.mu.Unlock()
	}()
	d := p.cfg.Dispatcher
	if err := d.waitStartGap(ctx); err != nil {
		return Started{}, err
	}
	p.mu.Lock()
	var caps *Caps
	if c, ok := p.caps[id]; ok {
		caps = &c
	}
	p.mu.Unlock()
	started, err := d.Dispatch(ctx, Dispatch{Path: path, Agent: a.Agent, Name: d.memberName(name, a), Model: a.Model, Effort: a.Effort, Type: typ, Consent: consent,
		Box: box, Caps: caps, pinned: &at, group: id, groupKind: GroupPair, asked: "pair:" + id})
	if err != nil || started.SID == "" {
		return started, err
	}
	p.mu.Lock()
	if pr := p.find(id); pr != nil && len(pr.Members) == 2 {
		pr.Members[1].SID = started.SID
	}
	delete(p.caps, id)
	p.saveLocked()
	p.mu.Unlock()
	p.greet(started.SID, issue, text)
	shared.For(ctx, p.cfg.Logger).Info("pairs", "event", "second-started")
	return started, nil
}

func (p *Pairs) View(id string) (Pair, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pr := p.find(id)
	if pr == nil {
		return Pair{}, notFound("pair", "That pair isn't one this engine started.")
	}
	out := *pr
	out.Members = slices.Clone(pr.Members)
	if pr.Bench != nil {
		b := *pr.Bench
		out.Bench = &b
	}
	return out, nil
}

func (p *Pairs) Keep(id, sid string) error {
	if err := heldBranch(p.cfg.Dispatcher.Doubled, sid); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	pr := p.find(id)
	if pr == nil {
		return notFound("pair", "That pair isn't one this engine started.")
	}
	if pr.History {
		return conflict("history only", pairHistoryMessage)
	}
	if sid != "" && !slices.ContainsFunc(pr.Members, func(m PairMember) bool { return m.SID == sid }) {
		return invalid("sid", "That session isn't one of this pair's.")
	}
	pr.Kept = sid
	p.saveLocked()
	p.cfg.Logger.Info("pairs", "event", "kept", "cleared", sid == "")
	return nil
}

type firstSender struct {
	base   context.Context
	tell   func(sid, text string) error
	now    func() time.Time
	logger shared.Logger
	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
}

func newFirstSender(base context.Context, tell func(sid, text string) error, now func() time.Time, logger shared.Logger) *firstSender {
	return &firstSender{base: base, tell: tell, now: now, logger: logger}
}

func (f *firstSender) close() {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
}

func (f *firstSender) wait() { f.wg.Wait() }

func (f *firstSender) send(sid, text, what string) {
	f.mu.Lock()
	if f.closed || f.tell == nil || text == "" {
		f.mu.Unlock()
		return
	}
	f.mu.Unlock()
	f.wg.Go(func() {
		until := f.now().Add(firstSendFor)
		t := time.NewTicker(firstSendEvery)
		defer t.Stop()
		for {
			select {
			case <-f.base.Done():
				return
			case <-t.C:
			}
			f.mu.Lock()
			closed := f.closed
			f.mu.Unlock()
			if closed {
				return
			}
			err := f.tell(sid, text)
			if err == nil {
				f.logger.Info("first message", "event", "sent", "what", what)
				return
			}
			if e, ok := errors.AsType[*Error](err); ok && e.Kind != Conflict || !f.now().Before(until) {
				f.logger.Warn("first message", "event", "not-sent", "what", what)
				return
			}
		}
	})
}
