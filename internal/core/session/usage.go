package session

import (
	"math"
	"regexp"
	"slices"
	"strconv"
	"time"
)

const (
	SideCodex       = "codex"
	UsageNear       = 0.9
	maxWindows      = 8
	maxPauses       = 20
	windowAhead     = 60 * 24 * time.Hour
	windowBehind    = 24 * time.Hour
	wakeAfterReset  = time.Minute
	StatusRejected  = "rejected"
	usageWhatReview = "review"
	usageWhatSend   = "findings"
)

var (
	windowKind  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}(:[0-9]{1,6})?$`)
	planPattern = regexp.MustCompile(`^[a-z0-9_]{1,40}$`)
)

var windowStatus = map[string]bool{"": true, "allowed": true, "allowed_warning": true, StatusRejected: true}

var windowWords = map[string]string{
	"five_hour": "5-hour limit", "seven_day": "weekly limit", "seven_day_opus": "weekly Opus limit", "seven_day_sonnet": "weekly Sonnet limit",
	"seven_day_overage_included": "weekly Fable limit", "overage": "usage credit limit", "spend_limit": "spending limit",
}

var sideWords = map[string]string{SideAgents: "Claude Code in the agent container", SideReviewers: "Claude Code in the reviewer container", SideCodex: "Codex"}

type meterSide struct {
	plan    string
	windows []RateWindow
	at      time.Time
}

type usagePause struct {
	sid  string
	side string
	kind string
	what string
	at   time.Time
}

func CleanPlan(p string) string {
	p = clip(p, 41)
	if planPattern.MatchString(p) {
		return p
	}
	return ""
}

func sideOf(s string) string {
	switch s {
	case SideAgents, "agent":
		return SideAgents
	case SideReviewers, "reviewer", "review":
		return SideReviewers
	case SideCodex:
		return SideCodex
	}
	return ""
}

func cleanWindows(in []RateWindow, now time.Time) []RateWindow {
	out := make([]RateWindow, 0, min(len(in), maxWindows))
	seen := map[string]bool{}
	for _, w := range in {
		if len(out) >= maxWindows {
			break
		}
		if !windowKind.MatchString(w.Kind) || seen[w.Kind] || math.IsNaN(w.Utilization) || math.IsInf(w.Utilization, 0) || !windowStatus[w.Status] {
			continue
		}
		seen[w.Kind] = true
		w.Utilization = min(max(w.Utilization, 0), 10)
		if !w.ResetsAt.IsZero() && (w.ResetsAt.Before(now.Add(-windowBehind)) || w.ResetsAt.After(now.Add(windowAhead))) {
			w.ResetsAt = time.Time{}
		}
		out = append(out, w)
	}
	return out
}

func (l *Limits) Windows(side string, w []RateWindow) {
	side = sideOf(side)
	if side == "" {
		return
	}
	now := l.now()
	clean := cleanWindows(w, now)
	l.mu.Lock()
	m := l.meter[side]
	if m == nil {
		m = &meterSide{}
		l.meter[side] = m
	}
	m.windows, m.at = clean, now
	l.mu.Unlock()
}

func (l *Limits) CodexWindows(plan string, w []RateWindow) {
	now := l.now()
	clean := cleanWindows(w, now)
	l.mu.Lock()
	m := l.meter[SideCodex]
	if m == nil {
		m = &meterSide{}
		l.meter[SideCodex] = m
	}
	if p := CleanPlan(plan); p != "" {
		m.plan = p
	}
	if w != nil {
		m.windows, m.at = clean, now
	}
	l.mu.Unlock()
}

func pluginWindows(raw any, now time.Time) []RateWindow {
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := []RateWindow{}
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok || len(out) >= maxWindows {
			continue
		}
		kind, _ := m["kind"].(string)
		used, okUsed := m["percentUsed"].(float64)
		reset, _ := m["resetsAt"].(string)
		if !okUsed {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, clip(reset, 40))
		if err != nil {
			at = time.Time{}
		}
		out = append(out, RateWindow{Kind: kind, Utilization: used / 100, ResetsAt: at})
	}
	return cleanWindows(out, now)
}

func (l *Limits) sideShownLocked(side string, now time.Time) bool {
	switch side {
	case SideAgents:
		s := l.latestClaudeLocked()
		return s == nil || s.Method == SignInSubscription
	case SideReviewers:
		return l.reviewerSignIn == nil || l.reviewerSignIn.Method == SignInSubscription
	case SideCodex:
		var last *SignIn
		for _, k := range l.known {
			if k.agent == "codex" && k.signIn != nil && (last == nil || k.signIn.CheckedAt.After(last.CheckedAt)) {
				last = k.signIn
			}
		}
		return last == nil || last.Method == SignInSubscription
	}
	return true
}

func (l *Limits) latestClaudeLocked() *SignIn {
	var out *SignIn
	for _, k := range l.known {
		if k.signIn == nil || k.agent != "claude" && k.signIn.Plan == "" && k.signIn.ExpiresAt.IsZero() {
			continue
		}
		if out == nil || k.signIn.CheckedAt.After(out.CheckedAt) {
			out = k.signIn
		}
	}
	return out
}

func (l *Limits) planLocked(side string) string {
	switch side {
	case SideAgents:
		if s := l.latestClaudeLocked(); s != nil {
			return s.Plan
		}
	case SideReviewers:
		if l.reviewerSignIn != nil {
			return l.reviewerSignIn.Plan
		}
	}
	if m := l.meter[side]; m != nil {
		return m.plan
	}
	return ""
}

func (l *Limits) windowsLocked(side string, now time.Time) []RateWindow {
	m := l.meter[side]
	if m == nil || !l.sideShownLocked(side, now) {
		return nil
	}
	out := make([]RateWindow, 0, len(m.windows))
	for _, w := range m.windows {
		if !w.ResetsAt.IsZero() && !w.ResetsAt.After(now) {
			continue
		}
		out = append(out, w)
	}
	return out
}

func ackKey(side, kind string) string { return side + "\x00" + kind }

func (l *Limits) nearLocked(side string, now time.Time) (RateWindow, bool) {
	for _, w := range l.windowsLocked(side, now) {
		if w.Utilization < UsageNear && w.Status != StatusRejected {
			continue
		}
		if at, ok := l.acks[ackKey(side, w.Kind)]; ok && at.Equal(w.ResetsAt) {
			continue
		}
		return w, true
	}
	return RateWindow{}, false
}

func agentSide(agent string) string {
	switch agent {
	case "claude":
		return SideAgents
	case "codex":
		return SideCodex
	}
	return ""
}

func windowName(side string, w RateWindow) string {
	name := windowWords[w.Kind]
	if name == "" {
		base, mins, ok := cutMinutes(w.Kind)
		switch {
		case ok && mins > 0:
			name = base + " limit over " + formatMinutes(time.Duration(mins)*time.Minute)
		default:
			name = w.Kind + " limit"
		}
	}
	return sideWords[side] + "'s " + name
}

func cutMinutes(kind string) (string, int, bool) {
	for i := range len(kind) {
		if kind[i] == ':' {
			n, err := strconv.Atoi(kind[i+1:])
			return kind[:i], n, err == nil
		}
	}
	return kind, 0, false
}

func percent(u float64) string {
	return strconv.Itoa(int(math.Round(u*100))) + "%"
}

func (l *Limits) held(sid, what string) string {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if s := l.sessions[sid]; s != nil && s.reached != "" {
		return "this session reached its " + capWords(s.reached, l.capsOfLocked(s)) + ", so the engine starts no automatic review and no automatic Send findings for it until the person raises the cap on the page; the person can still press Review"
	}
	sides := []string{SideReviewers}
	if side := agentSide(l.known[sid].agent); side != "" {
		sides = append(sides, side)
	}
	for _, side := range sides {
		w, near := l.nearLocked(side, now)
		if !near {
			continue
		}
		l.notePauseLocked(usagePause{sid: sid, side: side, kind: w.Kind, what: what, at: now})
		return windowName(side, w) + " is " + percent(w.Utilization) + " used, so the engine starts no automatic review and no automatic Send findings until the person answers on the page; the person can still press Review"
	}
	return ""
}

func (l *Limits) notePauseLocked(p usagePause) {
	if n := len(l.paused); n > 0 {
		last := l.paused[n-1]
		if last.sid == p.sid && last.side == p.side && last.kind == p.kind && last.what == p.what && p.at.Sub(last.at) < time.Minute {
			return
		}
	}
	l.paused = append(l.paused, p)
	if len(l.paused) > maxPauses {
		l.paused = slices.Delete(l.paused, 0, len(l.paused)-maxPauses)
	}
	go l.flush(false)
}

func (d *Dispatcher) automaticHeld(sid string) string {
	return d.reg.limits.held(sid, usageWhatReview)
}

func (d *Dispatcher) automaticHeldIf(auto bool, sid string) string {
	if !auto {
		return ""
	}
	return d.automaticHeld(sid)
}

func (p *Pushes) UseLimits(l *Limits) {
	p.limits.Store(l)
}

func (p *Pushes) automaticHeld(sid string) string {
	l := p.limits.Load()
	if l == nil {
		return ""
	}
	return l.held(sid, usageWhatSend)
}

func (l *Limits) Continue(side, kind string) error {
	side = sideOf(side)
	now := l.now()
	l.mu.Lock()
	var found *RateWindow
	for _, w := range l.windowsLocked(side, now) {
		if w.Kind == kind && (w.Utilization >= UsageNear || w.Status == StatusRejected) {
			found = &w
		}
	}
	if found == nil {
		l.mu.Unlock()
		return explained(conflict("not near"), "That limit isn't near its end now, so there is nothing to answer.")
	}
	l.acks[ackKey(side, kind)] = found.ResetsAt
	name := windowName(side, *found)
	lifted := l.liftLocked(side, kind)
	continued := l.cfg.Continued
	l.mu.Unlock()
	l.audit(AuditEntry{Kind: AuditUsageAnswer, Result: "continued", Reason: "the person chose to keep automatic reviews and automatic Send findings running although " + name + " is " + percent(found.Utilization) + " used; this holds until the limit resets"})
	l.flush(true)
	if continued != nil {
		for _, p := range lifted {
			go continued(p.sid, p.what == usageWhatSend)
		}
	}
	return nil
}

func (l *Limits) liftLocked(side, kind string) []usagePause {
	var lifted []usagePause
	l.paused = slices.DeleteFunc(l.paused, func(p usagePause) bool {
		if p.side != side || p.kind != kind {
			return false
		}
		if !slices.ContainsFunc(lifted, func(x usagePause) bool { return x.sid == p.sid && x.what == p.what }) {
			lifted = append(lifted, p)
		}
		return true
	})
	return lifted
}

func (p *Pushes) Continued(sid string, findings bool) {
	t, ok := p.cfg.Targets(sid)
	if !ok {
		return
	}
	head, err := p.cfg.Heads.Head(t.Repo, t.Branch)
	if err != nil {
		return
	}
	halted := p.halted(ByReviews) != ""
	p.mu.Lock()
	h := p.held[sid]
	ready := !p.closing && h != nil && h.commit == head && h.branch == t.Branch && p.refused[sid] != head
	send := ready && findings && !halted
	if send {
		p.wg.Add(1)
	}
	p.mu.Unlock()
	switch {
	case !ready:
		p.cfg.Logger.Info("push", "event", "continued", "then", "nothing-held")
	case send:
		defer p.wg.Done()
		p.cfg.Logger.Info("push", "event", "continued", "then", "findings")
		p.autoFindings(sid)
	case findings:
		p.cfg.Logger.Info("push", "event", "continued", "then", "held-by-halt")
	case halted:
		p.wait(sid, head)
		p.cfg.Logger.Info("push", "event", "continued", "then", "held-by-halt")
	default:
		p.cfg.Logger.Info("push", "event", "continued", "then", "review")
		p.queueReview(sid, head)
	}
}

func (l *Limits) wakeOfferLocked(sid string, now time.Time) time.Time {
	side := agentSide(l.known[sid].agent)
	if side == "" {
		return time.Time{}
	}
	var at time.Time
	for _, w := range l.windowsLocked(side, now) {
		if (w.Utilization >= 1 || w.Status == StatusRejected) && !w.ResetsAt.IsZero() && w.ResetsAt.After(at) {
			at = w.ResetsAt
		}
	}
	return at
}

func (l *Limits) WakeAtReset(sid string, on bool) error {
	if !sidPattern.MatchString(sid) {
		return invalid("sid")
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if !on {
		delete(l.wakes, sid)
		go l.flush(true)
		return nil
	}
	if _, ok := l.known[sid]; !ok {
		return explained(conflict("not started here"), "Only a session this engine started can be woken at the reset.")
	}
	at := l.wakeOfferLocked(sid, now)
	if at.IsZero() {
		return explained(conflict("no limit"), "This session's agent hasn't reported a usage limit that is used up, so there is no reset to wait for.")
	}
	l.wakes[sid] = at.Add(wakeAfterReset)
	go l.flush(true)
	return nil
}

func (l *Limits) dueWakesLocked(now time.Time) []string {
	var due []string
	for sid, at := range l.wakes {
		if !now.Before(at) {
			due = append(due, sid)
			delete(l.wakes, sid)
		}
	}
	return due
}

func (l *Limits) dropStaleLocked(now time.Time) {
	for key, at := range l.acks {
		if !at.IsZero() && !at.After(now) {
			delete(l.acks, key)
		}
	}
}
