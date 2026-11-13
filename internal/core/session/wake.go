package session

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	QueueFor    = 2 * time.Minute
	settleFor   = 3 * time.Second
	maxQueued   = 20
	StatesEvery = 10 * time.Second
	daemonFor   = 10 * time.Second
	maxState    = 200
)

var daemonPid = regexp.MustCompile(`(?m)^pid:\s+(\d+)\s*$`)

type queue struct {
	msgs  []Fields
	at    time.Time
	until time.Time
}

type AgentState struct {
	State      string
	Status     string
	WaitingFor string
}

type MessageWaker interface {
	Asleep(sid string) bool
	WakeWithMessage(ctx context.Context, sid, text string, confirm bool, consent Consent) error
}

var _ MessageWaker = (*Dispatcher)(nil)

func (r *Registry) lastControl(sid string) bool {
	var last *conn
	for _, c := range r.conns {
		if c.sid == sid && (last == nil || c.order > last.order) {
			last = c
		}
	}
	if last != nil {
		return last.controlled
	}
	if on, ok := r.lastGate[sid]; ok {
		return on
	}
	return true
}

func (r *Registry) queueMessage(sid, text string, confirm bool) (bool, error) {
	text = strings.TrimSpace(text)
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	a := r.admitted[sid]
	switch {
	case a == nil:
		return false, conflict("unverified")
	case a.running || len(r.liveFor(sid, now)) > 0:
		return false, conflict("not stopped")
	case a.Kind != "background":
		return false, conflict("not running")
	case !r.lastControl(sid):
		return false, explained(conflict("asleep"), "This session was read-only on this page when it stopped. Wake it without a message from the ⋯ menu first.")
	case text == "" || utf16Len(text) > MaxText || !utf8.ValidString(text):
		return false, invalid("text")
	case strings.HasPrefix(text, "!"):
		return false, invalid("command")
	}
	if name := commandName(text); name != "" && refusedName(name, r.catalogs[sid]) {
		return false, explained(invalid("command"), commandRefused)
	}
	if r.lastMode[sid] == "bypassPermissions" && !confirm {
		return false, conflict("confirm")
	}
	sent, noted := r.noted(sid, text)
	msg := Fields{"kind": "prompt", "text": sent}
	if q := r.queued[sid]; q != nil {
		if len(q.msgs) >= maxQueued {
			return false, explained(limited("queue"), "Twenty messages already wait for this session to wake.")
		}
		q.msgs = append(q.msgs, msg)
		if noted {
			delete(r.notes, sid)
		}
		r.announce(now)
		return true, nil
	}
	r.queued[sid] = &queue{msgs: []Fields{msg}, at: now, until: now.Add(QueueFor)}
	if noted {
		delete(r.notes, sid)
	}
	r.announce(now)
	return false, nil
}

func (r *Registry) unqueue(sid string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.queued, sid)
	r.announce(r.now())
}

func (r *Registry) releaseQueued(now time.Time) {
	for sid, q := range r.queued {
		if now.After(q.until) {
			delete(r.queued, sid)
			r.broadcast(Fields{"type": "dropped", "sid": sid, "why": "timeout", "count": float64(len(q.msgs))}, now)
			continue
		}
		a := r.admitted[sid]
		c := r.only(sid, now)
		if a == nil || !a.running || c == nil || c.born.Before(q.at) || now.Sub(c.born) < settleFor || c.protocol < MinProtocol {
			continue
		}
		delete(r.queued, sid)
		if !c.controlled {
			r.broadcast(Fields{"type": "dropped", "sid": sid, "why": "read-only", "count": float64(len(q.msgs))}, now)
			continue
		}
		for _, m := range q.msgs {
			r.tell(c, m, now)
		}
	}
}

func (r *Registry) SetStates(states map[string]AgentState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.states = states
	r.announce(r.now())
}

func (r *Registry) SetSupervisor(state string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.supervisor == state {
		return
	}
	r.supervisor = state
	r.broadcast(Fields{"type": "supervisor", "state": state}, r.now())
}

func (r *Registry) Supervisor() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.supervisor
}

func (d *Dispatcher) Asleep(sid string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	r := d.runs[sid]
	return r != nil && !r.running && !r.busy
}

func (d *Dispatcher) WakeWithMessage(ctx context.Context, sid, text string, confirm bool, consent Consent) error {
	added, err := d.reg.queueMessage(sid, text, confirm)
	if err != nil || added {
		return err
	}
	if err := d.Wake(ctx, sid, consent); err != nil {
		d.reg.unqueue(sid)
		return err
	}
	d.cfg.Logger.Info("message queued", "event", "woken")
	return nil
}

