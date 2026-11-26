package session

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"maps"
	"math"
	"path"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/level"
)

const (
	Protocol         = 1
	MinProtocol      = 1
	LiveFor          = 30 * time.Second
	Handover         = 3 * time.Second
	HoldAnswer       = time.Minute
	ForgetConn       = 10 * time.Minute
	ReplayGap        = time.Second
	NewClaimShown    = 30 * time.Second
	MaxConns         = 400
	MaxStrangerConns = 100
	MaxBoxStrangers  = 20
	BoxRegistrations = 10
	MaxStreams       = 2
	MaxInbox         = 200
	MaxText          = 20000
	MaxCwd           = 1000
	MaxRegisterCards = 50
	MaxUnverified    = 20
	MaxObservations  = 1000
	maxRemembered    = 1000
	DevCheckVariable = "SF_DEV_CHECK_KEY"
)

type Fields = map[string]any

type Sink interface {
	Hello(stream string, sessions []Info) bool
	Sessions(sessions []Info) bool
	Frame(frame Fields) bool
	Close()
}

type Admission struct {
	ID        string
	Name      string
	Kind      string
	Agent     string
	Cwd       string
	Project   string
	StartedAt time.Time
	Control   bool
	SignIn    *SignIn
	Mode      string
	Lineage   *Lineage
	Box       string
	Branch    string
	Doubled   bool
}

type Registration struct {
	SID        string
	Cwd        string
	Version    string
	Epoch      string
	Mode       string
	Protocol   int
	LastSeq    int64
	Controlled bool
	Restored   bool
	GateKnown  bool
	Busy       bool
	Login      bool
	Cards      []any
	Box        string
	keepPush   string
	bridged    bool
}

type Registered struct {
	Secret     string
	Push       string
	Version    string
	Protocol   int
	Page       bool
	Control    bool
	Background *bool
}

type Batch struct {
	Stream string
	Reset  bool
	Events []any
}

type Info struct {
	SID             string
	Cwd             string
	Name            string
	ID              string
	Kind            string
	Agent           string
	Project         string
	Mode            string
	Epoch           string
	StartedAt       time.Time
	Verified        bool
	Running         bool
	Live            bool
	Disputed        bool
	Outdated        bool
	Controlled      bool
	Busy            bool
	Login           bool
	Claims          []string
	Needs           int
	LastSeq         int64
	Level           level.Level
	LevelReason     string
	LevelDropped    bool
	LevelLost       bool
	Sandboxed       bool
	Review          *ReviewView
	Push            *PushView
	Forge           []ForgeView
	Access          []AccessCard
	AccessMarker    string
	AccessNotes     []AccessNote
	SignIn          *SignIn
	State           string
	Status          string
	WaitingFor      string
	Queued          int
	Unbacked        []TurnMarks
	StopReason      string
	Uncommitted     *Uncommitted
	Overlaps        []Overlap
	Checks          []AgentCheck
	Lineage         *Lineage
	Follow          *FollowView
	Splits          []SplitView
	SplitGoal       *SplitGoal
	Box             string
	SignInWait      bool
	HomeHeld        bool
	Branch          string
	Doubled         bool
	Stalled         bool
	StalledSince    time.Time
	Compacting      bool
	Subagents       bool
	CheckpointSaved bool
	Step            string
	StepsRan        []string
	AgentStates     map[string]string
}

type conn struct {
	secret     string
	push       string
	box        string
	sid        string
	cwd        string
	epoch      string
	version    string
	mode       string
	protocol   int
	controlled bool
	busy       bool
	login      bool
	lastSeq    int64
	seen       time.Time
	born       time.Time
	order      uint64
	poll       *waiter
	inbox      []Fields
	bridged    bool
}

type checker struct {
	sid string
	box string
}

type admitted struct {
	Admission
	running      bool
	waitSignIn   bool
	homeHeld     bool
	observations []level.Observation
	review       *ReviewView
	push         *PushView
	forge        []ForgeView
	access       []AccessCard
	accessMarker string
	accessNotes  []AccessNote
	follow       *FollowView
	splits       []SplitView
	splitGoal    *SplitGoal
	turnLog      *turnLog
	unbacked     []TurnMarks
	lastMessage  string
	turnSince    time.Time
	stopReason   string
	doneItself   bool
	lastReport   time.Time
	subagents    bool
	compacting   bool
	contextLevel string
	checkpointed string
	step         string
	stepsRan     []string
	agentStates  map[string]string
}

type replay struct {
	at   time.Time
	next Fields
}

type Registry struct {
	mu          sync.Mutex
	now         func() time.Time
	version     string
	conns       map[string]*conn
	checkers    map[string]checker
	cards       map[string]*card
	admitted    map[string]*admitted
	lastMode    map[string]string
	lastGate    map[string]bool
	restored    map[string]bool
	checked     bool
	catalogs    map[string]*catalog
	workflows   map[string]*Workflow
	sinks       map[string]Sink
	replays     map[string]*replay
	registerAll *limiter
	registerOne *limiter
	registerBox *limiter
	shown       []Info
	order       uint64
	uploads     *uploadBook
	shots       screenshotBook
	queued      map[string]*queue
	notes       map[string]map[string]string
	states      map[string]AgentState
	supervisor  string
	claimChecks *ClaimChecks
	claimSerial uint64
	turned      func()
	turnSaving  bool
	turnAgain   bool
	compacted   func(sid string, queued bool)
	docsMoved   func(sid string)
	compactions *limiter
	noteStore   NoteStore
	noteVersion uint64
	noteSaved   uint64
	noteMu      sync.Mutex
	halt        *Halt
	worktrees   *worktreeBook
	limits      *Limits
	work        *SessionWork
	branchCopy  *Keep
	firstSteps  FirstSteps
}

