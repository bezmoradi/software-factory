package session

import (
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	AuditAway = "away"
	maxAway   = 1000
	resultOn  = "on"
	resultOff = "off"
)

type HaltState struct {
	AwayAll bool
	Away    []string
}

type HaltStore interface {
	Load() (HaltState, error)
	Save(HaltState) error
}

type HaltConfig struct {
	Store   HaltStore
	Audit   Audit
	Targets func(sid string) (PushTarget, bool)
	Doubled func(sid string) bool
	Known   func(sid string) bool
	Logger  shared.Logger
}

type Halt struct {
	mu      sync.Mutex
	saveMu  sync.Mutex
	cfg     HaltConfig
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
	if err == nil {
		h.awayAll = st.AwayAll
		h.away = map[string]bool{}
		for _, sid := range st.Away {
			if sidPattern.MatchString(sid) && len(h.away) < maxAway {
				h.away[sid] = true
			}
		}
	}
	h.mu.Unlock()
	if err != nil {
		cfg.Logger.Warn("halt", "event", "state-not-read")
	}
	h.show()
}

func (h *Halt) State() HaltState {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.stateLocked()
}

func (h *Halt) stateLocked() HaltState {
	return HaltState{AwayAll: h.awayAll, Away: slices.Sorted(maps.Keys(h.away))}
}

func (h *Halt) frame() Fields {
	st := h.State()
	away := make([]any, 0, len(st.Away))
	for _, sid := range st.Away {
		away = append(away, sid)
	}
	return Fields{"type": "halt", "awayAll": st.AwayAll, "away": away}
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
		return conflict("state", "This is in effect now, but the engine couldn't save it, so a restart of the engine forgets it.")
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

func (h *Halt) SetAway(sid string, on bool) error {
	if sid != "" && !sidPattern.MatchString(sid) {
		return invalid("sid", "The engine knows no session by that id. Reload the page and pick the session again.")
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
			return limited("away", "Away mode is on for a thousand sessions already. Turn it on for every session instead.")
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

func (h *Halt) awayFor(sid string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.awayAll || h.away[sid]
}