type claudeRow struct {
	SessionID  string
	ID         string
	State      string
	Status     string
	WaitingFor string
	Pid        float64
}

func claudeRows(out string) ([]claudeRow, bool) {
	var raw []map[string]any
	if json.Unmarshal([]byte(out), &raw) != nil {
		return nil, false
	}
	rows := make([]claudeRow, 0, len(raw))
	for _, m := range raw {
		text := func(k string) string {
			v, _ := m[k].(string)
			return v
		}
		pid, _ := m["pid"].(float64)
		rows = append(rows, claudeRow{SessionID: text("sessionId"), ID: text("id"), State: text("state"), Status: text("status"), WaitingFor: text("waitingFor"), Pid: pid})
	}
	return rows, true
}

func (d *Dispatcher) WatchStates(ctx context.Context, every time.Duration) {
	if d.cfg.Host == nil {
		return
	}
	if every <= 0 {
		every = StatesEvery
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if d.reg.Streams() > 0 {
				d.refreshStates(ctx)
			}
		}
	}
}

func shortWord(s string) string {
	s = strings.TrimSpace(controlChars.ReplaceAllString(s, " "))
	return clip(s, maxState)
}

func (d *Dispatcher) refreshStates(ctx context.Context) {
	d.mu.Lock()
	mine := map[string]*run{}
	dir := ""
	for sid, r := range d.runs {
		if r.spec.Method == MethodPlugin && r.cwd != "" {
			mine[sid] = r
			dir = r.cwd
		}
	}
	d.mu.Unlock()
	if len(mine) == 0 {
		return
	}
	lctx, cancel := context.WithTimeout(ctx, claudeList)
	res, err := d.cfg.Host.Run(lctx, Command{Argv: d.claude("agents", "--json", "--all"), Dir: dir, Timeout: claudeList})
	cancel()
	if rows, ok := claudeRows(res.Stdout); err == nil && res.Code == 0 && ok {
		states := map[string]AgentState{}
		live := map[string]bool{}
		for _, s := range d.reg.Sessions() {
			live[s.SID] = s.Live
		}
		for _, row := range rows {
			r := mine[row.SessionID]
			if r == nil || row.ID != r.id {
				continue
			}
			states[row.SessionID] = AgentState{State: shortWord(row.State), Status: shortWord(row.Status), WaitingFor: shortWord(row.WaitingFor)}
			d.mu.Lock()
			gone := r.running && !r.busy && row.Pid <= 0 && !live[row.SessionID]
			if gone {
				r.running = false
			}
			d.mu.Unlock()
			if gone {
				d.reg.Ended(row.SessionID, false)
				d.cfg.Logger.Info("agent session", "event", "stopped-by-itself")
			}
		}
		d.reg.SetStates(states)
	}
	sctx, scancel := context.WithTimeout(ctx, daemonFor)
	res, err = d.cfg.Host.Run(sctx, Command{Argv: d.claude("daemon", "status"), Dir: dir, Timeout: daemonFor})
	scancel()
	switch {
	case err != nil:
	case daemonPid.MatchString(res.Stdout):
		d.reg.SetSupervisor("running")
	case strings.Contains(res.Stdout, "not running"):
		d.reg.SetSupervisor("stopped")
	}
}

func (d *Dispatcher) StopLeftovers(ctx context.Context) {
	if d.cfg.Host == nil || d.cfg.Repositories == nil {
		return
	}
	repos, err := d.cfg.Repositories.List()
	if err != nil || len(repos) == 0 {
		return
	}
	list, err := d.listClaude(ctx, repos[0].Path)
	if err != nil {
		d.cfg.Logger.Warn("dispatch", "event", "leftovers-not-listed")
		return
	}
	stopped := 0
	for _, l := range list {
		if !shortID.MatchString(l.ID) || !absolute(l.Cwd) || d.keeps(l.ID) {
			continue
		}
		res, err := d.cfg.Host.Run(ctx, Command{Argv: d.claude("stop", l.ID), Dir: l.Cwd, Timeout: claudeRun})
		if err == nil && res.Code == 0 {
			stopped++
		}
	}
	if stopped > 0 {
		d.cfg.Logger.Info("dispatch", "event", "leftovers-stopped", "count", stopped)
	}
}

func (d *Dispatcher) keeps(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range d.runs {
		if r.id == id && (r.running || r.busy) {
			return true
		}
	}
	return false
}