var (
	sidPattern     = regexp.MustCompile(`^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$`)
	versionPattern = regexp.MustCompile(`^\d{1,9}\.\d{1,9}\.\d{1,9}$`)
	epochPattern   = regexp.MustCompile(`^[0-9a-f]{16}$`)
	eventType      = regexp.MustCompile(`^[a-z][a-z_]{0,39}$`)
	streamPattern  = regexp.MustCompile(`^[0-9a-f]{24}$`)
	controlChars   = regexp.MustCompile(`[\x00-\x1f\x7f]`)
)

var engineFrames = map[string]bool{"hello": true, "sessions": true, "reset": true, "dropped": true, "omitted": true, NotifyFrame: true}

var forwarded = map[string]bool{
	"prompt": true, "command": true, "text": true, "tool": true, "tool_done": true, "busy": true, "idle": true,
	"ask": true, "permission": true, "plan": true, "resolved": true, "ended": true, "agents": true, "catalog": true,
	"control": true, "mode": true, "login": true, "usage": true, "compact": true, "context": true,
	"workflow": true,
}

func New(version string, now func() time.Time) *Registry {
	if now == nil {
		now = time.Now
	}
	r := &Registry{
		now:         now,
		version:     version,
		conns:       map[string]*conn{},
		checkers:    map[string]checker{},
		cards:       map[string]*card{},
		admitted:    map[string]*admitted{},
		lastMode:    map[string]string{},
		lastGate:    map[string]bool{},
		restored:    map[string]bool{},
		catalogs:    map[string]*catalog{},
		workflows:   map[string]*Workflow{},
		sinks:       map[string]Sink{},
		replays:     map[string]*replay{},
		registerAll: newLimiter(20, time.Minute),
		registerOne: newLimiter(3, time.Minute),
		registerBox: newLimiter(BoxRegistrations, time.Minute),
		uploads:     newUploadBook(),
		queued:      map[string]*queue{},
		states:      map[string]AgentState{},
		compactions: newLimiter(compactNotes, compactWindow),
	}
	r.halt = newHalt(now, r.Notify)
	r.limits = newLimits(r)
	return r
}

func secret() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func streamID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r > 0xffff {
			n++
		}
	}
	return n
}

func absolute(p string) bool {
	return p != "" && len(p) <= MaxCwd && path.IsAbs(p) && !controlChars.MatchString(p) && utf8.ValidString(p)
}

func (r *Registry) live(c *conn, now time.Time) bool {
	return c.poll != nil || now.Sub(c.seen) < LiveFor
}

func (r *Registry) liveFor(sid string, now time.Time) []*conn {
	var out []*conn
	for _, c := range r.conns {
		if c.sid == sid && r.live(c, now) {
			out = append(out, c)
		}
	}
	slices.SortFunc(out, func(a, b *conn) int { return cmp.Compare(a.order, b.order) })
	return out
}

func (r *Registry) disputed(sid string, now time.Time) bool {
	return len(r.liveFor(sid, now)) > 1
}

func (r *Registry) only(sid string, now time.Time) *conn {
	if live := r.liveFor(sid, now); len(live) == 1 {
		return live[0]
	}
	return nil
}

func (r *Registry) connOf(given string, now time.Time) *conn {
	if len(given) != 43 {
		return nil
	}
	c := r.conns[given]
	if c == nil {
		return nil
	}
	c.seen = now
	return c
}

