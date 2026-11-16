package session

import "time"

const (
	compactNotes  = 3
	compactWindow = 10 * time.Minute
)

func (r *Registry) OnTurn(f func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.turned = f
}

func (r *Registry) OnCompacted(f func(sid string, queued bool)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.compacted = f
}

func (r *Registry) TurnSince(sid string) time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	if a := r.admitted[sid]; a != nil {
		return a.turnSince
	}
	return time.Time{}
}

func (r *Registry) markTurn(sid, kind string, ev map[string]any, now time.Time) {
	a := r.admitted[sid]
	if a == nil {
		return
	}
	switch kind {
	case "busy":
		r.startTurnLocked(sid, now)
	case "idle":
		r.endTurnLocked(a)
	case "compact":
		if state, _ := ev["state"].(string); state == "done" && r.compacted != nil {
			go r.compacted(sid, r.compactions.allow(sid, now))
		}
	}
}

func (r *Registry) startTurnLocked(sid string, now time.Time) {
	a := r.admitted[sid]
	if a == nil || !a.running || !a.turnSince.IsZero() {
		return
	}
	a.turnSince = now
	r.turnChangedLocked()
}

func (r *Registry) endTurnLocked(a *admitted) {
	if a.turnSince.IsZero() {
		return
	}
	a.turnSince = time.Time{}
	r.turnChangedLocked()
}

func (r *Registry) turnChangedLocked() {
	if r.turned == nil {
		return
	}
	if r.turnSaving {
		r.turnAgain = true
		return
	}
	r.turnSaving = true
	go r.saveTurns(r.turned)
}

func (r *Registry) saveTurns(save func()) {
	for {
		save()
		r.mu.Lock()
		if !r.turnAgain {
			r.turnSaving = false
			r.mu.Unlock()
			return
		}
		r.turnAgain = false
		r.mu.Unlock()
	}
}
