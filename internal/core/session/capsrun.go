package session

import (
	"context"
	"math"
	"reflect"
	"time"
)

const capContinueText = "The usage limit has reset. Continue where you stopped."

func (r *Registry) limitsSeen(c *conn, kind string, ev map[string]any, now time.Time) {
	a := r.admitted[c.sid]
	if a == nil {
		return
	}
	l := r.limits
	switch kind {
	case "busy":
		l.turnBegan(c.sid, a.Project, a.running && a.turnSince.IsZero())
	case "tool":
		l.loopTool(c.sid, ev, now)
	case "tool_done":
		l.loopDone(c.sid, ev, now)
	case "usage":
		if cost, ok := ev["costUsd"].(float64); ok {
			l.costSeen(c.sid, a.Project, c.epoch, cost)
		}
		if raw, ok := ev["rateLimits"]; ok && a.Agent == "claude" && a.SignIn != nil && a.SignIn.Method == SignInSubscription {
			l.Windows(SideAgents, pluginWindows(raw, now))
		}
	}
}

func (r *Registry) limitsMessageHeld(sid string) string {
	r.mu.Lock()
	project := ""
	if a := r.admitted[sid]; a != nil {
		project = a.Project
	}
	r.mu.Unlock()
	return r.limits.beforeTurn(sid, project)
}

func (r *Registry) limitsMessage(sid string) {
	a := r.admitted[sid]
	if a == nil {
		return
	}
	r.limits.personTurn(sid, a.Project, a.turnSince.IsZero())
}

func (l *Limits) beforeTurn(sid, project string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.sessions[sid]
	if s == nil {
		if !l.projects[project].Any() {
			return ""
		}
		s = l.sessionLocked(sid, project)
		if s == nil {
			return ""
		}
	}
	c := l.capsOfLocked(s)
	if s.reached == "" && c.Turns > 0 && s.used.Turns >= c.Turns {
		l.reachLocked(sid, s, CapTurns, c)
	}
	if s.reached != "" {
		return "This session reached its " + capWords(s.reached, l.capsOfLocked(s)) + ", so the engine doesn't pass on new messages. Press Raise the cap on the session to go on, or Stop."
	}
	return ""
}

func (l *Limits) personTurn(sid, project string, fresh bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.sessionLocked(sid, project)
	if s == nil {
		return
	}
	s.used.Turns++
	if !fresh && s.credits < maxTurnCredits {
		s.credits++
	}
	l.unsaved = true
	l.checkLocked(sid, s)
}

