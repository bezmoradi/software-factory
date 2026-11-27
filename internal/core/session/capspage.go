package session

import (
	"context"
	"errors"
	"math"
	"slices"
	"strconv"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

func formatMinutes(d time.Duration) string {
	m := int(d / time.Minute)
	if m == 1 {
		return "1 minute"
	}
	return strconv.Itoa(m) + " minutes"
}

func formatElapsed(d time.Duration) string {
	if d < time.Minute {
		return "less than a minute"
	}
	out := formatMinutes(d)
	switch sec := int(d % time.Minute / time.Second); sec {
	case 0:
	case 1:
		out += " 1 second"
	default:
		out += " " + strconv.Itoa(sec) + " seconds"
	}
	return out
}

func formatTurns(n int) string {
	if n == 1 {
		return "1 turn"
	}
	return strconv.Itoa(n) + " turns"
}

func ms(t time.Time) float64 {
	if t.IsZero() {
		return 0
	}
	return float64(t.UnixMilli())
}

func capsFields(c Caps) Fields {
	return Fields{"money": c.MoneyUSD, "minutes": float64(c.Minutes), "turns": float64(c.Turns), "disk": float64(c.DiskGiB)}
}

const (
	capLevelLow  = "warn75"
	capLevelHigh = "warn90"
)

func capLevel(kind string, c Caps, u CapsUsed) string {
	limit := capValue(kind, c)
	if limit <= 0 {
		return ""
	}
	share := usedValue(kind, u) / limit
	switch {
	case share >= 1:
		return "reached"
	case share >= capWarnHigh:
		return capLevelHigh
	case share >= capWarnLow:
		return capLevelLow
	}
	return ""
}

func (l *Limits) Frame() Fields {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	sids := make([]string, 0, len(l.known))
	for sid := range l.known {
		sids = append(sids, sid)
	}
	slices.Sort(sids)
	meter := []any{}
	addMeter := func(side, box, sid string) {
		list := l.windowsLocked(side, box, now)
		if len(list) == 0 {
			return
		}
		windows := make([]any, 0, len(list))
		for _, w := range list {
			at, acked := l.acks[usageAck{side: side, box: box, kind: w.Kind}]
			continued := acked && at.Equal(w.ResetsAt)
			windows = append(windows, Fields{"kind": w.Kind, "name": windowName(w), "short": windowShort(w), "utilization": math.Round(w.Utilization*1000) / 1000, "resetsAt": ms(w.ResetsAt), "status": w.Status,
				"near": !continued && (w.Utilization >= UsageNear || w.Status == StatusRejected), "continued": continued})
		}
		meter = append(meter, Fields{"side": side, "sid": sid, "windows": windows, "at": ms(l.meter[meterKey{side: side, box: box}].at)})
	}
	addMeter(SideReviewers, "", "")
	for _, sid := range sids {
		if side, box := meterOf(l.known[sid]); side == SideAgents {
			addMeter(side, box, sid)
		}
	}
	sessions := []any{}
	for _, sid := range sids {
		s := l.sessions[sid]
		t := l.loops[sid]
		wakeAt, offer := l.wakes[sid], l.wakeOfferLocked(sid, now)
		ending := l.known[sid].signIn
		if !signInEnding(ending, now) {
			ending = nil
		}
		if (s == nil || !l.capsOfLocked(s).Any() && s.reached == "") && (t == nil || t.notice == nil) && wakeAt.IsZero() && offer.IsZero() && ending == nil {
			continue
		}
		f := Fields{"sid": sid, "wakeAt": ms(wakeAt), "wakeOffer": ms(offer), "stuck": nil, "signInFrom": 0.0, "signInUntil": 0.0}
		if ending != nil {
			f["signInFrom"], f["signInUntil"] = ms(ending.ExpiresAt.Add(-SignInEarlier)), ms(ending.ExpiresAt)
		}
		if s != nil {
			c := l.capsOfLocked(s)
			f["caps"] = capsFields(c)
			f["own"] = s.own != nil
			f["used"] = Fields{"costUsd": math.Round(s.used.CostUSD*10000) / 10000, "minutes": math.Floor(s.used.Time.Minutes()*10) / 10, "turns": float64(s.used.Turns), "diskBytes": float64(s.used.DiskBytes), "diskMore": s.used.DiskMore, "diskAt": ms(s.used.DiskAt)}
			f["levels"] = Fields{CapMoney: capLevel(CapMoney, c, s.used), CapTime: capLevel(CapTime, c, s.used), CapTurns: capLevel(CapTurns, c, s.used), CapDisk: capLevel(CapDisk, c, s.used)}
			f["reached"] = s.reached
			f["reachedAt"] = ms(s.reachedAt)
			if s.reached != "" {
				least, suggested := raiseFor(s.reached, c, s.used)
				f["raise"] = Fields{"min": least, "suggested": suggested}
			}
		}
		if t != nil && t.notice != nil {
			f["stuck"] = Fields{"tool": t.notice.tool, "detail": t.notice.detail, "count": float64(t.notice.count), "failing": t.notice.failing, "at": ms(t.notice.at)}
		}
		sessions = append(sessions, f)
	}
	projects := []any{}
	names := make([]string, 0, len(l.projects))
	for name := range l.projects {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		f := capsFields(l.projects[name])
		f["name"] = name
		projects = append(projects, f)
	}
	expiry := []any{}
	for _, e := range l.expiriesLocked(now) {
		expiry = append(expiry, Fields{"what": e.what, "side": e.side, "label": signInEndingWords[e.side], "host": e.host, "from": ms(e.from), "until": ms(e.until)})
	}
	slices.SortFunc(expiry, func(a, b any) int {
		return int(a.(Fields)["until"].(float64)/1000) - int(b.(Fields)["until"].(float64)/1000)
	})
	paused := []any{}
	for _, p := range l.paused {
		if w, near := l.nearLocked(p.side, p.box, now); !near || w.Kind != p.kind {
			continue
		}
		paused = append(paused, Fields{"sid": p.sid, "side": p.side, "kind": p.kind, "what": p.what, "at": ms(p.at)})
	}
	unread := l.unreadLocked()
	return Fields{"type": "limits", "usage": l.accountLocked(now), "meter": meter, "sessions": sessions, "projects": projects, "expiry": expiry, "paused": paused, "unread": unread}
}

var _ ProjectData = (*Limits)(nil)

func (l *Limits) projectData(event string, step *projectStep, fn func() bool) {
	l.mu.Lock()
	logger, store := l.cfg.Logger, l.cfg.Store
	if store == nil {
		l.mu.Unlock()
		return
	}
	late := !l.loaded
	if late {
		st, err := store.Load()
		if err != nil {
			name := ""
			if step != nil {
				name = step.id.Name
				l.pending.remember(step.id, step.restore, step.at)
			}
			l.mu.Unlock()
			projectDataNotSaved(logger, "limits", name, errors.New("caps.json wasn't read"))
			return
		}
		l.loadLocked(st)
		l.loaded = true
		logger.Info("limits", "event", "state-read-late")
	}
	steps := l.pending.list()
	l.removed, _ = replaySteps(steps, l.projects, l.removed)
	l.pending.done(len(steps))
	if !fn() && len(steps) == 0 && !late {
		l.mu.Unlock()
		return
	}
	l.mu.Unlock()
	if l.save() == nil {
		logger.Info("limits", "event", event)
	}
	l.flush(true)
}

func (l *Limits) ParkProject(id ProjectID) {
	l.projectData("caps-parked", &projectStep{id: id, at: l.now()}, func() bool {
		var parked bool
		l.removed, _, parked = parkProject(l.projects, l.removed, id)
		return parked
	})
}

func (l *Limits) RestoreProject(id ProjectID) {
	l.projectData("caps-restored", &projectStep{id: id, restore: true, at: l.now()}, func() bool {
		var changed bool
		l.removed, changed = restoreProject(l.projects, l.removed, id)
		return changed
	})
}

func (l *Limits) KeepProjects(names []string) {
	l.projectData("caps-unlisted-set-aside", nil, func() bool {
		var n int
		l.removed, n = keepProjects(l.projects, l.removed, names)
		return n > 0
	})
}

func (l *Limits) SetProject(name string, c Caps) (bool, error) {
	clean, ok := CheckCaps(c)
	if !ok || name == "" || len(name) > 400 {
		return false, invalid("caps", "A cap is a number from 0 (off) up to $100,000, 43,200 minutes, 10,000 turns or 4,096 GiB.")
	}
	l.mu.Lock()
	was, had := l.projects[name]
	switch {
	case was == clean:
		l.mu.Unlock()
		return false, nil
	case !clean.Any():
		delete(l.projects, name)
	case !had && len(l.projects) >= MaxCapProjects:
		l.mu.Unlock()
		return false, limited("projects", "The engine keeps caps for at most 1,000 projects.")
	default:
		l.projects[name] = clean
	}
	for sid, s := range l.sessions {
		if s.project == name {
			l.checkLocked(sid, s)
		}
	}
	l.mu.Unlock()
	err := l.save()
	l.flush(true)
	return true, err
}

func (l *Limits) SetSession(sid string, c Caps) (bool, error) {
	if !sidPattern.MatchString(sid) {
		return false, invalid("sid", "The engine knows no session by that id. Reload the page and pick the session again.")
	}
	if err := l.branchHeld(sid); err != nil {
		return false, err
	}
	clean, ok := CheckCaps(c)
	if !ok {
		return false, invalid("caps", "A cap is a number from 0 (off) up to $100,000, 43,200 minutes, 10,000 turns or 4,096 GiB.")
	}
	l.mu.Lock()
	k, known := l.known[sid]
	if !known {
		l.mu.Unlock()
		known = l.reg.admittedProject(sid, &k)
		l.mu.Lock()
		if _, seen := l.known[sid]; known && !seen {
			l.known[sid] = k
		}
	}
	if !known {
		l.mu.Unlock()
		return false, conflict("not started here", "Only a session this engine started can have caps.")
	}
	s := l.sessionLocked(sid, k.project)
	if s == nil {
		l.mu.Unlock()
		return false, limited("sessions", "The engine keeps caps for at most 1,000 sessions.")
	}
	if s.reached != "" {
		l.mu.Unlock()
		return false, conflict("reached", "This session is at a cap. Answer Raise the cap or Stop on the session first.")
	}
	changed := s.own == nil || *s.own != clean
	s.own = &clean
	l.checkLocked(sid, s)
	if changed {
		l.unsaved = true
	}
	measure := clean.DiskGiB > 0 && k.running && l.cfg.Disk != nil
	l.mu.Unlock()
	if measure {
		l.measureSoon(sid, k.box)
	}
	var err error
	if changed {
		err = l.save()
	}
	l.flush(true)
	return changed, err
}

func (r *Registry) admittedProject(sid string, out *limitsSession) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.admitted[sid]
	if a == nil {
		return false
	}
	*out = limitsSession{sid: sid, project: a.Project, agent: a.Agent, box: a.Box, running: a.running}
	return true
}