func (r *Registry) Register(g Registration) (Registered, error) {
	if !sidPattern.MatchString(g.SID) || !absolute(g.Cwd) || !versionPattern.MatchString(g.Version) || !epochPattern.MatchString(g.Epoch) {
		return Registered{}, toAgent(Invalid, "register")
	}
	if g.LastSeq < 0 || g.LastSeq > 1<<53 {
		g.LastSeq = 0
	}
	if !ValidBox(g.Box) {
		return Registered{}, unauthorized()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	a := r.admitted[g.SID]
	if a != nil && a.Box != g.Box {
		return Registered{}, unauthorized()
	}
	if !r.registerBox.allow(g.Box, now) {
		return Registered{}, toAgent(Limited, "register")
	}
	stranger := a == nil
	if stranger && !r.registerAll.allow("all", now) {
		return Registered{}, toAgent(Limited, "register")
	}
	if !r.registerOne.allow(g.SID, now) {
		return Registered{}, toAgent(Limited, "register")
	}
	crowded := func() bool {
		return len(r.conns) >= MaxConns || stranger && (r.strangerConns() >= MaxStrangerConns || r.boxStrangers(g.Box) >= MaxBoxStrangers)
	}
	if crowded() {
		r.sweep(now)
	}
	if crowded() {
		return Registered{}, toAgent(Full, "connections")
	}
	for s, old := range r.conns {
		if old.sid == g.SID && !r.live(old, now) {
			delete(r.conns, s)
		}
	}
	for _, old := range r.liveFor(g.SID, now) {
		if old.poll != nil {
			old.poll.finish(waitEmpty)
			old.poll = nil
		}
		if cut := now.Add(Handover - LiveFor); old.seen.After(cut) {
			old.seen = cut
		}
	}
	mode := clip(g.Mode, 40)
	if mode == "" {
		mode = r.lastMode[g.SID]
	}
	r.order++
	push := g.keepPush
	if len(push) != 43 {
		push = secret()
	}
	c := &conn{
		order:      r.order,
		secret:     secret(),
		push:       push,
		box:        g.Box,
		sid:        g.SID,
		cwd:        g.Cwd,
		epoch:      g.Epoch,
		version:    g.Version,
		mode:       mode,
		protocol:   g.Protocol,
		controlled: g.Controlled,
		busy:       g.Busy,
		login:      g.Login,
		lastSeq:    g.LastSeq,
		seen:       now,
		born:       now,
		bridged:    g.bridged,
	}
	r.conns[c.secret] = c
	if g.GateKnown {
		r.lastGate[g.SID] = g.Controlled || g.Restored
	}
	r.lastMode[g.SID] = mode
	open := map[string]bool{}
	for i, raw := range g.Cards {
		if i >= MaxRegisterCards {
			break
		}
		if ev, ok := raw.(map[string]any); ok && r.indexCard(g.SID, ev) {
			open[ev["id"].(string)] = true
		}
	}
	if !r.disputed(g.SID, now) {
		for id, k := range r.cards {
			if k.sid == g.SID && !k.answered && !open[id] {
				r.dropCard(id)
			}
		}
	}
	out := Registered{Secret: c.secret, Push: c.push, Version: r.version, Protocol: Protocol, Page: len(r.sinks) > 0}
	if a != nil {
		background := a.Kind == "background"
		out.Background = &background
		out.Control = a.running && a.Control
	}
	r.announce(now)
	return out, nil
}

func (r *Registry) Poll(ctx context.Context, given string) ([]Fields, error) {
	r.mu.Lock()
	now := r.now()
	c := r.connOf(given, now)
	if c == nil {
		r.mu.Unlock()
		return nil, unauthorized()
	}
	if r.disputed(c.sid, now) {
		r.mu.Unlock()
		return nil, toAgent(Conflict, "disputed")
	}
	if c.poll != nil {
		c.poll.finish(waitEmpty)
	}
	w := newWaiter()
	c.poll = w
	r.deliver(c, now)
	r.mu.Unlock()

	select {
	case <-w.done:
	case <-ctx.Done():
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	now = r.now()
	if c.poll == w {
		c.poll = nil
		w.finish(waitEmpty)
		if errors.Is(ctx.Err(), context.Canceled) {
			c.seen = time.Time{}
		} else {
			c.seen = now
		}
	}
	switch w.end {
	case waitDelivered:
		return w.messages, nil
	case waitRevoked:
		return nil, unauthorized()
	}
	return nil, nil
}

func (r *Registry) deliver(c *conn, now time.Time) {
	if c.poll == nil || len(c.inbox) == 0 || r.disputed(c.sid, now) {
		return
	}
	w := c.poll
	c.poll = nil
	w.messages = c.inbox
	c.inbox = nil
	w.finish(waitDelivered)
}

func (r *Registry) tell(c *conn, msg Fields, now time.Time) {
	if len(c.inbox) >= MaxInbox {
		c.inbox = c.inbox[1:]
	}
	c.inbox = append(c.inbox, msg)
	r.deliver(c, now)
}

func (r *Registry) tellAll(msg Fields, now time.Time) {
	for _, c := range r.conns {
		if r.live(c, now) {
			r.tell(c, msg, now)
		}
	}
}

func sequence(v any) (int64, bool) {
	f, ok := v.(float64)
	if !ok || f < 1 || f > 1<<53 || f != math.Trunc(f) {
		return 0, false
	}
	return int64(f), true
}

func (r *Registry) Report(given string, b Batch) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	c := r.connOf(given, now)
	if c == nil {
		return unauthorized()
	}
	if r.disputed(c.sid, now) {
		return toAgent(Conflict, "disputed")
	}
	var target Sink
	if b.Stream != "" {
		if !streamPattern.MatchString(b.Stream) {
			return toAgent(Invalid, "stream")
		}
		if target = r.sinks[b.Stream]; target == nil {
			return nil
		}
		if b.Reset && !target.Frame(Fields{"type": "reset", "sid": c.sid, "epoch": c.epoch}) {
			r.detach(b.Stream, now)
			return nil
		}
	}
	changed := false
	for _, raw := range b.Events {
		denied := false
		ev, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		kind, _ := ev["type"].(string)
		if !eventType.MatchString(kind) || engineFrames[kind] {
			continue
		}
		seq, ok := sequence(ev["seq"])
		if !ok {
			continue
		}
		shown := forwarded[kind]
		turnEnded := false
		if kind == "workflow" {
			clean, ok := workflowOf(ev)
			if !ok {
				shown = false
			} else {
				ev = clean.fields(ev["seq"], ev["ts"])
				if target == nil && seq > c.lastSeq {
					w := clean
					moved := docsDiffer(r.workflows[c.sid], &w)
					r.workflows[c.sid] = &w
					if moved && r.docsMoved != nil {
						go r.docsMoved(c.sid)
					}
				}
			}
		}
		if target == nil {
			if seq <= c.lastSeq {
				continue
			}
			c.lastSeq = seq
			turnEnded = kind == "idle" && c.busy
			changed = r.noteReport(c.sid, kind, ev, now) || changed
			changed = r.noteContext(c.sid, kind, ev) || changed
			r.limitsSeen(c, kind, ev, now)
			r.trackTurn(c, kind, ev, seq, now)
			r.markTurn(c.sid, kind, ev, now)
			r.noteFacts(c.sid, kind, ev, now)
			r.workSeen(c, kind, ev)
			if cardTypes[kind] {
				fresh := r.freshCard(ev)
				shown = r.indexCard(c.sid, ev)
				changed = changed || shown
				denied = shown && fresh && r.awayAnswer(c.sid, ev, now)
			} else if shown {
				changed = r.apply(c, kind, ev, now) || changed
			}
		} else if cardTypes[kind] {
			id, _ := ev["id"].(string)
			known := r.cards[id]
			shown = cardID.MatchString(id) && (known == nil || known.sid == c.sid)
		}
		frame := Fields{"type": "omitted", "seq": float64(seq), "sid": c.sid, "epoch": c.epoch}
		if shown {
			frame = make(Fields, len(ev)+2)
			maps.Copy(frame, ev)
			frame["sid"] = c.sid
			frame["epoch"] = c.epoch
			delete(frame, "away")
			delete(frame, "reported")
			delete(frame, "checkpoint")
			for _, k := range foldKeys {
				delete(frame, k)
			}
			maps.Copy(frame, foldOf(kind, ev))
			if cp := r.checkpointOf(c.sid, kind, ev); cp != nil {
				frame["checkpoint"] = cp
			}
			if !c.bridged || kind != "prompt" {
				frame["reported"] = true
			}
			if cardTypes[kind] {
				r.markAway(frame, denied)
			}
		} else if ts, ok := ev["ts"].(float64); ok {
			frame["ts"] = ts
		}
		if target != nil {
			if !target.Frame(frame) {
				r.detach(b.Stream, now)
				return nil
			}
			continue
		}
		r.broadcast(frame, now)
		if turnEnded {
			r.broadcast(notifyOf(c.sid, NotifyTurn, "turn:"+c.sid+":"+strconv.FormatInt(seq, 10)), now)
		}
	}
	if changed {
		r.announce(now)
	}
	return nil
}

func (r *Registry) apply(c *conn, kind string, ev map[string]any, now time.Time) bool {
	switch kind {
	case "resolved":
		id, _ := ev["id"].(string)
		if k := r.cards[id]; k != nil && k.sid == c.sid {
			r.dropCard(id)
			return true
		}
	case "login":
		c.login = ev["failed"] == true
		return true
	case "busy", "idle":
		c.busy = kind == "busy"
		return true
	case "mode":
		if mode, ok := ev["mode"].(string); ok {
			c.mode = clip(mode, 40)
			r.lastMode[c.sid] = c.mode
			return true
		}
	case "control":
		c.controlled = ev["on"] == true
		r.lastGate[c.sid] = c.controlled || ev["restored"] == true
		return true
	case "catalog":
		if list, ok := ev["commands"].([]any); ok {
			known := catalogOf(list)
			r.catalogs[c.sid] = &known
		}
	}
	return false
}

func (r *Registry) Notify(frame Fields) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.broadcast(frame, r.now())
}

