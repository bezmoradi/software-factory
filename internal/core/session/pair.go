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
	At      time.Time
	History bool
}

type PairStore interface {
	Load() ([]Pair, error)
	Save([]Pair) error
	Dropped(n int)
}

type PairStart struct {
	Path      string
	Name      string
	Type      string
	Message   string
	Agents    []PairAgent
	Double    bool
	Bench     int
	BenchHash string
	BenchBase string
	Consent   Consent
	Box       string
}

type PairStarted struct {
	Started
	Pair string
	Next bool
}

type PairsPage interface {
	MergedPulls(ctx context.Context, path string, page int) (MergedList, error)
	ReadMerged(ctx context.Context, path string, number int) (MergedView, error)
	Start(ctx context.Context, q PairStart) (PairStarted, error)
	Next(ctx context.Context, id string, consent Consent, box string) (Started, error)
	View(id string) (Pair, error)
	MergedDiff(id string) (ChangeView, error)
	Keep(id, sid string) error
}

type PairsConfig struct {
	Dispatcher *Dispatcher
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
	return &Pairs{cfg: cfg, send: newFirstSender(cfg.Base, cfg.Tell, cfg.Now, cfg.Logger), busy: map[string]bool{}, reads: newLimiter(MergedCalls, 10*time.Minute)}, nil
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
		return "", explained(invalid("message"), "Write the first message both sessions get: at most 20,000 characters, not starting with !.")
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
		return Repository{}, explained(conflict("no agent-host"), "This engine has no agent-host to start sessions in.")
	}
	repos, err := d.cfg.Repositories.List()
	if err != nil {
		return Repository{}, explained(conflict("workspace"), "The engine couldn't read the workspace's repositories.")
	}
	i := slices.IndexFunc(repos, func(r Repository) bool { return r.Path == path })
	if i < 0 {
		return Repository{}, explained(notFound("project"), "That project is no longer in the list.")
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
		return explained(limited("wait"), "Wait a few seconds between starts.")
	case <-time.After(wait):
		return nil
	}
}

func checkAgents(list []PairAgent, bench bool, double bool) error {
	switch {
	case len(list) < 1 || len(list) > MaxPairAgents:
		return explained(invalid("agents"), "Start one agent on a merged pull request, or two agents on one task.")
	case len(list) == 1 && !bench:
		return explained(invalid("agents"), "Start with both needs two agents, or two models of one agent.")
	case len(list) == 2 && !double:
		return explained(conflict("double"), DoubleUsage+" Confirm it to start them.")
	}
	return nil
}

func (p *Pairs) Start(ctx context.Context, q PairStart) (PairStarted, error) {
	if err := checkAgents(q.Agents, q.Bench != 0, q.Double); err != nil {
		return PairStarted{}, err
	}
	d := p.cfg.Dispatcher
	var pinned *startPoint
	var bench *Benchmark
	text := ""
	if q.Bench == 0 {
		t, err := firstMessage(q.Message)
		if err != nil {
			return PairStarted{}, err
		}
		text = t
	} else {
		if strings.TrimSpace(q.Message) != "" {
			return PairStarted{}, explained(invalid("message"), "A benchmark's first message is the merged pull request's description.")
		}
		at, b, t, err := p.benchPoint(ctx, q)
		if err != nil {
			return PairStarted{}, err
		}
		pinned, bench, text = &at, &b, t
	}
	id := randomHex(8)
	a := q.Agents[0]
	started, err := d.Dispatch(ctx, Dispatch{Path: q.Path, Agent: a.Agent, Name: d.memberName(q.Name, a), Model: a.Model, Effort: a.Effort, Type: q.Type, Consent: q.Consent,
		Box: q.Box, pinned: pinned, group: id, groupKind: GroupPair})
	if err != nil || started.SID == "" {
		return PairStarted{Started: started}, err
	}
	at, repo, _ := d.startedAt(started.SID)
	pr := &Pair{ID: id, Repo: repo, Path: q.Path, Commit: at.commit, Origin: at.origin, Name: q.Name, Type: q.Type, Message: text, Bench: bench, At: p.cfg.Now()}
	for i, x := range q.Agents {
		m := PairMember{PairAgent: x}
		if i == 0 {
			m.SID = started.SID
		}
		pr.Members = append(pr.Members, m)
	}
	p.mu.Lock()
	p.pairs = append(p.pairs, pr)
	p.saveLocked()
	p.mu.Unlock()
	p.send.send(started.SID, text, "the pair's first message")
	p.cfg.Logger.Info("pairs", "event", "started", "agents", len(q.Agents), "benchmark", bench != nil)
	return PairStarted{Started: started, Pair: id, Next: len(q.Agents) == 2}, nil
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
		return Started{}, explained(notFound("pair"), "That pair isn't one this engine started.")
	case len(pr.Members) < 2:
		p.mu.Unlock()
		return Started{}, explained(conflict("one"), "This benchmark runs one agent.")
	case pr.History:
		p.mu.Unlock()
		return Started{}, explained(conflict("history only"), pairHistoryMessage)
	case pr.Members[1].SID != "":
		p.mu.Unlock()
		return Started{}, explained(conflict("started"), "Both sessions of this pair started already.")
	case p.busy[id]:
		p.mu.Unlock()
		return Started{}, explained(limited("busy"), "The second session is starting.")
	}
	p.busy[id] = true
	a, path, name, typ, text := pr.Members[1].PairAgent, pr.Path, pr.Name, pr.Type, pr.Message
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
	started, err := d.Dispatch(ctx, Dispatch{Path: path, Agent: a.Agent, Name: d.memberName(name, a), Model: a.Model, Effort: a.Effort, Type: typ, Consent: consent,
		Box: box, pinned: &at, group: id, groupKind: GroupPair})
	if err != nil || started.SID == "" {
		return started, err
	}
	p.mu.Lock()
	if pr := p.find(id); pr != nil && len(pr.Members) == 2 {
		pr.Members[1].SID = started.SID
	}
	p.saveLocked()
	p.mu.Unlock()
	p.send.send(started.SID, text, "the pair's first message")
	p.cfg.Logger.Info("pairs", "event", "second-started")
	return started, nil
}

func (p *Pairs) View(id string) (Pair, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pr := p.find(id)
	if pr == nil {
		return Pair{}, explained(notFound("pair"), "That pair isn't one this engine started.")
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
	p.mu.Lock()
	defer p.mu.Unlock()
	pr := p.find(id)
	if pr == nil {
		return explained(notFound("pair"), "That pair isn't one this engine started.")
	}
	if pr.History {
		return explained(conflict("history only"), pairHistoryMessage)
	}
	if sid != "" && !slices.ContainsFunc(pr.Members, func(m PairMember) bool { return m.SID == sid }) {
		return explained(invalid("sid"), "That session isn't one of this pair's.")
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
