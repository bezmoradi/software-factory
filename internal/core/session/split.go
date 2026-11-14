package session

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	MaxSplitTasks   = 8
	MaxGoalRunning  = 3
	MaxSplitTitle   = 120
	MaxSplitMessage = 8000
	MaxSplitCards   = 100
	maxGoalWaiting  = 24
	SplitCalls      = 5
	SplitWindow     = 10 * time.Minute

	SplitWaiting   = "waiting"
	SplitQueued    = "queued"
	SplitStarting  = "starting"
	SplitStarted   = "started"
	SplitDismissed = "dismissed"

	SplitHeld    = "held"
	SplitRefused = "refused"
)

type SplitTask struct {
	Title   string
	Message string
}

type SplitCard struct {
	Card    string
	Goal    string
	Title   string
	Message string
	State   string
	SID     string
	Edited  bool
	Asked   time.Time
	At      time.Time
}

type SplitView struct {
	SplitCard
	SecretAt string
}

type SplitStore interface {
	Load() ([]SplitCard, error)
	Save([]SplitCard) error
	Dropped(n int)
}

type SplitsPage interface {
	Edit(sid, card, title, message string) error
	Dismiss(sid, card string) error
	Start(ctx context.Context, sid, card string, consent Consent) (Started, error)
}

type SplitsConfig struct {
	Dispatcher *Dispatcher
	Store      SplitStore
	Show       func(sid string, list []SplitView)
	Tell       func(sid, text string) error
	Base       context.Context
	Now        func() time.Time
	Logger     shared.Logger
}

type Splits struct {
	cfg      SplitsConfig
	send     *firstSender
	mu       sync.Mutex
	cards    []*SplitCard
	asks     *limiter
	starting map[string]bool
	closed   bool
}

var _ SplitsPage = (*Splits)(nil)

func NewSplits(cfg SplitsConfig) (*Splits, error) {
	if cfg.Dispatcher == nil || cfg.Show == nil {
		return nil, errors.New("splits: need the dispatcher and a way to show cards")
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
	return &Splits{cfg: cfg, send: newFirstSender(cfg.Base, cfg.Tell, cfg.Now, cfg.Logger), asks: newLimiter(SplitCalls, SplitWindow), starting: map[string]bool{}}, nil
}

func (s *Splits) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.send.close()
}

func (s *Splits) Wait() { s.send.wait() }

func splitsWaiting(list []SplitView) int {
	n := 0
	for _, v := range list {
		if v.State == SplitWaiting {
			n++
		}
	}
	return n
}

func (r *Registry) SetSplits(sid string, list []SplitView) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.admitted[sid]
	if a == nil {
		return
	}
	a.splits = slices.Clone(list)
	r.announce(r.now())
}

func (s *Splits) Restore() {
	if s.cfg.Store == nil {
		return
	}
	list, err := s.cfg.Store.Load()
	if err != nil {
		s.cfg.Logger.Warn("splits", "event", "not-read")
		return
	}
	goals := map[string]bool{}
	s.mu.Lock()
	for i := range list {
		if len(s.cards) >= MaxSplitCards {
			break
		}
		c := list[i]
		if c.State == SplitStarting {
			c.State = SplitWaiting
		}
		s.cards = append(s.cards, &c)
		goals[c.Goal] = true
	}
	s.mu.Unlock()
	for g := range goals {
		s.show(g)
	}
}

func (s *Splits) show(goal string) {
	s.mu.Lock()
	out := []SplitView{}
	for _, c := range s.cards {
		if c.Goal != goal {
			continue
		}
		v := SplitView{SplitCard: *c}
		if c.State == SplitWaiting || c.State == SplitQueued {
			v.SecretAt, _ = ForgeSecret(c.Title, c.Message)
		}
		out = append(out, v)
	}
	s.mu.Unlock()
	s.cfg.Show(goal, out)
}

func (s *Splits) saveLocked() {
	dropped := 0
	for len(s.cards) > MaxSplitCards {
		i := max(slices.IndexFunc(s.cards, func(c *SplitCard) bool {
			return c.State != SplitWaiting && c.State != SplitQueued && c.State != SplitStarting
		}), 0)
		s.cards = slices.Delete(s.cards, i, i+1)
		dropped++
	}
	if s.cfg.Store == nil {
		return
	}
	if dropped > 0 {
		s.cfg.Store.Dropped(dropped)
	}
	out := make([]SplitCard, 0, len(s.cards))
	for _, c := range s.cards {
		out = append(out, *c)
	}
	if err := s.cfg.Store.Save(out); err != nil {
		s.cfg.Logger.Warn("splits", "event", "not-saved")
	}
}

