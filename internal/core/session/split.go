package session

import (
	"context"
	"errors"
	"maps"
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
	splitQueueEvery = 5 * time.Second
	splitRetryAfter = 30 * time.Second
	splitStartFor   = 105 * time.Second

	SplitWaiting   = "waiting"
	SplitQueued    = "queued"
	SplitStarting  = "starting"
	SplitStarted   = "started"
	SplitDismissed = "dismissed"

	SplitHeld    = "held"
	SplitRefused = "refused"

	SplitStart   = "start"
	SplitUnqueue = "unqueue"
	SplitEdit    = "edit"
	SplitDismiss = "dismiss"

	splitQueuedNote = "Waits until one of this goal's sessions stops; it starts then."
	splitNeedsNote  = "Its turn in the queue came, but its start needs your answer. Press Start to answer."
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
	Queued  time.Time
}

type SplitView struct {
	SplitCard
	SecretAt string
	Place    int
	Note     string
	Needs    *Needs
	Actions  map[string]string
}

type SplitGoal struct {
	Running int
	Limit   int
	Queue   []string
}

type splitHold struct {
	needs   *Needs
	note    string
	consent Consent
}

type SplitStore interface {
	Load() ([]SplitCard, error)
	Save([]SplitCard) error
	Dropped(n int)
}

type SplitsPage interface {
	Edit(sid, card, title, message string) error
	Dismiss(sid, card string) error
	Unqueue(sid, card string) error
	Start(ctx context.Context, sid, card string, consent Consent) (Started, error)
}

