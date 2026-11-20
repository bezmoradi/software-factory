package session

import (
	"context"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	HaltOff       = ""
	HaltPaused    = "paused"
	HaltStopped   = "stopped"
	AuditHalt     = "halt"
	AuditAway     = "away"
	HaltedReason  = "Stop everything is on"
	haltFor       = 3 * time.Minute
	maxAway       = 1000
	resultOn      = "on"
	resultOff     = "off"
	resultRelease = "released"
)

type HaltState struct {
	Mode    string
	Since   time.Time
	AwayAll bool
	Away    []string
}

func ValidHaltMode(m string) bool {
	return m == HaltOff || m == HaltPaused || m == HaltStopped
}

type HaltStore interface {
	Load() (HaltState, error)
	Save(HaltState) error
}

type HaltStopper interface {
	StopEvery(ctx context.Context, why string)
	Kick()
}

type HaltConfig struct {
	Store   HaltStore
	Audit   Audit
	Targets func(sid string) (PushTarget, bool)
	Doubled func(sid string) bool
	Known   func(sid string) bool
	Stopper HaltStopper
	Resume  func(ctx context.Context) int
	Logger  shared.Logger
}

type Halt struct {
	mu      sync.Mutex
	saveMu  sync.Mutex
	cfg     HaltConfig
	mode    string
	since   time.Time
	awayAll bool
	away    map[string]bool
	now     func() time.Time
	notify  func(Fields)
}

func newHalt(now func() time.Time, notify func(Fields)) *Halt {
	return &Halt{cfg: HaltConfig{Logger: quiet{}}, away: map[string]bool{}, now: now, notify: notify}
}

func (r *Registry) Halt() *Halt { return r.halt }

func (h *Halt) Use(cfg HaltConfig) {
	if cfg.Logger == nil {
		cfg.Logger = quiet{}
	}
	cfg.Audit = masking(cfg.Audit)
	var st HaltState
	var err error
	if cfg.Store != nil {
		st, err = cfg.Store.Load()
	}
	h.mu.Lock()
	h.cfg = cfg
	if err != nil {
		h.mode, h.since = HaltStopped, h.now()
	} else {
		h.mode, h.since, h.awayAll = st.Mode, st.Since, st.AwayAll
		h.away = map[string]bool{}
		for _, sid := range st.Away {
			if sidPattern.MatchString(sid) && len(h.away) < maxAway {
				h.away[sid] = true
			}
		}
	}
	mode := h.mode
	h.mu.Unlock()
	if err != nil {
		cfg.Logger.Warn("halt", "event", "state-not-read", "mode", HaltStopped)
	} else if mode != HaltOff {
		cfg.Logger.Warn("halt", "event", "restored", "mode", mode)
	}
	h.show()
	if mode == HaltStopped {
		h.stopEvery(cfg)
	}
}

func (h *Halt) State() HaltState {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.stateLocked()
}

func (h *Halt) stateLocked() HaltState {
	return HaltState{Mode: h.mode, Since: h.since, AwayAll: h.awayAll, Away: slices.Sorted(maps.Keys(h.away))}
}

func (h *Halt) frame() Fields {
	st := h.State()
	since := 0.0
	if !st.Since.IsZero() {
		since = float64(st.Since.UnixMilli())
	}
	away := make([]any, 0, len(st.Away))
	for _, sid := range st.Away {
		away = append(away, sid)
	}
	return Fields{"type": "halt", "mode": st.Mode, "since": since, "awayAll": st.AwayAll, "away": away}
}

func (h *Halt) show() {
	if h.notify != nil {
		h.notify(h.frame())
	}
}

func (h *Halt) config() HaltConfig {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cfg
}

func (h *Halt) save() error {
	h.saveMu.Lock()
	defer h.saveMu.Unlock()
	cfg := h.config()
	if cfg.Store == nil {
		return nil
	}
	st := h.State()
	if err := cfg.Store.Save(st); err != nil {
		cfg.Logger.Warn("halt", "event", "state-not-saved")
		return explained(conflict("state"), "This is in effect now, but the engine couldn't save it, so a restart of the engine forgets it.")
	}
	return nil
}

func (h *Halt) audit(e AuditEntry) {
	cfg := h.config()
	if cfg.Audit == nil {
		return
	}
	e.Time = h.now()
	if e.By == "" {
		e.By = ByPage
	}
	if e.Session != "" && cfg.Targets != nil {
		if t, ok := cfg.Targets(e.Session); ok {
			e.Repository, e.Branch = t.Name, t.Branch
		}
	}
	if err := cfg.Audit.Append(e); err != nil {
		cfg.Logger.Warn("halt", "event", "audit-not-written", "kind", e.Kind)
	}
}

func (h *Halt) stopEvery(cfg HaltConfig) {
	if cfg.Stopper == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), haltFor)
		defer cancel()
		cfg.Stopper.StopEvery(ctx, HaltedReason)
	}()
}