func splitText(title, message string) (string, string, string) {
	title = strings.TrimSpace(title)
	message = strings.TrimSpace(message)
	if title == "" {
		return "", "", "a task's title is empty"
	}
	if _, why := ForgeText(title, MaxSplitTitle, false); why != "" {
		return "", "", "a task's title " + why + "; give one line of plain text"
	}
	if message == "" {
		return "", "", "a task's first message is empty"
	}
	if strings.HasPrefix(message, "!") {
		return "", "", "a task's first message can't start with !"
	}
	if _, why := ForgeText(message, MaxSplitMessage, true); why != "" {
		return "", "", "a task's first message " + why
	}
	return title, message, ""
}

func (s *Splits) Request(sid string, tasks []SplitTask) ForgeReply {
	refuse := func(why string) ForgeReply { return ForgeReply{Result: SplitRefused, Reason: why} }
	now := s.cfg.Now()
	s.mu.Lock()
	closed, allowed := s.closed, s.asks.allow(sid, now)
	s.mu.Unlock()
	switch {
	case closed:
		return refuse(forgeStopping)
	case !allowed:
		return refuse("factory_split was called more than " + strconv.Itoa(SplitCalls) + " times in 10 minutes for this session; end your turn and let the person act on the page")
	}
	if _, ok := s.cfg.Dispatcher.PushTarget(sid); !ok || s.cfg.Dispatcher.historyOnly(sid) != nil {
		return refuse("only a session this engine started can use this tool")
	}
	if len(tasks) == 0 {
		return refuse("give at least one task")
	}
	if len(tasks) > MaxSplitTasks {
		return refuse("a proposal has at most " + strconv.Itoa(MaxSplitTasks) + " tasks; propose the first " + strconv.Itoa(MaxSplitTasks) + " and say in your reply what's left")
	}
	clean := make([]SplitTask, 0, len(tasks))
	for i, t := range tasks {
		title, message, why := splitText(t.Title, t.Message)
		if why != "" {
			return refuse("task " + strconv.Itoa(i+1) + ": " + why)
		}
		clean = append(clean, SplitTask{Title: title, Message: message})
	}
	s.mu.Lock()
	waiting := 0
	for _, c := range s.cards {
		if c.Goal == sid && (c.State == SplitWaiting || c.State == SplitQueued) {
			waiting++
		}
	}
	if waiting+len(clean) > maxGoalWaiting {
		s.mu.Unlock()
		return refuse(strconv.Itoa(waiting) + " tasks of this session already wait for the person on the page; end your turn")
	}
	added := 0
	for _, t := range clean {
		if slices.ContainsFunc(s.cards, func(c *SplitCard) bool {
			return c.Goal == sid && c.State == SplitWaiting && c.Title == t.Title && c.Message == t.Message
		}) {
			continue
		}
		s.cards = append(s.cards, &SplitCard{Card: randomHex(16), Goal: sid, Title: t.Title, Message: t.Message, State: SplitWaiting, Asked: now, At: now})
		added++
	}
	s.saveLocked()
	s.mu.Unlock()
	s.show(sid)
	s.cfg.Logger.Info("splits", "event", "proposed", "tasks", len(clean), "added", added)
	return ForgeReply{Result: SplitHeld, Reason: strconv.Itoa(len(clean)) + " tasks wait for the person as cards on Software Factory's page; the person reads, edits and starts each one, or doesn't, and each starts as its own session from main. End your turn and don't propose them again"}
}

func (s *Splits) cardLocked(sid, card string) *SplitCard {
	for _, c := range s.cards {
		if c.Goal == sid && c.Card == card {
			return c
		}
	}
	return nil
}

