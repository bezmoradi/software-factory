package session

import (
	"math"
	"slices"
	"strings"
	"time"
)

const (
	maxSubagents    = 64
	shownSubagents  = 20
	maxSubagentType = 80
	maxSubagentLine = 160
	maxAgentTokens  = 1 << 40
)

const (
	SubagentRunning = "running"
	SubagentDone    = "done"
	SubagentFailed  = "failed"
	SubagentStopped = "stopped"
)

var subagentWords = map[string]string{SubagentRunning: "Running", SubagentDone: "Done", SubagentFailed: "Failed", SubagentStopped: "Stopped"}

var listedStates = map[string]string{
	"pending": SubagentRunning, "running": SubagentRunning, "waiting": SubagentRunning,
	"completed": SubagentDone, "idle": SubagentDone, "failed": SubagentFailed, "killed": SubagentStopped,
}

var turnEnds = map[string]string{"answer": SubagentDone, "aborted": SubagentStopped, "error": SubagentFailed, "refusal": SubagentFailed}

var unaskedPrompts = map[string]bool{"task": true, "history": true}

type SubagentRow struct {
	ID     string
	Type   string
	Task   string
	State  string
	Label  string
	Start  time.Time
	End    time.Time
	Tool   string
	Line   string
	Tokens int64
}

type subagent struct {
	SubagentRow
	listed string
	hidden bool
}

type subagentBook struct {
	epoch string
	rows  []*subagent
}

func (r *Registry) noteSubagents(c *conn, kind string, ev map[string]any, now time.Time) bool {
	changed := r.subagentEvent(c, kind, ev, now)
	a := r.admitted[c.sid]
	return a != nil && a.agents.settled() || changed
}

func (r *Registry) subagentEvent(c *conn, kind string, ev map[string]any, now time.Time) bool {
	a := r.admitted[c.sid]
	if a == nil {
		return false
	}
	b := &a.agents
	changed := false
	if b.epoch != c.epoch {
		changed = len(b.rows) > 0
		b.epoch, b.rows = c.epoch, nil
	}
	switch kind {
	case "agents":
		list, _ := ev["agents"].([]any)
		return b.listed(list, now) || changed
	case "tool":
		return b.toolLine(ev, r.worktreeOf(c), r.areaOf(c)) || changed
	case "agent_turn":
		return b.turnEnded(ev, now) || changed
	case "prompt":
		from, _ := ev["from"].(string)
		return !unaskedPrompts[from] && b.nextMessage() || changed
	}
	return changed
}

func (b *subagentBook) find(id string) *subagent {
	for _, s := range b.rows {
		if s.ID == id {
			return s
		}
	}
	return nil
}

func (b *subagentBook) add(id string, now time.Time) *subagent {
	if len(b.rows) >= maxSubagents {
		drop := max(0, slices.IndexFunc(b.rows, func(s *subagent) bool { return s.hidden }))
		b.rows = slices.Delete(b.rows, drop, drop+1)
	}
	s := &subagent{ID: id, Start: now}
	b.rows = append(b.rows, s)
	return s
}

func agentID(v any) string {
	id, _ := v.(string)
	if id == "" || len(id) > maxToolID || controlChars.MatchString(id) {
		return ""
	}
	return id
}

func (b *subagentBook) listed(list []any, now time.Time) bool {
	changed := false
	for _, x := range list {
		m, _ := x.(map[string]any)
		id := agentID(m["id"])
		status, _ := m["status"].(string)
		state, known := listedStates[status]
		if id == "" || !known {
			continue
		}
		s := b.find(id)
		if s == nil {
			s = b.add(id, now)
			s.hidden = state != SubagentRunning
			changed = true
		}
		kind, _ := m["type"].(string)
		task, _ := m["description"].(string)
		kind, task = spokenWithin(kind, maxSubagentType), spoken(task)
		if kind != s.Type || task != s.Task {
			s.Type, s.Task = kind, task
			changed = true
		}
		if status != s.listed {
			s.listed = status
			changed = s.set(state, now) || changed
		}
	}
	return changed
}

func (s *subagent) set(state string, now time.Time) bool {
	if state == s.State {
		return false
	}
	ended := s.State != "" && s.State != SubagentRunning
	s.State = state
	if state == SubagentRunning {
		s.hidden, s.End = false, time.Time{}
		if ended {
			s.Start = now
		}
		return true
	}
	if s.End.IsZero() {
		s.End = now
	}
	return true
}

func (b *subagentBook) toolLine(ev map[string]any, cwd, area string) bool {
	s := b.find(agentID(ev["agentId"]))
	if s == nil {
		return false
	}
	tool, line := subagentLine(ev, cwd, area)
	if tool == s.Tool && line == s.Line {
		return false
	}
	s.Tool, s.Line = tool, line
	return true
}

func subagentLine(ev map[string]any, cwd, area string) (string, string) {
	tool, _ := ev["tool"].(string)
	summary, _ := ev["summary"].(string)
	line := callLine(ev, cwd, area)
	said, _ := line["said"].(string)
	programs, _ := line["programs"].([]string)
	command, _ := line["command"].(string)
	path, _ := line["path"].(string)
	text := summary
	switch {
	case said != "" && len(programs) > 0:
		text = said + " · " + strings.Join(programs, ", ")
	case command != "":
		text = command
	case path != "":
		text = path
	}
	return spokenWithin(tool, maxSubagentType), spokenWithin(text, maxSubagentLine)
}

func (b *subagentBook) turnEnded(ev map[string]any, now time.Time) bool {
	s := b.find(agentID(ev["agentId"]))
	if s == nil {
		return false
	}
	changed := false
	if n, ok := ev["tokens"].(float64); ok && n > 0 && n <= maxAgentTokens && n == math.Trunc(n) && s.Tokens+int64(n) <= maxAgentTokens {
		s.Tokens += int64(n)
		changed = true
	}
	reason, _ := ev["reason"].(string)
	if state, ok := turnEnds[reason]; ok {
		changed = s.set(state, now) || changed
	}
	return changed
}

func (b *subagentBook) nextMessage() bool {
	changed := false
	for _, s := range b.rows {
		if s.State != SubagentRunning && !s.hidden {
			s.hidden = true
			changed = true
		}
	}
	return changed
}

func (b *subagentBook) settled() bool {
	return !b.running(b.epoch) && b.nextMessage()
}

func (b *subagentBook) running(epoch string) bool {
	if b.epoch != epoch {
		return false
	}
	for _, s := range b.rows {
		if s.State == SubagentRunning {
			return true
		}
	}
	return false
}

func (b *subagentBook) view(epoch string) []SubagentRow {
	if b.epoch != epoch {
		return nil
	}
	var out []SubagentRow
	for _, s := range b.rows {
		if s.hidden {
			continue
		}
		row := s.SubagentRow
		row.Label = subagentWords[row.State]
		out = append(out, row)
	}
	if len(out) > shownSubagents {
		out = out[len(out)-shownSubagents:]
	}
	return out
}