func (l *Limits) Raise(sid, kind string, value float64) error {
	if !sidPattern.MatchString(sid) {
		return invalid("sid", "The engine knows no session by that id. Reload the page and pick the session again.")
	}
	if err := l.branchHeld(sid); err != nil {
		return err
	}
	l.mu.Lock()
	s := l.sessions[sid]
	if s == nil || s.reached == "" || s.reached != kind {
		l.mu.Unlock()
		return conflict("not reached", "This session isn't at that cap, so there is nothing to raise.")
	}
	c := l.capsOfLocked(s)
	old := capWords(kind, c)
	next := c
	switch kind {
	case CapMoney:
		next.MoneyUSD = value
	case CapTime:
		next.Minutes = int(value)
	case CapTurns:
		next.Turns = int(value)
	case CapDisk:
		next.DiskGiB = int(value)
	}
	clean, ok := CheckCaps(next)
	if !ok || value != math.Trunc(value) && kind != CapMoney || overCap(kind, clean, s.used) || capValue(kind, clean) <= capValue(kind, c) {
		l.mu.Unlock()
		most := map[string]string{CapMoney: "$100,000", CapTime: "43,200 minutes", CapTurns: "10,000 turns", CapDisk: "4,096 whole GiB"}[kind]
		return onField("value", invalid("value", "Above the cap and what's used, up to "+most+"."))
	}
	if least := (s.used.Time + raiseTimeMargin + time.Minute - 1).Truncate(time.Minute); kind == CapTime && time.Duration(clean.Minutes)*time.Minute < least {
		l.mu.Unlock()
		return onField("value", invalid("value", "Needs at least "+formatMinutes(least)+"."))
	}
	s.own = &clean
	s.reached, s.reachedAt = "", time.Time{}
	l.unsaved = true
	l.checkLocked(sid, s)
	reason := "the person pressed Raise the cap: the " + old + " is now a " + capWords(kind, clean)
	k := l.known[sid]
	measure := kind == CapDisk && l.cfg.Disk != nil
	l.mu.Unlock()
	if measure {
		l.measureSoon(sid, k.box)
	}
	l.audit(AuditEntry{Session: sid, Kind: AuditCapAnswer, Result: kind, Verdict: CapRaised, Reason: reason})
	err := l.save()
	l.flush(true)
	return err
}

