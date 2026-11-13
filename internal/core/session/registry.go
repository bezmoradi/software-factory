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
	MaxStreams       = 2
	MaxInbox         = 200
	MaxText          = 20000
	MaxCwd           = 1000
	MaxRegisterCards = 50
	MaxUnverified    = 20
	MaxObservations  = 1000
	maxRemembered    = 1000
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
	keepPush   string
}

type Registered struct {
	Secret     string
	Push       string
	Version    string
	Protocol   int
	WasGate    bool
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
	SID          string
	Cwd          string
	Name         string
	ID           string
	Kind         string
	Agent        string
	Project      string
	Mode         string
	Epoch        string
	StartedAt    time.Time
	Verified     bool
	Running      bool
	Live         bool
	Disputed     bool
	Outdated     bool
	Controlled   bool
	Busy         bool
	Login        bool
	Claims       []string
	Needs        int
	LastSeq      int64
	Level        level.Level
	LevelReason  string
	LevelDropped bool
	Review       *ReviewView
	Push         *PushView
	Forge        []ForgeView
	SignIn       *SignIn
	State        string
	Status       string
	WaitingFor   string
	Queued       int
}

type conn struct {
	secret     string
	push       string
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
}

type admitted struct {
	Admission
	running      bool
	observations []level.Observation
	review       *ReviewView
	push         *PushView
	forge        []ForgeView
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
	cards       map[string]*card
	admitted    map[string]*admitted
	lastMode    map[string]string
	lastGate    map[string]bool
	catalogs    map[string]*catalog
	workflows   map[string]*Workflow
	sinks       map[string]Sink
	replays     map[string]*replay
	registerAll *limiter
	registerOne *limiter
	shown       []Info
	order       uint64
	uploads     *uploadBook
	queued      map[string]*queue
	notes       map[string]map[string]string
	states      map[string]AgentState
	supervisor  string
}

var (
	sidPattern     = regexp.MustCompile(`^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$`)
	versionPattern = regexp.MustCompile(`^\d{1,9}\.\d{1,9}\.\d{1,9}$`)
	epochPattern   = regexp.MustCompile(`^[0-9a-f]{16}$`)
	eventType      = regexp.MustCompile(`^[a-z][a-z_]{0,39}$`)
	streamPattern  = regexp.MustCompile(`^[0-9a-f]{24}$`)
	controlChars   = regexp.MustCompile(`[\x00-\x1f\x7f]`)
)