func (r *Registry) broadcast(frame Fields, now time.Time) {
	for id, s := range r.sinks {
		if !s.Frame(frame) {
			r.detach(id, now)
		}
	}
}

func (r *Registry) Wait(ctx context.Context, given, id string) (any, bool, error) {
	r.mu.Lock()
	now := r.now()
	c := r.connOf(given, now)
	if c == nil {
		r.mu.Unlock()
		return nil, false, unauthorized()
	}
	k := r.cards[id]
	if k == nil || k.sid != c.sid {
		r.mu.Unlock()
		return nil, false, toAgent(NotFound, "card")
	}
	if r.only(c.sid, now) != c {
		r.mu.Unlock()
		return nil, false, toAgent(Conflict, "disputed")
	}
	if k.answered {
		delete(r.cards, id)
		r.announce(now)
		r.mu.Unlock()
		return k.answer, true, nil
	}
	if k.waiter != nil {
		k.waiter.finish(waitEmpty)
	}
	w := newWaiter()
	k.waiter = w
	r.mu.Unlock()

	select {
	case <-w.done:
	case <-ctx.Done():
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if k.waiter == w {
		k.waiter = nil
		w.finish(waitEmpty)
	}
	switch w.end {
	case waitDelivered:
		return w.answer, true, nil
	case waitGone:
		return nil, false, toAgent(Gone, "card")
	}
	return nil, false, nil
}

func (r *Registry) Wake(given, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.connOf(given, r.now())
	if c == nil {
		return unauthorized()
	}
	if k := r.cards[id]; k != nil && k.sid == c.sid && k.waiter != nil {
		k.waiter.finish(waitEmpty)
		k.waiter = nil
	}
	return nil
}

func (r *Registry) Known(given string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.connOf(given, r.now()) != nil
}

func (r *Registry) admittedElsewhere(sid, box string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.admitted[sid]
	return a != nil && a.Box != box
}

func (r *Registry) Admit(sid string, a Admission) {
	r.mu.Lock()
	defer r.mu.Unlock()
	known := r.admitted[sid]
	if known == nil {
		known = &admitted{}
		r.admitted[sid] = known
	}
	known.Admission = a
	known.stopReason = ""
	known.doneItself = false
	r.dropOtherBoxes(sid, a.Box)
	if !known.running && len(known.observations) > 0 {
		known.observe(level.Observation{By: level.Engine, Event: level.ProcessStarted})
	}
	known.running = true
	r.announce(r.now())
}

func (r *Registry) Restore(sid string, a Admission, sandboxed bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.admitted[sid] != nil {
		return
	}
	first := level.Observation{By: level.Engine, Event: level.CheckPassed}
	if !sandboxed {
		first = level.Observation{By: level.Engine, Event: level.CheckNotRun, Parts: []string{"the engine restarted and has no record that the check passed throughout this session's earlier runs"}}
	}
	r.admitted[sid] = &admitted{Admission: a, observations: []level.Observation{first, {By: level.Engine, Event: level.ProcessEnded}}}
	if !r.checked {
		r.restored[sid] = true
	}
	r.announce(r.now())
}

func (r *Registry) WaitSignIn(sid string, on bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if a := r.admitted[sid]; a != nil && a.waitSignIn != on {
		a.waitSignIn = on
		r.announce(r.now())
	}
}

func (r *Registry) HoldHome(sid string, on bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if a := r.admitted[sid]; a != nil && a.homeHeld != on {
		a.homeHeld = on
		r.announce(r.now())
	}
}

func (r *Registry) CheckPassed() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checked = true
	clear(r.restored)
}

func (r *Registry) PushLevel(sid string) (level.Result, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.admitted[sid]
	if a == nil {
		return level.Result{}, false
	}
	lv := level.Of(a.observations)
	if lv.Sandboxed && r.restored[sid] {
		return level.Result{Level: level.Held, Reason: WaitingForFirstCheck, Dropped: lv.Dropped, Waiting: true}, true
	}
	return lv, true
}