func (l *Limits) turnBegan(sid, project string, fresh bool) {
	l.loopTurn(sid)
	if !fresh {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.sessionLocked(sid, project)
	if s == nil {
		return
	}
	if s.credits > 0 {
		s.credits--
	} else {
		s.used.Turns++
		l.unsaved = true
	}
	if !l.checkLocked(sid, s) && s.reached != "" {
		go l.interrupt(sid)
	}
}

func (l *Limits) costSeen(sid, project, epoch string, cost float64) {
	if math.IsNaN(cost) || math.IsInf(cost, 0) || cost < 0 || cost >= 1e7 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.sessionLocked(sid, project)
	if s == nil {
		return
	}
	add := cost
	if s.costEpoch == epoch && cost >= s.costLast {
		add = cost - s.costLast
	}
	s.costEpoch, s.costLast = epoch, cost
	if add <= 0 {
		return
	}
	s.used.CostUSD = min(s.used.CostUSD+add, 1e7)
	l.unsaved = true
	l.checkLocked(sid, s)
}

func (l *Limits) checkLocked(sid string, s *capSession) bool {
	if s.reached != "" {
		return false
	}
	c := l.capsOfLocked(s)
	for _, kind := range []string{CapMoney, CapTime, CapTurns} {
		if overCap(kind, c, s.used) {
			l.reachLocked(sid, s, kind, c)
			return true
		}
	}
	return false
}

func (l *Limits) reachLocked(sid string, s *capSession, kind string, c Caps) {
	s.reached, s.reachedAt = kind, l.now()
	l.unsaved = true
	reason := "the session reached its " + capWords(kind, c) + " (" + usedWords(kind, s.used) + " used), so the engine interrupted its turn, holds its automatic reviews and automatic Send findings, and passes on no message until the person raises the cap or stops the session"
	go l.reached(sid, kind, reason)
}

func usedWords(kind string, u CapsUsed) string {
	switch kind {
	case CapMoney:
		return money(u.CostUSD) + " reported by the agent"
	case CapTime:
		return formatElapsed(u.Time) + " of running time by the engine's clock"
	}
	return formatTurns(u.Turns)
}

func (l *Limits) reached(sid, kind, reason string) {
	l.interrupt(sid)
	l.audit(AuditEntry{Session: sid, Kind: AuditCapReached, Result: kind, By: ByEngine, Reason: reason})
	l.mu.Lock()
	logger := l.cfg.Logger
	l.mu.Unlock()
	logger.Warn("limits", "event", "cap-reached", "cap", kind)
	_ = l.save()
	l.flush(true)
}

func (l *Limits) interrupt(sid string) {
	_ = l.reg.Interrupt(sid)
}

func (r *Registry) limitsSnapshot() map[string]limitsSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]limitsSession, len(r.admitted))
	for sid, a := range r.admitted {
		var s *SignIn
		if a.SignIn != nil {
			v := *a.SignIn
			s = &v
		}
		out[sid] = limitsSession{sid: sid, project: a.Project, agent: a.Agent, running: a.running, signIn: s}
	}
	return out
}

func (l *Limits) run() {
	t := time.NewTicker(limitsTick)
	defer t.Stop()
	for range t.C {
		l.tick()
	}
}

func (l *Limits) tick() {
	known := l.reg.limitsSnapshot()
	var reviewer *SignIn
	l.mu.Lock()
	get := l.cfg.Reviewer
	l.mu.Unlock()
	if get != nil {
		if s, ok := get(); ok {
			reviewer = &s
		}
	}
	now := l.now()
	l.mu.Lock()
	elapsed := now.Sub(l.lastTick)
	if elapsed < 0 || elapsed > time.Minute {
		elapsed = limitsTick
	}
	l.lastTick = now
	l.known = known
	l.reviewerSignIn = reviewer
	for sid, k := range known {
		if !k.running {
			continue
		}
		s := l.sessionLocked(sid, k.project)
		if s == nil {
			continue
		}
		s.used.Time += elapsed
		l.checkLocked(sid, s)
	}
	for sid := range l.sessions {
		if _, ok := known[sid]; !ok {
			delete(l.sessions, sid)
			delete(l.loops, sid)
			delete(l.wakes, sid)
			l.unsaved = true
		}
	}
	l.dropStaleLocked(now)
	due := l.dueWakesLocked(now)
	save := l.unsaved && now.Sub(l.lastSave) >= limitsSaveEvery
	l.mu.Unlock()
	for _, sid := range due {
		go l.wakeNow(sid, known[sid].running)
	}
	if save {
		_ = l.save()
	}
	l.flush(false)
}

func (l *Limits) flush(force bool) {
	f := l.Frame()
	l.mu.Lock()
	same := !force && reflect.DeepEqual(f, l.shown)
	if !same {
		l.shown = f
	}
	l.mu.Unlock()
	if !same {
		l.reg.Notify(f)
	}
}

func (l *Limits) wakeNow(sid string, running bool) {
	l.mu.Lock()
	cfg := l.cfg
	l.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), capStopFor)
	defer cancel()
	var err error
	switch {
	case running:
		err = l.reg.Message(sid, capContinueText, false, nil, nil)
	case cfg.Wake != nil:
		err = cfg.Wake(ctx, sid)
	}
	if err != nil {
		cfg.Logger.Info("limits", "event", "wake-at-reset-failed")
		return
	}
	cfg.Logger.Info("limits", "event", "woken-at-reset")
}