var engineFrames = map[string]bool{"hello": true, "sessions": true, "reset": true, "dropped": true, "omitted": true}

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
	return &Registry{
		now:         now,
		version:     version,
		conns:       map[string]*conn{},
		cards:       map[string]*card{},
		admitted:    map[string]*admitted{},
		lastMode:    map[string]string{},
		lastGate:    map[string]bool{},
		catalogs:    map[string]*catalog{},
		workflows:   map[string]*Workflow{},
		sinks:       map[string]Sink{},
		replays:     map[string]*replay{},
		registerAll: newLimiter(20, time.Minute),
		registerOne: newLimiter(3, time.Minute),
		uploads:     newUploadBook(),
		queued:      map[string]*queue{},
		states:      map[string]AgentState{},
	}
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
		return Registered{}, invalid("register")
	}
	if g.LastSeq < 0 || g.LastSeq > 1<<53 {
		g.LastSeq = 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	a := r.admitted[g.SID]
	if (a == nil || !a.running) && !r.registerAll.allow("all", now) {
		return Registered{}, limited("register")
	}
	if !r.registerOne.allow(g.SID, now) {
		return Registered{}, limited("register")
	}
	stranger := a == nil
	if len(r.conns) >= MaxConns || stranger && r.strangerConns() >= MaxStrangerConns {
		r.sweep(now)
	}
	if len(r.conns) >= MaxConns || stranger && r.strangerConns() >= MaxStrangerConns {
		return Registered{}, full("connections")
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
	}
	r.conns[c.secret] = c
	wasGate := r.lastGate[g.SID]
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
	out := Registered{Secret: c.secret, Push: c.push, Version: r.version, Protocol: Protocol, WasGate: wasGate, Page: len(r.sinks) > 0}
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
		return nil, conflict("disputed")
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
		return conflict("disputed")
	}
	var target Sink
	if b.Stream != "" {
		if !streamPattern.MatchString(b.Stream) {
			return invalid("stream")
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
		if kind == "workflow" {
			clean, ok := workflowOf(ev)
			if !ok {
				shown = false
			} else {
				ev = clean.fields(ev["seq"], ev["ts"])
				if target == nil && seq > c.lastSeq {
					w := clean
					r.workflows[c.sid] = &w
				}
			}
		}
		if target == nil {
			if seq <= c.lastSeq {
				continue
			}
			c.lastSeq = seq
			if cardTypes[kind] {
				shown = r.indexCard(c.sid, ev)
				changed = changed || shown
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
		return nil, false, notFound("card")
	}
	if r.only(c.sid, now) != c {
		r.mu.Unlock()
		return nil, false, conflict("disputed")
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
		return nil, false, &Error{Kind: Gone, Reason: "card"}
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

func (r *Registry) Admit(sid string, a Admission) {
	r.mu.Lock()
	defer r.mu.Unlock()
	known := r.admitted[sid]
	if known == nil {
		known = &admitted{}
		r.admitted[sid] = known
	}
	known.Admission = a
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
	r.announce(r.now())
}

func (r *Registry) Forget(sid string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.admitted[sid] == nil {
		return
	}
	delete(r.admitted, sid)
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
	delete(r.queued, sid)
	delete(r.notes, sid)
	delete(r.states, sid)
	delete(r.uploads.noImage, sid)
	r.dropUploads(sid)
	r.announce(r.now())
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
				return "", conflict("disputed")
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

func (r *Registry) Ended(sid string, lost bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.admitted[sid]
	if a == nil {
		return
	}
	a.running = false
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
		return "", limited("streams")
	}
	now := r.now()
	id := streamID()
	list := r.list(now)
	if !s.Hello(id, list) {
		s.Close()
		return "", full("stream")
	}
	if r.supervisor != "" && !s.Frame(Fields{"type": "supervisor", "state": r.supervisor}) {
		s.Close()
		return "", full("stream")
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
		if len(a.forge) > 0 {
			info.Forge = slices.Clone(a.forge)
			info.Needs += forgeWaiting(a.forge)
		}
		if st, ok := r.states[sid]; ok {
			info.State, info.Status, info.WaitingFor = st.State, st.Status, st.WaitingFor
		}
		if q := r.queued[sid]; q != nil {
			info.Queued = len(q.msgs)
		}
	}
	for _, k := range r.cards {
		if k.sid == sid {
			info.Needs++
		}
	}
	lv := level.Of(observations)
	info.Level = lv.Level
	info.LevelReason = lv.Reason
	info.LevelDropped = lv.Dropped
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
			return nil, conflict("disputed")
		}
		return nil, conflict("offline")
	}
	a := r.admitted[sid]
	switch {
	case a == nil:
		return nil, conflict("unverified")
	case !a.running:
		return nil, conflict("not running")
	case c.protocol < MinProtocol:
		return nil, conflict("outdated")
	case needControl && !c.controlled:
		return nil, conflict("read-only")
	}
	return c, nil
}

func (r *Registry) Replay(stream, sid, epoch string, since int64) error {
	if epoch != "" && !epochPattern.MatchString(epoch) {
		return invalid("epoch")
	}
	if since < 0 || since > 1<<53 {
		return invalid("since")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	c := r.only(sid, now)
	if r.sinks[stream] == nil || c == nil {
		return conflict("offline")
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

func (r *Registry) Message(sid, text string, confirm bool, images, files []string) error {
	text = strings.TrimSpace(text)
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	c, err := r.controllable(sid, true, now)
	if err != nil {
		return err
	}
	attached := len(images) > 0 || len(files) > 0
	switch {
	case (text == "" && !attached) || utf16Len(text) > MaxText || !utf8.ValidString(text):
		return invalid("text")
	case strings.HasPrefix(text, "!"):
		return invalid("command")
	case attached && commandName(text) != "":
		return explained(invalid("command"), "A command can't carry images or files. Send them in a message of their own.")
	case len(images) > 0 && r.uploads.noImage[sid]:
		return explained(conflict("no images"), noImages)
	}
	if name := commandName(text); name != "" && refusedName(name, r.catalogs[sid]) {
		return explained(invalid("command"), commandRefused)
	}
	if c.mode == "bypassPermissions" && !confirm {
		return conflict("confirm")
	}
	ims, err := r.takeUploads(sid, images, true)
	if err != nil {
		return err
	}
	fs, err := r.takeUploads(sid, files, false)
	if err != nil {
		return err
	}
	r.markSent(ims)
	r.markSent(fs)
	msg, _ := uploadFields(ims, fs)
	msg["kind"] = "prompt"
	sent, noted := r.noted(sid, text)
	msg["text"] = sent
	if q := r.queued[sid]; q != nil {
		if len(q.msgs) >= maxQueued {
			return explained(limited("queue"), "Twenty messages already wait for this session to wake.")
		}
		q.msgs = append(q.msgs, msg)
		if noted {
			delete(r.notes, sid)
		}
		r.announce(now)
		return nil
	}
	r.tell(c, msg, now)
	if noted {
		delete(r.notes, sid)
	}
	return nil
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
		return conflict("not open")
	}
	if _, err := r.controllable(sid, true, now); err != nil {
		return err
	}
	if !validAnswer(k, value) {
		return invalid("answer")
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
		return conflict("not disputed")
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
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	c := r.connOf(given, now)
	if c == nil {
		return "", unauthorized()
	}
	if r.disputed(c.sid, now) {
		return "", conflict("disputed")
	}
	return c.sid, nil
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