func (r *Registry) Forget(sid string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.admitted[sid] == nil {
		return
	}
	box := r.admitted[sid].Box
	delete(r.admitted, sid)
	r.dropCheckers(func(c checker) bool { return c.sid == sid })
	for key, c := range r.conns {
		if c.sid != sid {
			continue
		}
		if c.poll != nil {
			c.poll.finish(waitRevoked)
			c.poll = nil
		}
		delete(r.conns, key)
	}
	for id, k := range r.cards {
		if k.sid == sid {
			r.dropCard(id)
		}
	}
	delete(r.catalogs, sid)
	delete(r.workflows, sid)
	delete(r.lastMode, sid)
	delete(r.lastGate, sid)
	delete(r.restored, sid)
	delete(r.queued, sid)
	r.dropNotesLocked(sid)
	delete(r.states, sid)
	delete(r.uploads.noImage, sid)
	r.dropUploads(sid)
	r.dropScreenshots(box, sid)
	r.announce(r.now())
	go r.halt.forget(sid)
}

func (r *Registry) dropOtherBoxes(sid, box string) {
	r.dropCheckers(func(c checker) bool { return c.sid == sid && c.box != box })
	for key, c := range r.conns {
		if c.sid != sid || c.box == box {
			continue
		}
		if c.poll != nil {
			c.poll.finish(waitRevoked)
			c.poll = nil
		}
		delete(r.conns, key)
	}
}

func (r *Registry) FromBox(given, push, box string) error {
	if !ValidBox(box) {
		return unauthorized()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	fits := func(c *conn) bool {
		if c.box != box {
			return false
		}
		a := r.admitted[c.sid]
		return a == nil || a.Box == c.box
	}
	if len(given) == 43 {
		if c := r.conns[given]; c != nil && !fits(c) {
			return unauthorized()
		}
		if c, ok := r.checkers[given]; ok && c.box != box {
			return unauthorized()
		}
	}
	if len(push) == 43 {
		for _, c := range r.conns {
			if subtle.ConstantTimeCompare([]byte(c.push), []byte(push)) == 1 && !fits(c) {
				return unauthorized()
			}
		}
	}
	return nil
}

func (r *Registry) DropBox(box string) int {
	if !ValidBox(box) {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dropCheckers(func(c checker) bool { return c.box == box })
	n := 0
	for key, c := range r.conns {
		if c.box != box {
			continue
		}
		if c.poll != nil {
			c.poll.finish(waitRevoked)
			c.poll = nil
		}
		delete(r.conns, key)
		n++
	}
	if n > 0 {
		r.announce(r.now())
	}
	return n
}

func (r *Registry) PushSession(token string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(token) != 43 {
		return "", unauthorized()
	}
	now := r.now()
	for _, c := range r.conns {
		if subtle.ConstantTimeCompare([]byte(c.push), []byte(token)) == 1 {
			if r.disputed(c.sid, now) {
				return "", toAgent(Conflict, "disputed")
			}
			return c.sid, nil
		}
	}
	return "", unauthorized()
}

func (r *Registry) Observe(sid string, o level.Observation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.admitted[sid]
	if a == nil || o.By != level.Engine {
		return
	}
	a.observe(o)
	r.announce(r.now())
}

func (r *Registry) SetReview(sid string, v ReviewView) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.admitted[sid]
	if a == nil {
		return
	}
	v = v.clone()
	if key, ok := reviewFinished(sid, a.review, v); ok {
		r.broadcast(notifyOf(sid, NotifyReview, key), r.now())
	}
	a.review = &v
	r.announce(r.now())
}

func (r *Registry) SetPush(sid string, v PushView) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.admitted[sid]
	if a == nil {
		return
	}
	v = v.clone()
	a.push = &v
	r.announce(r.now())
}

func (r *Registry) Level(sid string) (level.Result, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.admitted[sid]
	if a == nil {
		return level.Result{}, false
	}
	return level.Of(a.observations), true
}

func (r *Registry) EndedDone(sid string) {
	r.mu.Lock()
	if a := r.admitted[sid]; a != nil {
		a.doneItself = true
	}
	r.mu.Unlock()
	r.Ended(sid, false)
}

func (r *Registry) Ended(sid string, lost bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.admitted[sid]
	if a == nil {
		return
	}
	a.running = false
	r.dropCheckers(func(c checker) bool { return c.sid == sid })
	r.endTurnLocked(a)
	event := level.ProcessEnded
	if lost {
		event = level.ProcessLost
	}
	a.observe(level.Observation{By: level.Engine, Event: event})
	r.announce(r.now())
}

func (a *admitted) observe(o level.Observation) {
	if n := len(a.observations); n > 0 {
		last := a.observations[n-1]
		if last.By == o.By && last.Event == o.Event && slices.Equal(last.Parts, o.Parts) {
			return
		}
	}
	a.observations = append(a.observations, level.Observation{By: o.By, Event: o.Event, Parts: slices.Clone(o.Parts)})
	if len(a.observations) > MaxObservations {
		trimmed := level.Observation{By: level.Engine, Event: level.CheckNotRun, Parts: []string{"the engine trimmed this session's older checks"}}
		keep := a.observations[len(a.observations)-MaxObservations/2:]
		a.observations = append([]level.Observation{a.observations[0], trimmed}, keep...)
	}
}

func (r *Registry) Attach(s Sink) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.sinks) >= MaxStreams {
		return "", limited("streams", "The page holds two live streams at once, and both are open. Close another tab of the page, then reload this one.")
	}
	now := r.now()
	id := streamID()
	list := r.list(now)
	if !s.Hello(id, list) {
		s.Close()
		return "", full("stream", "The new stream couldn't take its first lines. Reload the page.")
	}
	if r.supervisor != "" && !s.Frame(Fields{"type": "supervisor", "state": r.supervisor}) {
		s.Close()
		return "", full("stream", "The new stream couldn't take its first lines. Reload the page.")
	}
	if !s.Frame(r.halt.frame()) {
		s.Close()
		return "", full("stream", "The new stream couldn't take its first lines. Reload the page.")
	}
	r.sinks[id] = s
	if len(r.sinks) == 1 {
		r.tellAll(Fields{"kind": "page", "connected": true}, now)
	}
	return id, nil
}

