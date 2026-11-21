package session

import (
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxQueue          = 10
	maxQueueSessions  = 1000
	queueWait         = "wait"
	queueRestarted    = "The engine restarted. Press Resume to send the rest."
	queueCard         = "A card waits for your answer on the page."
	queuePush         = "A push is held for your decision."
	queueFailed       = "The last turn ended with an error."
	queueAborted      = "The last turn was stopped."
	queueSignIn       = "The agent's sign-in failed. Sign it in again, then press Resume."
	queueNotRunning   = "The session isn't running. Wake it, then press Resume."
	queueSetupWaiting = "The setup command is still running in this session's worktree; the queue sends its first message when it ends."
)

type QueuedMessage struct {
	ID   string
	Text string
	At   time.Time
}

type QueueState struct {
	Items   []QueuedMessage
	Stopped string
}

type QueueStore interface {
	Load() (map[string]QueueState, error)
	Save(map[string]QueueState) error
}

type sessionQueue struct {
	items   []QueuedMessage
	stopped string
	sending bool
}

type QueueView struct {
	Items   []QueuedMessage
	Stopped string
	Waiting string
	Sending bool
}

func (w *SessionWork) loadQueues() {
	if w.cfg.Queues == nil {
		return
	}
	saved, err := w.cfg.Queues.Load()
	if err != nil {
		w.cfg.Logger.Warn("session work", "event", "queues-unreadable")
		return
	}
	for sid, st := range saved {
		if !sidPattern.MatchString(sid) || len(w.queues) >= maxQueueSessions {
			continue
		}
		q := &sessionQueue{stopped: clip(st.Stopped, 300)}
		for _, m := range st.Items {
			if text, ok := queueText(m.Text); ok && len(q.items) < MaxQueue && cardID.MatchString(m.ID) {
				q.items = append(q.items, QueuedMessage{ID: m.ID, Text: text, At: m.At})
			}
		}
		if len(q.items) == 0 {
			continue
		}
		if q.stopped == "" {
			q.stopped = queueRestarted
		}
		w.queues[sid] = q
	}
}

func (w *SessionWork) saveQueues() {
	if w.cfg.Queues == nil {
		return
	}
	w.saveMu.Lock()
	w.mu.Lock()
	snapshot := make(map[string]QueueState, len(w.queues))
	for sid, q := range w.queues {
		if len(q.items) > 0 {
			snapshot[sid] = QueueState{Items: slices.Clone(q.items), Stopped: q.stopped}
		}
	}
	w.mu.Unlock()
	err := w.cfg.Queues.Save(snapshot)
	w.saveMu.Unlock()
	if err != nil {
		w.cfg.Logger.Warn("session work", "event", "queues-not-saved")
	}
}

func queueText(text string) (string, bool) {
	text = strings.TrimSpace(text)
	if text == "" || !utf8.ValidString(text) || utf16Len(text) > MaxText || strings.HasPrefix(text, "!") || commandName(text) != "" {
		return "", false
	}
	return text, true
}

func (w *SessionWork) Queue(sid string) QueueView {
	w.mu.Lock()
	defer w.mu.Unlock()
	q := w.queues[sid]
	v := QueueView{Items: []QueuedMessage{}}
	if q != nil {
		v.Items, v.Stopped, v.Sending = slices.Clone(q.items), q.stopped, q.sending
	}
	if rs := w.runs[sid]; rs != nil && rs.setting {
		v.Waiting = queueSetupWaiting
	}
	return v
}

func (w *SessionWork) queueOf(sid string) *sessionQueue {
	q := w.queues[sid]
	if q == nil {
		q = &sessionQueue{}
		w.queues[sid] = q
	}
	return q
}

func (w *SessionWork) admitted(sid string) error {
	if _, ok := w.reg.workInfo(sid); !ok {
		return notFound("session", "The engine has no session of that id.")
	}
	return w.branchHeld(sid)
}

func (w *SessionWork) branchHeld(sid string) error {
	if w.d == nil {
		return nil
	}
	return heldBranch(w.d.Doubled, sid)
}

func QueueFull(queued int) string {
	if queued < MaxQueue {
		return ""
	}
	return "Ten messages already wait in this session's queue."
}

func checkQueued(text string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", onField("text", invalid("text", "Type the message to queue first."))
	}
	if secretText(text) {
		return "", onField("text", invalid("text", "Can't hold token-shaped text. Send it directly."))
	}
	t, ok := queueText(text)
	if !ok {
		return "", onField("text", invalid("text", "At most 20,000 characters, and not a command."))
	}
	return t, nil
}

func (w *SessionWork) QueueAdd(sid, text string) error {
	t, err := checkQueued(text)
	if err != nil {
		return err
	}
	if err := w.admitted(sid); err != nil {
		return err
	}
	if err := w.push(sid, t); err != nil {
		return err
	}
	go w.pump(sid, "")
	return nil
}

func (w *SessionWork) push(sid, text string) error {
	w.mu.Lock()
	q := w.queueOf(sid)
	if full := QueueFull(len(q.items)); full != "" {
		w.mu.Unlock()
		return limited("queue", full)
	}
	if _, ok := w.queues[sid]; !ok && len(w.queues) >= maxQueueSessions {
		w.mu.Unlock()
		return limited("queue", "The engine keeps queues for at most 1,000 sessions.")
	}
	q.items = append(q.items, QueuedMessage{ID: randomHex(16), Text: text, At: w.cfg.Now()})
	w.mu.Unlock()
	w.saveQueues()
	w.changed(sid)
	return nil
}