func (l *Limits) StopAtCap(ctx context.Context, sid string) error {
	if !sidPattern.MatchString(sid) {
		return invalid("sid", "The engine knows no session by that id. Reload the page and pick the session again.")
	}
	if err := l.branchHeld(sid); err != nil {
		return err
	}
	l.mu.Lock()
	s := l.sessions[sid]
	stop := l.cfg.Stop
	if s == nil || s.reached == "" {
		l.mu.Unlock()
		return conflict("not reached", "This session isn't at a cap.")
	}
	kind := s.reached
	words := capWords(kind, l.capsOfLocked(s))
	l.mu.Unlock()
	if stop == nil {
		return conflict("no agent-host", "This engine can't stop sessions.")
	}
	sctx, cancel := context.WithTimeout(ctx, capStopFor)
	defer cancel()
	if err := stop(sctx, sid); err != nil {
		if e, ok := errors.AsType[*Error](err); !ok || e.Reason != "not running" {
			return err
		}
	}
	until := "the person raises it"
	if kind == CapDisk {
		until = "the person raises it or the engine measures the session's folder under the cap, as Check again does"
	}
	l.audit(AuditEntry{Req: shared.RequestOf(ctx), Session: sid, Kind: AuditCapAnswer, Result: kind, Verdict: CapStopped, Reason: "the person pressed Stop at the " + words + ", so the engine stopped the session; it stays at the cap until " + until})
	l.flush(true)
	return nil
}

func (l *Limits) branchHeld(sid string) error {
	l.mu.Lock()
	doubled := l.cfg.Doubled
	l.mu.Unlock()
	return heldBranch(doubled, sid)
}

func raiseFor(kind string, c Caps, u CapsUsed) (float64, float64) {
	now := capValue(kind, c)
	var least float64
	switch kind {
	case CapMoney:
		least = max(now, math.Round(u.CostUSD*10000)/10000)
		double := now * 2
		if double == 0 {
			double = 1
		}
		return least, math.Round(double*100) / 100
	case CapTime:
		least = max(now+1, math.Ceil(math.Floor(u.Time.Minutes()*10)/10)+1)
	case CapTurns:
		least = max(now+1, float64(u.Turns))
	case CapDisk:
		least = max(now+1, math.Floor(float64(u.DiskBytes)/(1<<30))+1)
	}
	return least, max(now*2, least)
}