func (r *Registry) Detach(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.detach(id, r.now())
}

func (r *Registry) detach(id string, now time.Time) {
	s, ok := r.sinks[id]
	if !ok {
		return
	}
	delete(r.sinks, id)
	s.Close()
	if len(r.sinks) == 0 {
		r.tellAll(Fields{"kind": "page", "connected": false}, now)
	}
}

func (r *Registry) DetachAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	for id := range r.sinks {
		r.detach(id, now)
	}
}

func (r *Registry) Streams() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sinks)
}

func (r *Registry) Sessions() []Info {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.list(r.now())
}

func (r *Registry) list(now time.Time) []Info {
	sids := map[string]bool{}
	for sid := range r.admitted {
		sids[sid] = true
	}
	type claim struct {
		sid  string
		born time.Time
	}
	var claims []claim
	seen := map[string]bool{}
	for _, c := range r.conns {
		if r.admitted[c.sid] != nil || seen[c.sid] {
			continue
		}
		if r.live(c, now) || now.Sub(c.born) < NewClaimShown {
			seen[c.sid] = true
			claims = append(claims, claim{c.sid, c.born})
		}
	}
	slices.SortFunc(claims, func(a, b claim) int { return b.born.Compare(a.born) })
	for i, c := range claims {
		if i >= MaxUnverified {
			break
		}
		sids[c.sid] = true
	}
	keys := make([]string, 0, len(sids))
	for sid := range sids {
		keys = append(keys, sid)
	}
	slices.Sort(keys)
	out := make([]Info, 0, len(keys))
	for _, sid := range keys {
		out = append(out, r.info(sid, now))
	}
	return out
}

func (r *Registry) info(sid string, now time.Time) Info {
	live := r.liveFor(sid, now)
	var c *conn
	if len(live) > 0 {
		c = live[0]
	} else {
		for _, x := range r.conns {
			if x.sid == sid && (c == nil || x.seen.After(c.seen)) {
				c = x
			}
		}
	}
	info := Info{SID: sid, Live: len(live) > 0, Disputed: len(live) > 1, Mode: r.lastMode[sid]}
	if len(live) > 1 {
		for _, x := range live {
			info.Claims = append(info.Claims, x.cwd)
		}
	}
	if c != nil {
		info.Cwd = c.cwd
		info.Epoch = c.epoch
		info.LastSeq = c.lastSeq
		info.Mode = c.mode
		info.Busy = c.busy
		info.Login = c.login
		info.Controlled = c.controlled
		info.Outdated = c.protocol < MinProtocol
	} else if r.admitted[sid] != nil {
		info.Controlled = r.lastControl(sid)
	}
	var observations []level.Observation
	if a := r.admitted[sid]; a != nil {
		info.Verified = true
		info.Running = a.running
		info.Name = a.Name
		info.ID = a.ID
		info.Kind = a.Kind
		info.Agent = a.Agent
		info.Project = a.Project
		info.StartedAt = a.StartedAt
		info.StopReason = a.stopReason
		info.Lineage = a.Lineage
		info.Box, info.SignInWait, info.Branch, info.Doubled = a.Box, a.waitSignIn && !a.running, a.Branch, a.Doubled
		info.HomeHeld = a.homeHeld && !a.running
		if a.SignIn != nil {
			v := *a.SignIn
			info.SignIn = &v
		}
		if info.Cwd == "" {
			info.Cwd = a.Cwd
		}
		if info.Mode == "" {
			info.Mode = a.Mode
		}
		observations = a.observations
		if a.review != nil {
			v := a.review.clone()
			info.Review = &v
		}
		if a.push != nil {
			v := a.push.clone()
			info.Push = &v
		}
		if len(a.unbacked) > 0 {
			info.Unbacked = slices.Clone(a.unbacked)
		}
		if len(a.forge) > 0 {
			info.Forge = slices.Clone(a.forge)
			info.Needs += forgeWaiting(a.forge)
		}
		if len(a.access) > 0 {
			info.Access = slices.Clone(a.access)
			info.Needs += len(a.access)
		}
		info.AccessMarker, info.AccessNotes = a.accessMarker, slices.Clone(a.accessNotes)
		if a.follow != nil {
			info.Follow = a.follow.clone()
		}
		if len(a.splits) > 0 {
			info.Splits = slices.Clone(a.splits)
			info.Needs += splitsWaiting(a.splits)
			if a.splitGoal != nil {
				g := *a.splitGoal
				g.Queue = slices.Clone(a.splitGoal.Queue)
				info.SplitGoal = &g
			}
		}
		if st, ok := r.states[sid]; ok {
			info.State, info.Status, info.WaitingFor = st.State, st.Status, st.WaitingFor
		} else if a.doneItself && !a.running {
			info.State = "done"
		}
		if q := r.queued[sid]; q != nil {
			info.Queued = len(q.msgs)
		}
		r.factsInto(sid, &info)
	}
	for _, k := range r.cards {
		if k.sid == sid {
			info.Needs++
		}
	}
	r.workingInto(sid, &info)
	r.stallInto(sid, &info, now)
	lv := level.Of(observations)
	info.Level = lv.Level
	info.LevelReason = lv.Reason
	info.LevelDropped = lv.Dropped
	info.LevelLost = lv.Lost
	info.Sandboxed = lv.Sandboxed
	return info
}

func (r *Registry) announce(now time.Time) {
	list := r.list(now)
	if reflect.DeepEqual(list, r.shown) {
		return
	}
	r.shown = list
	for id, s := range r.sinks {
		if !s.Sessions(list) {
			r.detach(id, now)
		}
	}
}

