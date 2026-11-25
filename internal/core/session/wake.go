package session

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	QueueFor    = 2 * time.Minute
	settleFor   = 3 * time.Second
	maxQueued   = 20
	StatesEvery = 10 * time.Second
	daemonFor   = 10 * time.Second
	maxState    = 200
	firstPidFor = time.Minute
)

var daemonPid = regexp.MustCompile(`(?m)^pid:\s+(\d+)\s*$`)

type queue struct {
	msgs   []Fields
	at     time.Time
	until  time.Time
	marked []string
}

type AgentState struct {
	State      string
	Status     string
	WaitingFor string
}

type MessageWaker interface {
	Asleep(sid string) bool
	WakeWithMessage(ctx context.Context, sid, text string, typed, confirm bool, consent Consent) error
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

func (r *Registry) queueMessage(sid, text string, typed, confirm bool) (*queue, error) {
	text = strings.TrimSpace(text)
	var first FirstStep
	if typed {
		first = r.firstStepFor(sid, text, false)
	}
	made, used, err := r.queueLocked(sid, text, confirm, first)
	if first.Step != "" && used == "" {
		r.unclaimStep(sid, first.Step)
	}
	return made, err
}

func (r *Registry) queueLocked(sid, text string, confirm bool, first FirstStep) (*queue, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	a := r.admitted[sid]
	switch {
	case a == nil:
		return nil, "", conflict("unverified", "The engine didn't start this session, so it takes nothing for it from the page.")
	case a.running || len(r.liveFor(sid, now)) > 0:
		return nil, "", conflict("not stopped", "The session is running again. Send the message once more.")
	case a.Kind != "background":
		return nil, "", conflict("not running", "The session isn't running. Wake it first.")
	case !r.lastControl(sid):
		return nil, "", conflict("asleep", "This session was read-only on this page when it stopped. Press Wake it above the message box first.")
	case text == "":
		return nil, "", invalid("text", "Type a message first.")
	case utf16Len(text) > MaxText || !utf8.ValidString(text):
		return nil, "", invalid("text", "A message holds at most 20,000 characters.")
	case strings.HasPrefix(text, "!"):
		return nil, "", shellLine(text)
	}
	if name := commandName(text); name != "" && refusedName(name, r.catalogs[sid]) {
		return nil, "", invalid("command", commandRefused(a.Box, a.ID))
	}
	if r.lastMode[sid] == "bypassPermissions" && !confirm {
		return nil, "", conflict("confirm", "This session runs with bypassPermissions: Claude runs every tool without asking. Confirm to send anyway.")
	}
	q := r.queued[sid]
	if q != nil && len(q.msgs) >= maxQueued {
		return nil, "", limited("queue", "Twenty messages already wait for this session to wake.")
	}
	sent, noted, step := r.stepTextLocked(sid, text, first)
	msg := Fields{"kind": "prompt", "text": sent}
	if q != nil {
		q.msgs = append(q.msgs, msg)
		q.marked = withStep(q.marked, step)
		if noted {
			r.dropNotesLocked(sid)
		}
		r.announce(now)
		return nil, step, nil
	}
	made := &queue{msgs: []Fields{msg}, at: now, until: now.Add(QueueFor), marked: withStep(nil, step)}
	r.queued[sid] = made
	if noted {
		r.dropNotesLocked(sid)
	}
	r.announce(now)
	return made, step, nil
}

func (r *Registry) unqueue(sid string, made *queue) {
	r.mu.Lock()
	if r.queued[sid] != made {
		r.mu.Unlock()
		return
	}
	marked := made.marked
	delete(r.queued, sid)
	r.announce(r.now())
	r.mu.Unlock()
	for _, s := range marked {
		r.unclaimStep(sid, s)
	}
}

func (r *Registry) releaseQueued(now time.Time) {
	for sid, q := range r.queued {
		if now.After(q.until) {
			r.unclaimLocked(sid, q.marked)
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
			r.unclaimLocked(sid, q.marked)
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

func (d *Dispatcher) WakeWithMessage(ctx context.Context, sid, text string, typed, confirm bool, consent Consent) error {
	made, err := d.reg.queueMessage(sid, text, typed, confirm)
	if err != nil || made == nil {
		return err
	}
	if err := d.wakeFor(ctx, sid, consent, ByPage, "a message the person sent to the stopped session"); err != nil {
		d.reg.unqueue(sid, made)
		return err
	}
	shared.For(ctx, d.cfg.Logger).Info("message queued", "event", "woken")
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
	if !d.boxed() {
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
	byBox := map[string]map[string]*run{}
	dirs := map[string]string{}
	skipped := map[string]bool{}
	for _, r := range d.runs {
		if r.homeAsk != "" || r.homeGated || r.busy && !r.running {
			skipped[r.box] = true
		}
	}
	for sid, r := range d.runs {
		if r.spec.Method == MethodPlugin && r.cwd != "" && r.box != "" && !r.doubled && !skipped[r.box] {
			if byBox[r.box] == nil {
				byBox[r.box] = map[string]*run{}
			}
			byBox[r.box][sid] = r
			dirs[r.box] = r.cwd
		}
	}
	d.mu.Unlock()
	if len(byBox) == 0 {
		return
	}
	live := map[string]bool{}
	for _, s := range d.reg.Sessions() {
		live[s.SID] = s.Live
	}
	states := map[string]AgentState{}
	supervisor, answered := "", false
	for box, mine := range byBox {
		m := d.boxLock(box)
		m.Lock()
		if !d.liveBox(box) || d.boxHeld(box) {
			m.Unlock()
			continue
		}
		got, ok := d.refreshBox(ctx, box, dirs[box], mine, live, states)
		m.Unlock()
		answered = answered || ok
		if got != "" && supervisor != "running" {
			supervisor = got
		}
	}
	if answered {
		d.reg.SetStates(states)
	}
	if supervisor != "" {
		d.reg.SetSupervisor(supervisor)
	}
}

func claudeEnded(state string) bool {
	return state == "done" || state == "failed" || state == "stopped"
}

func (d *Dispatcher) comingUp(r *run, state string) bool {
	return !r.pidPendingSince.IsZero() && d.reg.now().Sub(r.pidPendingSince) < firstPidFor && !claudeEnded(state)
}

func (d *Dispatcher) refreshBox(ctx context.Context, box, dir string, mine map[string]*run, live map[string]bool, states map[string]AgentState) (string, bool) {
	host := d.hostFor(box)
	lctx, cancel := context.WithTimeout(ctx, claudeList)
	res, err := host.Run(lctx, Command{Argv: d.claude("agents", "--json", "--all"), Dir: dir, Timeout: claudeList})
	cancel()
	d.restarted(ctx, host, mine, live)
	rows, answered := claudeRows(res.Stdout)
	answered = answered && err == nil && res.Code == 0
	if answered {
		for _, row := range rows {
			r := mine[row.SessionID]
			if r == nil || row.ID != r.id {
				continue
			}
			states[row.SessionID] = AgentState{State: shortWord(row.State), Status: shortWord(row.Status), WaitingFor: shortWord(row.WaitingFor)}
			d.mu.Lock()
			if row.Pid > 0 {
				r.pidPendingSince = time.Time{}
			}
			gone := r.running && !r.busy && row.Pid <= 0 && !live[row.SessionID] && !d.comingUp(r, row.State)
			if gone {
				r.running = false
			}
			d.mu.Unlock()
			if gone {
				d.reg.Ended(row.SessionID, false)
				shared.For(ctx, d.cfg.Logger).Info("agent session", "event", "stopped-by-itself")
				go d.idleBox(r)
			}
		}
	}
	sctx, scancel := context.WithTimeout(ctx, daemonFor)
	res, err = host.Run(sctx, Command{Argv: d.claude("daemon", "status"), Dir: dir, Timeout: daemonFor})
	scancel()
	switch {
	case err != nil:
	case daemonPid.MatchString(res.Stdout):
		return "running", answered
	case strings.Contains(res.Stdout, "not running"):
		return "stopped", answered
	}
	return "", answered
}
