package session

import (
	"context"
	"maps"
	"math"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	CapMoney         = "money"
	CapTime          = "time"
	CapTurns         = "turns"
	CapDisk          = "disk"
	AuditCapReached  = "cap-reached"
	AuditCapAnswer   = "cap-answer"
	AuditUsageAnswer = "usage-answer"
	CapRaised        = "raised"
	CapStopped       = "stopped"
	CapFreed         = "freed"
	MaxCapUSD        = 100000.0
	MaxCapMinutes    = 30 * 24 * 60
	MaxCapTurns      = 10000
	MaxCapDiskGiB    = 4096
	MaxCapSessions   = 1000
	MaxCapProjects   = 1000
	maxTurnCredits   = 20
	limitsTick       = time.Second
	raiseTimeMargin  = time.Minute
	limitsSaveEvery  = 30 * time.Second
	capStopFor       = 2 * time.Minute
	capWarnLow       = 0.75
	capWarnHigh      = 0.9
)

type Caps struct {
	MoneyUSD float64
	Minutes  int
	Turns    int
	DiskGiB  int
}

func (c Caps) Any() bool { return c.MoneyUSD > 0 || c.Minutes > 0 || c.Turns > 0 || c.DiskGiB > 0 }

func CheckCaps(c Caps) (Caps, bool) {
	clean, fault := CheckCapsFault(c)
	return clean, fault == ""
}

func CheckCapsFault(c Caps) (Caps, string) {
	if math.IsNaN(c.MoneyUSD) || math.IsInf(c.MoneyUSD, 0) || c.MoneyUSD < 0 || c.MoneyUSD > MaxCapUSD {
		return Caps{}, CapMoney
	}
	if c.Minutes < 0 || c.Minutes > MaxCapMinutes {
		return Caps{}, CapTime
	}
	if c.Turns < 0 || c.Turns > MaxCapTurns {
		return Caps{}, CapTurns
	}
	if c.DiskGiB < 0 || c.DiskGiB > MaxCapDiskGiB {
		return Caps{}, CapDisk
	}
	c.MoneyUSD = math.Round(c.MoneyUSD*100) / 100
	return c, ""
}

func ValidCapKind(k string) bool { return slices.Contains(capKinds, k) }

func CapRefusal(kind string) string {
	switch kind {
	case CapMoney:
		return "Needs 0 to 100,000 US dollars; 0 or empty is off."
	case CapTime:
		return "Needs whole minutes, 0 to 43,200; 0 or empty is off."
	case CapTurns:
		return "Needs whole turns, 0 to 10,000; 0 or empty is off."
	case CapDisk:
		return "Needs whole GiB, 0 to 4,096; 0 or empty is off."
	}
	return "Each cap is a number from 0 up to $100,000, 43,200 minutes, 10,000 turns or 4,096 GiB; minutes, turns and GiB are whole numbers."
}

var capKinds = []string{CapMoney, CapTime, CapTurns, CapDisk}

type CapsUsed struct {
	CostUSD   float64
	Time      time.Duration
	Turns     int
	DiskBytes int64
	DiskMore  bool
	DiskAt    time.Time
}

type CapsSession struct {
	SID       string
	Project   string
	Own       *Caps
	Used      CapsUsed
	Reached   string
	ReachedAt time.Time
}

type CapsState struct {
	Projects map[string]Caps
	Removed  []RemovedProject[Caps]
	Sessions []CapsSession
	Tokens   map[string]time.Time
}

type CapsStore interface {
	Load() (CapsState, error)
	Save(CapsState) error
}

type LimitsConfig struct {
	Store     CapsStore
	Audit     Audit
	Targets   func(sid string) (PushTarget, bool)
	Doubled   func(sid string) bool
	Stop      func(ctx context.Context, sid string) error
	Wake      func(ctx context.Context, sid string) error
	Reviewer  func() (SignIn, bool)
	Continued func(ctx context.Context, sid string, findings bool)
	Disk      func(ctx context.Context, box string) (TreeSize, error)
	Logger    shared.Logger
}

type capSession struct {
	project   string
	own       *Caps
	used      CapsUsed
	reached   string
	reachedAt time.Time
	credits   int
	costEpoch string
	costLast  float64
	diskTried time.Time
	diskSoon  bool
}

type limitsSession struct {
	sid     string
	project string
	agent   string
	box     string
	running bool
	signIn  *SignIn
}