func (r *Registry) controllable(sid string, needControl bool, now time.Time) (*conn, error) {
	c := r.only(sid, now)
	if c == nil {
		if len(r.liveFor(sid, now)) > 0 {
			return nil, conflict("disputed", "Two connections claim this session, so the engine takes nothing for it from the page. Press Release on the session to keep the newer one.")
		}
		return nil, conflict("offline", "The session isn't connected to the engine now. Wait for it to reconnect, or wake it.")
	}
	a := r.admitted[sid]
	switch {
	case a == nil:
		return nil, conflict("unverified", "The engine didn't start this session, so it takes nothing for it from the page.")
	case !a.running:
		return nil, conflict("not running", "The session isn't running. Wake it first.")
	case c.protocol < MinProtocol:
		return nil, conflict("outdated", "The session runs an older plugin than this engine takes. Stop it and wake it again.")
	case needControl && !c.controlled:
		return nil, conflict("read-only", "This session is read-only on this page.")
	}
	return c, nil
}

func (r *Registry) Replay(stream, sid, epoch string, since int64) error {
	if epoch != "" && !epochPattern.MatchString(epoch) {
		return invalid("epoch", "The page's place in the session's events isn't one the engine reads. Reload the page.")
	}
	if since < 0 || since > 1<<53 {
		return invalid("since", "The page's place in the session's events isn't one the engine reads. Reload the page.")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	c := r.only(sid, now)
	if r.sinks[stream] == nil || c == nil {
		return conflict("offline", "The page's stream or the session isn't connected now, so nothing was sent again. Reload the page.")
	}
	key := sid + "\x00" + stream
	msg := Fields{"kind": "replay", "stream": stream, "epoch": epoch, "since": float64(since)}
	last := r.replays[key]
	if last == nil || !now.Before(last.at.Add(ReplayGap)) {
		r.replays[key] = &replay{at: now}
		r.tell(c, msg, now)
		return nil
	}
	last.next = msg
	return nil
}

const ShellLine = "The page doesn't run shell lines: it sends messages to the agent. Ask it to run the command, or run it yourself in the agent container."

func shellLine(text string) *Error {
	e := invalid("command", ShellLine)
	e.Line = strings.TrimSpace(strings.TrimPrefix(text, "!"))
	return e
}

func (r *Registry) Message(sid, text string, confirm bool, images, files []string) error {
	return r.message(sid, text, false, confirm, images, files)
}

func (r *Registry) PersonMessage(sid, text string, confirm bool, images, files []string) error {
	return r.message(sid, text, true, confirm, images, files)
}

func (r *Registry) message(sid, text string, typed, confirm bool, images, files []string) error {
	text = strings.TrimSpace(text)
	if why := r.limitsMessageHeld(sid); why != "" {
		return conflict("cap", why)
	}
	if held, err := r.workDivert(sid, text, len(images) > 0 || len(files) > 0, typed); held {
		return err
	}
	var first FirstStep
	if typed {
		first = r.firstStepFor(sid, text, len(images) > 0 || len(files) > 0)
	}
	used, err := r.sendTyped(sid, text, confirm, images, files, first)
	if first.Step != "" && used == "" {
		r.unclaimStep(sid, first.Step)
	}
	return err
}

func (r *Registry) sendTyped(sid, text string, confirm bool, images, files []string, first FirstStep) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	c, err := r.controllable(sid, true, now)
	if err != nil {
		if e, ok := errors.AsType[*Error](err); ok && e.Reason == "read-only" {
			return "", explained(e, "This session is read-only on this page.")
		}
		return "", err
	}
	attached := len(images) > 0 || len(files) > 0
	switch {
	case text == "" && !attached:
		return "", invalid("text", "Type a message first.")
	case utf16Len(text) > MaxText || !utf8.ValidString(text):
		return "", invalid("text", "A message holds at most 20,000 characters.")
	case strings.HasPrefix(text, "!"):
		return "", shellLine(text)
	case attached && commandName(text) != "":
		return "", invalid("command", "A command can't carry images or files. Send them in a message of their own.")
	case len(images) > 0 && r.uploads.noImage[sid]:
		return "", conflict("no images", noImages)
	}
	if name := commandName(text); name != "" && refusedName(name, r.catalogs[sid]) {
		return "", invalid("command", r.commandRefusedLocked(sid))
	}
	if c.mode == "bypassPermissions" && !confirm {
		return "", conflict("confirm", "This session runs with bypassPermissions: Claude runs every tool without asking. Confirm to send anyway.")
	}
	ims, err := r.takeUploads(sid, images, true)
	if err != nil {
		return "", err
	}
	fs, err := r.takeUploads(sid, files, false)
	if err != nil {
		return "", err
	}
	r.markSent(ims)
	r.markSent(fs)
	msg, _ := uploadFields(ims, fs)
	msg["kind"] = "prompt"
	q := r.queued[sid]
	if q != nil && len(q.msgs) >= maxQueued {
		return "", limited("queue", "Twenty messages already wait for this session to wake.")
	}
	sent, noted, step := r.stepTextLocked(sid, text, first)
	msg["text"] = sent
	asked := r.checkpointAsked(sid, text)
	if q != nil {
		q.msgs = append(q.msgs, msg)
		q.marked = withStep(q.marked, step)
		if noted {
			r.dropNotesLocked(sid)
		}
		r.announce(now)
		return step, nil
	}
	r.tell(c, msg, now)
	if noted {
		r.dropNotesLocked(sid)
	}
	if asked || step != "" {
		r.announce(now)
	}
	if commandName(text) == "" {
		r.limitsMessage(sid)
		r.startTurnLocked(sid, now)
	}
	return step, nil
}