type SplitsConfig struct {
	Dispatcher *Dispatcher
	Store      SplitStore
	Show       func(sid string, list []SplitView, goal SplitGoal)
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
	holds    map[string]*splitHold
	retry    map[string]time.Time
	running  map[string]int
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
	return &Splits{cfg: cfg, send: newFirstSender(cfg.Base, cfg.Tell, cfg.Now, cfg.Logger), asks: newLimiter(SplitCalls, SplitWindow), starting: map[string]bool{},
		holds: map[string]*splitHold{}, retry: map[string]time.Time{}, running: map[string]int{}}, nil
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

func (r *Registry) SetSplits(sid string, list []SplitView, goal SplitGoal) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.admitted[sid]
	if a == nil {
		return
	}
	a.splits = slices.Clone(list)
	a.splitGoal = nil
	if len(list) > 0 {
		g := goal
		g.Queue = slices.Clone(goal.Queue)
		a.splitGoal = &g
	}
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
		if c.State == SplitQueued && c.Queued.IsZero() {
			c.Queued = c.At
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
	running := 0
	if _, _, _, n, ok := s.cfg.Dispatcher.goalOf(goal); ok {
		running = n
	}
	s.mu.Lock()
	s.running[goal] = running
	out := []SplitView{}
	queue := s.queueLocked(goal)
	for _, c := range s.cards {
		if c.Goal != goal {
			continue
		}
		v := SplitView{SplitCard: *c}
		if c.State == SplitWaiting || c.State == SplitQueued {
			v.SecretAt, _ = ForgeSecret(c.Title, c.Message)
		}
		if h := s.holds[c.Card]; h != nil && c.State == SplitWaiting {
			v.Note, v.Needs = h.note, h.needs
		}
		if c.State == SplitQueued {
			v.Note, v.Place = splitQueuedNote, slices.Index(queue, c.Card)+1
		}
		v.Actions = splitActions(v)
		out = append(out, v)
	}
	s.mu.Unlock()
	s.cfg.Show(goal, out, SplitGoal{Running: running, Limit: MaxGoalRunning, Queue: queue})
}

func splitActions(v SplitView) map[string]string {
	switch v.State {
	case SplitWaiting:
		start := ""
		if v.SecretAt != "" {
			start = "This looks like a token (" + v.SecretAt + "). Edit it out before starting."
		}
		return map[string]string{SplitStart: start, SplitEdit: "", SplitDismiss: ""}
	case SplitQueued:
		return map[string]string{SplitUnqueue: "", SplitEdit: "", SplitDismiss: ""}
	}
	return nil
}

func (s *Splits) queueLocked(goal string) []string {
	var list []*SplitCard
	for _, c := range s.cards {
		if c.Goal == goal && c.State == SplitQueued {
			list = append(list, c)
		}
	}
	slices.SortStableFunc(list, func(a, b *SplitCard) int { return a.Queued.Compare(b.Queued) })
	out := make([]string, 0, len(list))
	for _, c := range list {
		out = append(out, c.Card)
	}
	return out
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

func splitText(title, message string) (string, string, string, string) {
	title = strings.TrimSpace(title)
	message = strings.TrimSpace(message)
	if title == "" {
		return "", "", "title", "a task's title is empty"
	}
	if _, why := ForgeText(title, MaxSplitTitle, false); why != "" {
		return "", "", "title", "a task's title " + why + "; give one line of plain text"
	}
	if message == "" {
		return "", "", "message", "a task's first message is empty"
	}
	if strings.HasPrefix(message, "!") {
		return "", "", "message", "a task's first message can't start with !"
	}
	if _, why := ForgeText(message, MaxSplitMessage, true); why != "" {
		return "", "", "message", "a task's first message " + why
	}
	return title, message, "", ""
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
		title, message, _, why := splitText(t.Title, t.Message)
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
	title, message, field, why := splitText(title, message)
	if why != "" {
		return onField(field, explained(invalid("text"), strings.ToUpper(why[:1])+why[1:]+"."))
	}
	if err := heldBranch(s.cfg.Dispatcher.Doubled, sid); err != nil {
		return err
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
	if err := heldBranch(s.cfg.Dispatcher.Doubled, sid); err != nil {
		return err
	}
	s.mu.Lock()
	c := s.cardLocked(sid, card)
	if c == nil || c.State != SplitWaiting && c.State != SplitQueued {
		s.mu.Unlock()
		return explained(conflict("card"), "That card isn't waiting any more.")
	}
	c.State, c.At = SplitDismissed, s.cfg.Now()
	delete(s.holds, card)
	s.saveLocked()
	s.mu.Unlock()
	s.show(sid)
	return nil
}

func (d *Dispatcher) goalOf(sid string) (path, agent, typ string, running int, ok bool) {
	d.mu.Lock()
	r := d.runs[sid]
	if r == nil || r.doubled {
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
	return s.start(ctx, sid, card, consent, false)
}

func (s *Splits) start(ctx context.Context, sid, card string, consent Consent, queued bool) (Started, error) {
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
	case c == nil || c.State != SplitWaiting && c.State != SplitQueued || queued && c.State != SplitQueued:
		s.mu.Unlock()
		return Started{}, explained(conflict("card"), "That card isn't waiting any more.")
	case s.starting[sid]:
		s.mu.Unlock()
		return Started{}, explained(limited("busy"), "Another task of this goal is starting. Wait for it.")
	}
	if where, _ := ForgeSecret(c.Title, c.Message); where != "" {
		err := explained(conflict("secret"), "The card holds what looks like a token ("+where+"). Edit it out before starting.")
		if queued {
			s.afterQueuedLocked(c, err)
			s.saveLocked()
		}
		s.mu.Unlock()
		s.show(sid)
		return Started{}, err
	}
	if queued {
		if h := s.holds[card]; h != nil {
			consent = h.consent
		}
	}
	if running >= MaxGoalRunning {
		if !queued {
			if c.State != SplitQueued {
				c.Queued = s.cfg.Now()
			}
			c.State, c.At = SplitQueued, s.cfg.Now()
			s.holds[card] = &splitHold{consent: consent}
			s.saveLocked()
		}
		s.mu.Unlock()
		s.show(sid)
		return Started{}, explained(limited("goal full"), strconv.Itoa(MaxGoalRunning)+" of this goal's sessions are running, so this task waits. It starts when one of them stops.")
	}
	before := c.State
	c.State = SplitStarting
	s.starting[sid] = true
	if !queued {
		delete(s.holds, card)
	}
	title, message := c.Title, c.Message
	s.mu.Unlock()
	started, err := d.Dispatch(ctx, Dispatch{Path: path, Agent: agent, Name: clipUnits(title, maxName), Type: typ, Consent: consent, group: sid, groupKind: GroupGoal, asked: "split:" + sid + ":" + card})
	s.mu.Lock()
	delete(s.starting, sid)
	if c = s.cardLocked(sid, card); c != nil {
		if err != nil || started.SID == "" {
			c.State = before
		} else {
			c.State, c.SID, c.At = SplitStarted, started.SID, s.cfg.Now()
			delete(s.holds, card)
		}
	}
	if c != nil && queued && (err != nil || started.SID == "") {
		s.afterQueuedLocked(c, err)
	}
	s.saveLocked()
	s.mu.Unlock()
	s.show(sid)
	if err != nil || started.SID == "" {
		return started, err
	}
	s.send.send(started.SID, message, "a task's first message")
	shared.For(ctx, s.cfg.Logger).Info("splits", "event", "started", "queued", queued)
	return started, nil
}

func (s *Splits) afterQueuedLocked(c *SplitCard, err error) {
	e, ok := errors.AsType[*Error](err)
	switch {
	case ok && e.Needs != nil:
		c.State, c.At = SplitWaiting, s.cfg.Now()
		s.holds[c.Card] = &splitHold{needs: e.Needs, note: splitNeedsNote}
		s.cfg.Logger.Info("splits", "event", "queued-needs-answer")
	case ok && (e.Kind == Limited || e.Kind == Full) || err == nil:
		s.retry[c.Goal] = s.cfg.Now().Add(splitRetryAfter)
	default:
		why := "the engine couldn't start it"
		if ok && e.Message != "" {
			why = e.Message
		}
		c.State, c.At = SplitWaiting, s.cfg.Now()
		s.holds[c.Card] = &splitHold{note: "It didn't start from the queue: " + why}
		s.cfg.Logger.Info("splits", "event", "queued-not-started")
	}
}

func (s *Splits) Unqueue(sid, card string) error {
	if err := heldBranch(s.cfg.Dispatcher.Doubled, sid); err != nil {
		return err
	}
	s.mu.Lock()
	c := s.cardLocked(sid, card)
	if c == nil || c.State != SplitQueued {
		s.mu.Unlock()
		return explained(conflict("card"), "That card isn't in the queue any more.")
	}
	c.State, c.At, c.Queued = SplitWaiting, s.cfg.Now(), time.Time{}
	delete(s.holds, card)
	s.saveLocked()
	s.mu.Unlock()
	s.show(sid)
	return nil
}

func (s *Splits) Run(ctx context.Context) {
	t := time.NewTicker(splitQueueEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.tick(ctx)
	}
}

func (s *Splits) tick(ctx context.Context) {
	now := s.cfg.Now()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	goals := []string{}
	held := map[string]bool{}
	for _, c := range s.cards {
		open := c.State == SplitWaiting || c.State == SplitQueued || c.State == SplitStarted
		if open && !slices.Contains(goals, c.Goal) {
			goals = append(goals, c.Goal)
		}
		if h := s.holds[c.Card]; h != nil && h.needs != nil && c.State == SplitWaiting {
			held[c.Goal] = true
		}
	}
	shown := maps.Clone(s.running)
	s.mu.Unlock()
	for _, goal := range goals {
		_, _, _, running, ok := s.cfg.Dispatcher.goalOf(goal)
		if !ok {
			continue
		}
		if was, seen := shown[goal]; !seen || was != running {
			s.show(goal)
		}
		s.mu.Lock()
		queue := s.queueLocked(goal)
		wait := held[goal] || s.starting[goal] || now.Before(s.retry[goal]) || len(queue) == 0 || running >= MaxGoalRunning
		s.mu.Unlock()
		if wait {
			continue
		}
		start, cancel := context.WithTimeout(ctx, splitStartFor)
		_, err := s.start(start, goal, queue[0], Consent{}, true)
		cancel()
		if err != nil {
			shared.For(ctx, s.cfg.Logger).Info("splits", "event", "queued-start-refused")
		}
	}
}