func (w *SessionWork) QueueEdit(sid, id, text string) (bool, error) {
	t, err := checkQueued(text)
	if err != nil {
		return false, err
	}
	return w.queueChange(sid, id, func(q *sessionQueue, i int) bool {
		if q.items[i].Text == t {
			return false
		}
		q.items[i].Text = t
		return true
	})
}

func (w *SessionWork) QueueRemove(sid, id string) error {
	_, err := w.queueChange(sid, id, func(q *sessionQueue, i int) bool {
		q.items = slices.Delete(q.items, i, i+1)
		return true
	})
	return err
}

func (w *SessionWork) QueueMove(sid, id string, to int) error {
	_, err := w.queueChange(sid, id, func(q *sessionQueue, i int) bool {
		to = min(max(to, 0), len(q.items)-1)
		m := q.items[i]
		q.items = slices.Insert(slices.Delete(q.items, i, i+1), to, m)
		return true
	})
	return err
}

func (w *SessionWork) queueChange(sid, id string, change func(q *sessionQueue, i int) bool) (bool, error) {
	if err := w.branchHeld(sid); err != nil {
		return false, err
	}
	w.mu.Lock()
	q := w.queues[sid]
	i := -1
	if q != nil {
		i = slices.IndexFunc(q.items, func(m QueuedMessage) bool { return m.ID == id })
	}
	if i < 0 {
		w.mu.Unlock()
		return false, notFound("queued", "That message is no longer in the queue.")
	}
	if q.sending && i == 0 {
		w.mu.Unlock()
		return false, conflict("sending", "That message is being sent now.")
	}
	if !change(q, i) {
		w.mu.Unlock()
		return false, nil
	}
	if len(q.items) == 0 {
		q.stopped = ""
	}
	w.mu.Unlock()
	w.saveQueues()
	w.changed(sid)
	return true, nil
}

func (w *SessionWork) QueueResume(sid string) error {
	if err := w.admitted(sid); err != nil {
		return err
	}
	w.mu.Lock()
	if q := w.queues[sid]; q != nil {
		q.stopped = ""
	}
	w.mu.Unlock()
	w.saveQueues()
	w.changed(sid)
	go w.pump(sid, "")
	return nil
}

func (w *SessionWork) divert(sid, text string, attached bool) (bool, error) {
	w.mu.Lock()
	rs := w.runs[sid]
	setting := rs != nil && rs.setting
	w.mu.Unlock()
	if !setting {
		return false, nil
	}
	if attached {
		return true, conflict("setup", "The setup command is still running in this session's worktree. Send images and files once it has finished.")
	}
	t, err := checkQueued(text)
	if err != nil {
		return true, conflict("setup", "The setup command is still running in this session's worktree, and this message can't wait in the queue. Send it once setup has finished.")
	}
	return true, w.push(sid, t)
}

func (w *SessionWork) cardShown(sid string) {
	w.stopQueue(sid, queueCard)
}

func (w *SessionWork) stopQueue(sid, why string) {
	w.mu.Lock()
	q := w.queues[sid]
	stop := q != nil && len(q.items) > 0 && q.stopped == ""
	if stop {
		q.stopped = why
	}
	w.mu.Unlock()
	if stop {
		w.cfg.Logger.Info("queue", "event", "stopped")
		w.saveQueues()
		w.changed(sid)
	}
}

func (w *SessionWork) afterTurn(sid, reason string) {
	w.pump(sid, reason)
}

func (w *SessionWork) blocked(sid, ended string) string {
	switch {
	case ended == "aborted":
		return queueAborted
	case ended != "" && !plainEnd(ended):
		return queueFailed
	}
	info, ok := w.reg.workInfo(sid)
	switch {
	case !ok || !info.Running:
		return queueNotRunning
	case info.Login:
		return queueSignIn
	case info.Cards > 0 || info.Forge:
		return queueCard
	case info.Push:
		return queuePush
	case !info.Idle:
		return queueWait
	}
	return ""
}

func (w *SessionWork) pump(sid, ended string) {
	w.mu.Lock()
	q := w.queues[sid]
	rs := w.runs[sid]
	busy := rs != nil && (rs.setting || rs.checking)
	if q == nil || len(q.items) == 0 || q.stopped != "" || q.sending || busy {
		w.mu.Unlock()
		return
	}
	q.sending = true
	first := q.items[0]
	w.mu.Unlock()
	why := w.blocked(sid, ended)
	if why == "" {
		if err := w.reg.Message(sid, first.Text, false, nil, nil); err != nil {
			why = queueRefused(err)
		}
	}
	w.mu.Lock()
	q.sending = false
	switch why {
	case queueWait:
	case "":
		q.items = slices.DeleteFunc(q.items, func(m QueuedMessage) bool { return m.ID == first.ID })
	default:
		q.stopped = why
	}
	w.mu.Unlock()
	if why == queueWait {
		return
	}
	w.cfg.Logger.Info("queue", "event", map[bool]string{true: "sent", false: "stopped"}[why == ""])
	w.saveQueues()
	w.changed(sid)
}

func queueRefused(err error) string {
	var e *Error
	if errors.As(err, &e) && e.Message != "" {
		return "The engine didn't send the next message: " + clip(e.Message, 300)
	}
	if errors.As(err, &e) {
		return "The engine didn't send the next message (" + clip(e.Reason, 40) + ")."
	}
	return "The engine didn't send the next message."
}

func QueueMoves(count, i int, sending bool) (up, down, editable bool) {
	editable = !sending || i != 0
	up = i > 0 && (!sending || i > 1)
	down = i < count-1 && (!sending || i != 0)
	return up, down, editable
}

func ModelSwitchable(agent string) bool {
	return agent == "claude"
}