type Limits struct {
	reg            *Registry
	now            func() time.Time
	mu             sync.Mutex
	saveMu         sync.Mutex
	cfg            LimitsConfig
	started        bool
	loaded         bool
	pending        pendingSteps
	projects       map[string]Caps
	removed        []RemovedProject[Caps]
	sessions       map[string]*capSession
	known          map[string]limitsSession
	reviewerSignIn *SignIn
	meter          map[meterKey]*meterSide
	acks           map[usageAck]time.Time
	paused         []usagePause
	loops          map[string]*loopTrack
	tokens         map[string]time.Time
	wakes          map[string]time.Time
	lastTick       time.Time
	lastSave       time.Time
	unsaved        bool
	shown          Fields
	diskRound      bool
	diskTurn       chan struct{}
}

func newLimits(r *Registry) *Limits {
	return &Limits{
		reg: r, now: r.now, cfg: LimitsConfig{Logger: quiet{}},
		projects: map[string]Caps{}, sessions: map[string]*capSession{}, known: map[string]limitsSession{},
		meter: map[meterKey]*meterSide{}, acks: map[usageAck]time.Time{}, loops: map[string]*loopTrack{},
		tokens: map[string]time.Time{}, wakes: map[string]time.Time{}, diskTurn: make(chan struct{}, 1),
	}
}

func (r *Registry) Limits() *Limits { return r.limits }

func (l *Limits) Use(cfg LimitsConfig) {
	if cfg.Logger == nil {
		cfg.Logger = quiet{}
	}
	cfg.Audit = masking(cfg.Audit)
	var st CapsState
	var err error
	if cfg.Store != nil {
		st, err = cfg.Store.Load()
	}
	l.mu.Lock()
	l.cfg = cfg
	if err == nil {
		l.loadLocked(st)
	}
	l.loaded = err == nil
	start := !l.started
	l.started = true
	l.lastTick = l.now()
	l.lastSave = l.lastTick
	l.mu.Unlock()
	if err != nil {
		cfg.Logger.Warn("limits", "event", "state-not-read", "err", err.Error())
	}
	if start {
		go l.run()
	}
}

func (l *Limits) loadLocked(st CapsState) {
	for name, c := range st.Projects {
		if clean, ok := CheckCaps(c); ok && clean.Any() && name != "" && len(l.projects) < MaxCapProjects {
			l.projects[name] = clean
		}
	}
	l.removed = nil
	for _, r := range st.Removed {
		if clean, ok := CheckCaps(r.Data); ok && clean.Any() && r.ID.Name != "" && RoomForRemoved(l.removed, r.ID) {
			l.removed = append(l.removed, RemovedProject[Caps]{ID: r.ID, Data: clean})
		}
	}
	for _, s := range st.Sessions {
		if !sidPattern.MatchString(s.SID) || len(l.sessions) >= MaxCapSessions {
			continue
		}
		cs := &capSession{project: s.Project, used: cleanUsed(s.Used), reachedAt: s.ReachedAt}
		if ValidCapKind(s.Reached) {
			cs.reached = s.Reached
		}
		if s.Own != nil {
			if clean, ok := CheckCaps(*s.Own); ok {
				cs.own = &clean
			}
		}
		l.sessions[s.SID] = cs
	}
	for host, at := range st.Tokens {
		if CleanTokenHost(host) == host && host != "" && !at.IsZero() && len(l.tokens) < maxTokenHosts {
			l.tokens[host] = at
		}
	}
}

func cleanUsed(u CapsUsed) CapsUsed {
	if math.IsNaN(u.CostUSD) || math.IsInf(u.CostUSD, 0) || u.CostUSD < 0 {
		u.CostUSD = 0
	}
	u.CostUSD = min(u.CostUSD, 1e7)
	u.Time = max(u.Time, 0)
	u.Turns = max(u.Turns, 0)
	u.DiskBytes, u.DiskMore, u.DiskAt = 0, false, time.Time{}
	return u
}

func (l *Limits) stateLocked() CapsState {
	st := CapsState{Projects: map[string]Caps{}, Tokens: map[string]time.Time{}}
	maps.Copy(st.Projects, l.projects)
	st.Removed = slices.Clone(l.removed)
	for sid, s := range l.sessions {
		cs := CapsSession{SID: sid, Project: s.project, Used: s.used, Reached: s.reached, ReachedAt: s.reachedAt}
		if s.own != nil {
			own := *s.own
			cs.Own = &own
		}
		st.Sessions = append(st.Sessions, cs)
	}
	maps.Copy(st.Tokens, l.tokens)
	return st
}

