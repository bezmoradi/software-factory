package session

import (
	"slices"
	"strconv"
	"time"
)

const (
	maxTasks     = 64
	shownTasks   = 20
	maxTaskWhat  = 160
	maxTaskState = 40
)

const (
	TaskShell         = "shell"
	TaskMonitorKind   = "monitor"
	TaskWorkflowKind  = "workflow"
	TaskReportRunning = "running"
)

var taskKindWords = map[string]string{TaskShell: "Command", TaskMonitorKind: "Monitor", TaskWorkflowKind: "Workflow"}

var taskEnds = map[string]string{"completed": SubagentDone, "failed": SubagentFailed, "killed": SubagentStopped, "stopped": SubagentStopped}

type TaskRow struct {
	ID    string
	Kind  string
	What  string
	State string
	Label string
	Start time.Time
	End   time.Time
}

type task struct {
	TaskRow
	hidden  bool
	assumed bool
}

type taskBook struct {
	epoch string
	rows  []*task
}

func BackgroundNote(running int) string {
	if running == 1 {
		return "1 task running"
	}
	return strconv.Itoa(running) + " tasks running"
}

func (r *Registry) noteTasks(c *conn, kind string, ev map[string]any, now time.Time) bool {
	changed := r.taskEvent(c, kind, ev, now)
	a := r.admitted[c.sid]
	return a != nil && a.tasks.settled() || changed
}

func (r *Registry) taskEvent(c *conn, kind string, ev map[string]any, now time.Time) bool {
	a := r.admitted[c.sid]
	if a == nil {
		return false
	}
	b := &a.tasks
	changed := false
	if b.epoch != c.epoch {
		changed = len(b.rows) > 0
		b.epoch, b.rows = c.epoch, nil
	}
	switch kind {
	case "task":
		return b.reported(ev, now) || changed
	case "tasks":
		list, ok := ev["tasks"].([]any)
		running, listed := ev["running"].([]any)
		return ok && listed && b.listed(list, running, now) || changed
	case "prompt":
		from, _ := ev["from"].(string)
		return !unaskedPrompts[from] && b.nextMessage() || changed
	}
	return changed
}

func (b *taskBook) find(id string) *task {
	for _, t := range b.rows {
		if t.ID == id {
			return t
		}
	}
	return nil
}

func (b *taskBook) add(id, kind, what string, now time.Time) {
	if len(b.rows) >= maxTasks {
		drop := max(0, slices.IndexFunc(b.rows, func(t *task) bool { return t.hidden }))
		b.rows = slices.Delete(b.rows, drop, drop+1)
	}
	b.rows = append(b.rows, &task{ID: id, Kind: kind, What: what, State: SubagentRunning, Start: now})
}

func taskStart(ev map[string]any) (string, string, bool) {
	kind, _ := ev["kind"].(string)
	word := taskKindWords[kind]
	if word == "" {
		return "", "", false
	}
	command, _ := ev["command"].(string)
	description, _ := ev["description"].(string)
	what := command
	if kind != TaskShell || what == "" {
		what = cmpOr(description, command)
	}
	return word, spokenWithin(what, maxTaskWhat), true
}

func (b *taskBook) reported(ev map[string]any, now time.Time) bool {
	id := agentID(ev["id"])
	state, _ := ev["state"].(string)
	if id == "" || len(state) > maxTaskState {
		return false
	}
	t := b.find(id)
	if state == TaskReportRunning {
		word, what, ok := taskStart(ev)
		if t != nil || !ok {
			return false
		}
		b.add(id, word, what, now)
		return true
	}
	ended, known := taskEnds[state]
	if t == nil || !known || t.State != SubagentRunning && !t.assumed {
		return false
	}
	t.assumed = false
	if t.State == ended {
		return false
	}
	t.State = ended
	if t.End.IsZero() {
		t.End = now
	}
	return true
}

func (b *taskBook) listed(list, running []any, now time.Time) bool {
	if len(list) > maxTasks || len(running) > maxTasks {
		return false
	}
	changed := false
	seen := map[string]bool{}
	for _, x := range running {
		if id := agentID(x); id != "" {
			seen[id] = true
		}
	}
	for _, x := range list {
		m, _ := x.(map[string]any)
		id := agentID(m["id"])
		word, what, ok := taskStart(m)
		if id == "" || !ok || !seen[id] {
			continue
		}
		if b.find(id) == nil {
			b.add(id, word, what, now)
			changed = true
		}
	}
	for _, t := range b.rows {
		if t.State == SubagentRunning && !seen[t.ID] {
			t.State, t.End, t.assumed = SubagentDone, now, true
			changed = true
		}
	}
	return changed
}

func (b *taskBook) nextMessage() bool {
	changed := false
	for _, t := range b.rows {
		if t.State != SubagentRunning && !t.hidden {
			t.hidden = true
			changed = true
		}
	}
	return changed
}

func (b *taskBook) settled() bool {
	return b.running(b.epoch) == 0 && b.nextMessage()
}

func (b *taskBook) running(epoch string) int {
	if b.epoch != epoch {
		return 0
	}
	n := 0
	for _, t := range b.rows {
		if t.State == SubagentRunning {
			n++
		}
	}
	return n
}

func (b *taskBook) view(epoch string) []TaskRow {
	if b.epoch != epoch {
		return nil
	}
	var out []TaskRow
	for _, t := range b.rows {
		if t.hidden {
			continue
		}
		row := t.TaskRow
		row.Label = subagentWords[row.State]
		out = append(out, row)
	}
	if len(out) > shownTasks {
		out = out[len(out)-shownTasks:]
	}
	return out
}