func (s *Splits) Edit(sid, card, title, message string) error {
	title, message, why := splitText(title, message)
	if why != "" {
		return explained(invalid("text"), strings.ToUpper(why[:1])+why[1:]+".")
	}
	s.mu.Lock()
	c := s.cardLocked(sid, card)
	if c == nil || c.State != SplitWaiting && c.State != SplitQueued {
		s.mu.Unlock()
		return explained(conflict("card"), "That card can't be edited any more.")
	}
	if c.Title != title || c.Message != message {
		c.Title, c.Message, c.Edited, c.At = title, message, true, s.cfg.Now()
	}
	s.saveLocked()
	s.mu.Unlock()
	s.show(sid)
	return nil
}

func (s *Splits) Dismiss(sid, card string) error {
	s.mu.Lock()
	c := s.cardLocked(sid, card)
	if c == nil || c.State != SplitWaiting && c.State != SplitQueued {
		s.mu.Unlock()
		return explained(conflict("card"), "That card isn't waiting any more.")
	}
	c.State, c.At = SplitDismissed, s.cfg.Now()
	s.saveLocked()
	s.mu.Unlock()
	s.show(sid)
	return nil
}

func (d *Dispatcher) goalOf(sid string) (path, agent, typ string, running int, ok bool) {
	d.mu.Lock()
	r := d.runs[sid]
	if r == nil || r.handedTo != "" {
		d.mu.Unlock()
		return "", "", "", 0, false
	}
	path, agent, typ = r.repoPath, r.spec.ID, r.typ
	var tasks []string
	for x, t := range d.runs {
		if t.groupKind == GroupGoal && t.group == sid {
			tasks = append(tasks, x)
		}
	}
	d.mu.Unlock()
	return path, agent, typ, d.reg.runningOf(tasks), true
}

func (r *Registry) runningOf(sids []string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, sid := range sids {
		if a := r.admitted[sid]; a != nil && a.running {
			n++
		}
	}
	return n
}

func (s *Splits) Start(ctx context.Context, sid, card string, consent Consent) (Started, error) {
	d := s.cfg.Dispatcher
	if err := d.historyOnly(sid); err != nil {
		return Started{}, err
	}
	path, agent, typ, running, ok := d.goalOf(sid)
	if !ok {
		return Started{}, explained(conflict("goal"), "The goal session isn't one this engine can start tasks from any more.")
	}
	s.mu.Lock()
	c := s.cardLocked(sid, card)
	switch {
	case s.closed:
		s.mu.Unlock()
		return Started{}, explained(conflict("stopping"), stoppingMessage)
	case c == nil || c.State != SplitWaiting && c.State != SplitQueued:
		s.mu.Unlock()
		return Started{}, explained(conflict("card"), "That card isn't waiting any more.")
	case s.starting[sid]:
		s.mu.Unlock()
		return Started{}, explained(limited("busy"), "Another task of this goal is starting. Wait for it.")
	}
	if where, _ := ForgeSecret(c.Title, c.Message); where != "" {
		s.mu.Unlock()
		return Started{}, explained(conflict("secret"), "The card holds what looks like a token ("+where+"). Edit it out before starting.")
	}
	if running >= MaxGoalRunning {
		c.State, c.At = SplitQueued, s.cfg.Now()
		s.saveLocked()
		s.mu.Unlock()
		s.show(sid)
		return Started{}, explained(limited("goal full"), strconv.Itoa(MaxGoalRunning)+" of this goal's sessions are running, so this task waits. It starts when one of them stops, while this page stays open; otherwise press Start again then.")
	}
	before := c.State
	c.State = SplitStarting
	s.starting[sid] = true
	title, message := c.Title, c.Message
	s.mu.Unlock()
	started, err := d.Dispatch(ctx, Dispatch{Path: path, Agent: agent, Name: clipUnits(title, maxName), Type: typ, Consent: consent, group: sid, groupKind: GroupGoal})
	s.mu.Lock()
	delete(s.starting, sid)
	if c = s.cardLocked(sid, card); c != nil {
		if err != nil || started.SID == "" {
			c.State = before
		} else {
			c.State, c.SID, c.At = SplitStarted, started.SID, s.cfg.Now()
		}
	}
	s.saveLocked()
	s.mu.Unlock()
	s.show(sid)
	if err != nil || started.SID == "" {
		return started, err
	}
	s.send.send(started.SID, message, "a task's first message")
	s.cfg.Logger.Info("splits", "event", "started")
	return started, nil
}