func (h *Halt) StopAll() error {
	h.mu.Lock()
	if h.mode != HaltStopped {
		h.mode, h.since = HaltStopped, h.now()
	}
	cfg := h.cfg
	h.mu.Unlock()
	err := h.save()
	h.audit(AuditEntry{Kind: AuditHalt, Result: HaltStopped, Reason: "the developer pressed Stop everything: every running turn is interrupted and every session stopped, every push is held and no session starts or wakes until Release"})
	h.show()
	cfg.Logger.Warn("halt", "event", "stop-everything")
	h.stopEvery(cfg)
	return err
}

func (h *Halt) PauseAll() error {
	h.mu.Lock()
	switch h.mode {
	case HaltStopped:
		h.mu.Unlock()
		return explained(conflict("stopped"), "Stop everything is on, which already holds more than Pause all. Release it first.")
	case HaltPaused:
		h.mu.Unlock()
		return nil
	}
	h.mode, h.since = HaltPaused, h.now()
	cfg := h.cfg
	h.mu.Unlock()
	err := h.save()
	h.audit(AuditEntry{Kind: AuditHalt, Result: HaltPaused, Reason: "the developer pressed Pause all: no new turn starts and no automatic review or push runs until Release; running sessions keep running"})
	h.show()
	cfg.Logger.Warn("halt", "event", "pause-all")
	return err
}

func (h *Halt) Release() error {
	h.mu.Lock()
	was := h.mode
	if was == HaltOff {
		h.mu.Unlock()
		return explained(conflict("not on"), "Neither Stop everything nor Pause all is on.")
	}
	h.mode, h.since = HaltOff, time.Time{}
	cfg := h.cfg
	h.mu.Unlock()
	err := h.save()
	what := "Pause all"
	if was == HaltStopped {
		what = "Stop everything"
	}
	h.audit(AuditEntry{Kind: AuditHalt, Result: resultRelease, Reason: "the developer pressed Release, which ends " + what})
	h.show()
	cfg.Logger.Info("halt", "event", "released", "was", was)
	if cfg.Stopper != nil {
		cfg.Stopper.Kick()
	}
	if cfg.Resume != nil {
		go cfg.Resume(context.Background())
	}
	return err
}

func (h *Halt) SetAway(sid string, on bool) error {
	if sid != "" && !sidPattern.MatchString(sid) {
		return invalid("sid")
	}
	h.mu.Lock()
	doubled, known := h.cfg.Doubled, h.cfg.Known
	h.mu.Unlock()
	if on && sid != "" && known != nil && !known(sid) {
		return notStartedHere(doubled, sid, "Only a session this engine started can have away mode of its own.")
	}
	if err := heldBranch(doubled, sid); err != nil {
		return err
	}
	h.mu.Lock()
	changed := false
	switch {
	case sid == "":
		changed, h.awayAll = h.awayAll != on, on
	case on && !h.away[sid]:
		if len(h.away) >= maxAway {
			h.mu.Unlock()
			return explained(limited("away"), "Away mode is on for a thousand sessions already. Turn it on for every session instead.")
		}
		h.away[sid], changed = true, true
	case !on && h.away[sid]:
		delete(h.away, sid)
		changed = true
	}
	cfg := h.cfg
	h.mu.Unlock()
	if !changed {
		return nil
	}
	err := h.save()
	result, what := resultOff, "off"
	if on {
		result, what = resultOn, "on"
	}
	scope := "every session"
	if sid != "" {
		scope = "this session"
	}
	h.audit(AuditEntry{Session: sid, Kind: AuditAway, Result: result, Reason: "the developer turned away mode " + what + " for " + scope + ": while it is on, the engine denies each new permission card with the note \"" + AwayNote + "\"; issue, pull request and push cards still wait"})
	h.show()
	cfg.Logger.Info("halt", "event", "away", "on", on, "all", sid == "")
	return err
}

func (h *Halt) forget(sid string) {
	h.mu.Lock()
	_, had := h.away[sid]
	delete(h.away, sid)
	h.mu.Unlock()
	if had {
		_ = h.save()
		h.show()
	}
}

func (h *Halt) modeNow() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.mode
}

func (h *Halt) starts() string {
	if h.modeNow() == HaltStopped {
		return "Stop everything is on, so no session starts or wakes until you press Release at the top of the page."
	}
	return ""
}

func (h *Halt) turns() string {
	switch h.modeNow() {
	case HaltStopped:
		return "Stop everything is on, so no message reaches a session until you press Release at the top of the page."
	case HaltPaused:
		return "Pause all is on, so no new turn starts until you press Release at the top of the page."
	}
	return ""
}

func (h *Halt) automatic() string {
	switch h.modeNow() {
	case HaltStopped:
		return "Stop everything is on, so the engine starts no automatic review until the person presses Release on the page."
	case HaltPaused:
		return "Pause all is on, so the engine starts no automatic review until the person presses Release on the page."
	}
	return ""
}

func (h *Halt) pushes(by string) string {
	switch mode := h.modeNow(); {
	case mode == HaltStopped:
		return "Stop everything is on, so the engine pushes nothing until the person presses Release on the page"
	case mode == HaltPaused && by != ByPage:
		return "Pause all is on, so the engine decides no automatic push and no push an agent asks for until the person presses Release on the page; the person can still press Push"
	}
	return ""
}

func (h *Halt) awayFor(sid string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.awayAll || h.away[sid]
}