const (
	capsUnreadTurn = "The engine couldn't read its caps (caps.json), so it counts every cap as reached and doesn't pass on new messages. Fix the file in the engine's state folder and restart the engine."
	capsUnreadHeld = "the engine couldn't read its caps (caps.json), so it counts every cap as reached and starts no automatic review and no automatic Send findings; the person can still press Review"
)

func (l *Limits) unreadLocked() bool {
	return l.cfg.Store != nil && !l.loaded
}

func (l *Limits) save() error {
	l.saveMu.Lock()
	defer l.saveMu.Unlock()
	l.mu.Lock()
	cfg := l.cfg
	if cfg.Store == nil || !l.loaded {
		l.mu.Unlock()
		return nil
	}
	st := l.stateLocked()
	l.unsaved = false
	l.lastSave = l.now()
	l.mu.Unlock()
	if err := cfg.Store.Save(st); err != nil {
		cfg.Logger.Warn("limits", "event", "state-not-saved", "err", err.Error())
		l.mu.Lock()
		l.unsaved = true
		l.mu.Unlock()
		return conflict("state", "This is in effect now, but the engine couldn't save it, so a restart of the engine forgets it.")
	}
	return nil
}

func (l *Limits) audit(e AuditEntry) {
	l.mu.Lock()
	cfg := l.cfg
	l.mu.Unlock()
	if cfg.Audit == nil {
		return
	}
	e.Time = l.now()
	if e.By == "" {
		e.By = ByPage
	}
	if e.Session != "" && cfg.Targets != nil {
		if t, ok := cfg.Targets(e.Session); ok {
			e.Repository, e.Branch = t.Name, t.Branch
		}
	}
	if err := cfg.Audit.Append(e); err != nil {
		cfg.Logger.Warn("limits", "event", "audit-not-written", "kind", e.Kind)
	}
}

func (l *Limits) sessionLocked(sid, project string) *capSession {
	s := l.sessions[sid]
	if s == nil {
		if len(l.sessions) >= MaxCapSessions {
			return nil
		}
		s = &capSession{project: project}
		l.sessions[sid] = s
	}
	if project != "" {
		s.project = project
	}
	return s
}

func (l *Limits) capsOfLocked(s *capSession) Caps {
	if s.own != nil {
		return *s.own
	}
	return l.projects[s.project]
}

func money(v float64) string {
	return "$" + strconv.FormatFloat(v, 'f', 2, 64)
}

func gib(n int64) string {
	return strconv.FormatFloat(float64(n)/(1<<30), 'f', 1, 64) + " GiB"
}

func capWords(kind string, c Caps) string {
	switch kind {
	case CapMoney:
		return "spending cap of " + money(c.MoneyUSD)
	case CapTime:
		return "time cap of " + formatMinutes(time.Duration(c.Minutes)*time.Minute)
	case CapTurns:
		return "turn cap of " + formatTurns(c.Turns)
	case CapDisk:
		return "disk cap of " + strconv.Itoa(c.DiskGiB) + " GiB"
	}
	return "cap"
}

func capValue(kind string, c Caps) float64 {
	switch kind {
	case CapMoney:
		return c.MoneyUSD
	case CapTime:
		return float64(c.Minutes)
	case CapTurns:
		return float64(c.Turns)
	case CapDisk:
		return float64(c.DiskGiB)
	}
	return 0
}

func usedValue(kind string, u CapsUsed) float64 {
	switch kind {
	case CapMoney:
		return u.CostUSD
	case CapTime:
		return u.Time.Minutes()
	case CapTurns:
		return float64(u.Turns)
	case CapDisk:
		return float64(u.DiskBytes) / (1 << 30)
	}
	return 0
}

func overCap(kind string, c Caps, u CapsUsed) bool {
	switch kind {
	case CapMoney:
		return c.MoneyUSD > 0 && u.CostUSD >= c.MoneyUSD
	case CapTime:
		return c.Minutes > 0 && u.Time >= time.Duration(c.Minutes)*time.Minute
	case CapTurns:
		return c.Turns > 0 && u.Turns > c.Turns
	case CapDisk:
		return c.DiskGiB > 0 && u.DiskBytes >= int64(c.DiskGiB)<<30
	}
	return false
}
