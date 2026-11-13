package session

import "time"

type runStamp struct {
	interruptedAt time.Time
	agent         string
	engine        string
	workflow      string
}

func (d *Dispatcher) stampOf(agent string, interrupted time.Time) runStamp {
	s := runStamp{interruptedAt: interrupted, engine: d.cfg.Version}
	if d.cfg.Versions != nil {
		s.agent, s.workflow = d.cfg.Versions(agent)
	}
	return s
}

func (d *Dispatcher) woke(r *run) {
	d.mu.Lock()
	r.stamp = d.stampOf(r.spec.ID, time.Time{})
	d.mu.Unlock()
	d.saveRuns()
}

func (d *Dispatcher) MarkStopping() int {
	now := d.reg.now()
	type candidate struct {
		sid string
		r   *run
		b   *bridge
	}
	var list []candidate
	d.mu.Lock()
	for sid, r := range d.runs {
		if r.running {
			list = append(list, candidate{sid: sid, r: r, b: r.bridge})
		}
	}
	d.mu.Unlock()
	marked := 0
	for _, c := range list {
		if !d.reg.Busy(c.sid) && (c.b == nil || !c.b.inTurn()) {
			continue
		}
		d.mu.Lock()
		c.r.stamp.interruptedAt = now
		d.mu.Unlock()
		marked++
	}
	if marked > 0 {
		d.saveRuns()
		d.cfg.Logger.Info("sessions", "event", "interrupted", "count", marked)
	}
	return marked
}

func (d *Dispatcher) interrupted(sid string, at time.Time) {
	if at.IsZero() || d.cfg.Note == nil {
		return
	}
	d.cfg.Note(sid, InterruptedNote(at))
}

func InterruptedNote(at time.Time) string {
	return "The engine stopped at " + at.UTC().Format("2006-01-02 15:04 UTC") + " during your last turn, so that turn was cut off. Tool calls in it may or may not have taken effect, and a factory_push, factory_issue or factory_pull_request call in it may not have been answered. Check git status and git log before you go on."
}

func (r *Registry) Busy(sid string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.conns {
		if c.sid == sid && c.busy {
			return true
		}
	}
	return false
}

func (b *bridge) inTurn() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.turn != ""
}