func (r *Registry) Interrupt(sid string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	c, err := r.controllable(sid, true, now)
	if err != nil {
		return err
	}
	r.tell(c, Fields{"kind": "interrupt"}, now)
	return nil
}

func (r *Registry) Control(sid string, on bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	c, err := r.controllable(sid, false, now)
	if err != nil {
		return err
	}
	r.tell(c, Fields{"kind": "control", "on": on}, now)
	return nil
}

func (r *Registry) Answer(sid, id string, value any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	k := r.cards[id]
	if k == nil || k.sid != sid || k.answered {
		return conflict("not open", "That answer did not go through. It may already have been answered in the session itself.")
	}
	if _, err := r.controllable(sid, true, now); err != nil {
		return err
	}
	value = normalAnswer(k, value)
	if err := answerError(k, value); err != nil {
		return err
	}
	if k.kind == "permission" {
		r.halt.answered(sid, k, value, false)
	}
	if k.waiter != nil {
		w := k.waiter
		k.waiter = nil
		w.answer = value
		w.finish(waitDelivered)
		delete(r.cards, id)
	} else {
		k.answer = value
		k.answered = true
		k.at = now
	}
	r.announce(now)
	return nil
}

func (r *Registry) Release(sid string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	if len(r.liveFor(sid, now)) < 2 {
		return conflict("not disputed", "Only one connection holds this session, so there is nothing to release.")
	}
	for key, c := range r.conns {
		if c.sid != sid {
			continue
		}
		if c.poll != nil {
			c.poll.finish(waitRevoked)
			c.poll = nil
		}
		delete(r.conns, key)
	}
	r.announce(now)
	return nil
}

func (r *Registry) Tick() {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	r.sweep(now)
	for key, p := range r.replays {
		if p.next == nil {
			if now.Sub(p.at) > time.Minute {
				delete(r.replays, key)
			}
			continue
		}
		if now.Before(p.at.Add(ReplayGap)) {
			continue
		}
		sid, stream, _ := strings.Cut(key, "\x00")
		msg := p.next
		r.replays[key] = &replay{at: now}
		if target := r.only(sid, now); target != nil && r.sinks[stream] != nil {
			r.tell(target, msg, now)
		}
	}
	r.releaseQueued(now)
	r.sweepUploads(now)
	for _, c := range r.conns {
		r.deliver(c, now)
	}
	r.announce(now)
}

func (r *Registry) strangerConns() int {
	n := 0
	for _, c := range r.conns {
		if r.admitted[c.sid] == nil {
			n++
		}
	}
	return n
}

func (r *Registry) boxStrangers(box string) int {
	n := 0
	for _, c := range r.conns {
		if c.box == box && r.admitted[c.sid] == nil {
			n++
		}
	}
	return n
}

func (r *Registry) sweep(now time.Time) {
	present := map[string]bool{}
	for key, c := range r.conns {
		if !r.live(c, now) && now.Sub(c.seen) > ForgetConn {
			delete(r.conns, key)
			continue
		}
		present[c.sid] = true
	}
	for key, c := range r.conns {
		if r.live(c, now) {
			continue
		}
		for _, other := range r.conns {
			if other != c && other.sid == c.sid && other.order > c.order && r.live(other, now) && !r.disputed(c.sid, now) {
				delete(r.conns, key)
				break
			}
		}
	}
	for id, k := range r.cards {
		if (k.answered && now.Sub(k.at) > HoldAnswer) || !present[k.sid] {
			r.dropCard(id)
		}
	}
	for sid := range r.catalogs {
		if !present[sid] && r.admitted[sid] == nil {
			delete(r.catalogs, sid)
		}
	}
	for sid := range r.workflows {
		if !present[sid] && r.admitted[sid] == nil {
			delete(r.workflows, sid)
		}
	}
	if len(r.lastMode) > maxRemembered || len(r.lastGate) > maxRemembered {
		for sid := range r.lastMode {
			if !present[sid] && r.admitted[sid] == nil {
				delete(r.lastMode, sid)
				delete(r.lastGate, sid)
			}
		}
		for sid := range r.lastGate {
			if !present[sid] && r.admitted[sid] == nil {
				delete(r.lastGate, sid)
			}
		}
	}
}

func (r *Registry) Checking(given string) (string, error) {
	return r.checking(given, false)
}

func (r *Registry) ToolChecking(given string) (string, error) {
	return r.checking(given, true)
}

func (r *Registry) checking(given string, checkOnly bool) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	sid := ""
	if c := r.connOf(given, now); c != nil {
		sid = c.sid
	} else if ch, ok := r.checkerOf(given); ok && checkOnly {
		sid = ch.sid
	} else {
		return "", unauthorized()
	}
	if r.disputed(sid, now) {
		return "", toAgent(Conflict, "disputed")
	}
	return sid, nil
}

func (r *Registry) CheckOnly(sid, box string) string {
	if !ValidBox(box) {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if a := r.admitted[sid]; a == nil || !a.running || a.Box != box {
		return ""
	}
	r.dropCheckers(func(c checker) bool { return c.sid == sid })
	key := secret()
	r.checkers[key] = checker{sid: sid, box: box}
	return key
}

func (r *Registry) checkerOf(given string) (checker, bool) {
	if len(given) != 43 {
		return checker{}, false
	}
	c, ok := r.checkers[given]
	if !ok {
		return checker{}, false
	}
	if a := r.admitted[c.sid]; a == nil || !a.running || a.Box != c.box {
		return checker{}, false
	}
	return c, true
}

func (r *Registry) dropCheckers(drop func(checker) bool) {
	maps.DeleteFunc(r.checkers, func(_ string, c checker) bool { return drop(c) })
}

func (r *Registry) Leave(given string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.conns[given]
	if c == nil {
		return
	}
	if c.poll != nil {
		c.poll.finish(waitEmpty)
		c.poll = nil
	}
	c.seen = time.Time{}
	r.announce(r.now())
}
