package session

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

const (
	loopWindow = 8
	loopRepeat = 3
	loopDetail = 160
)

type loopCall struct {
	id      string
	key     string
	command string
	tool    string
	detail  string
	failed  bool
}

type loopNotice struct {
	tool    string
	detail  string
	count   int
	failing bool
	at      time.Time
}

type loopTrack struct {
	calls  []loopCall
	notice *loopNotice
}

func loopKey(tool, agentID, summary, command string) string {
	sum := sha256.Sum256([]byte(summary + "\x00" + command))
	return tool + "\x00" + agentID + "\x00" + hex.EncodeToString(sum[:8])
}

func (l *Limits) loopTurn(sid string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if t := l.loops[sid]; t != nil {
		t.calls = nil
		t.notice = nil
	}
}

func (l *Limits) loopTool(sid string, ev map[string]any, now time.Time) {
	tool, _ := ev["tool"].(string)
	summary, _ := ev["summary"].(string)
	command, _ := ev["command"].(string)
	id, _ := ev["id"].(string)
	agentID, _ := ev["agentId"].(string)
	if tool == "" {
		return
	}
	shell := command
	if shell == "" && shellTools[tool] {
		shell = summary
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	t := l.loops[sid]
	if t == nil {
		if len(l.loops) >= MaxCapSessions {
			return
		}
		t = &loopTrack{}
		l.loops[sid] = t
	}
	detail := summary
	if command != "" {
		detail = command
	}
	call := loopCall{id: clip(id, maxToolID), key: loopKey(clip(tool, 80), clip(agentID, 80), summary, command), command: shell, tool: clip(tool, 80), detail: clip(detail, loopDetail)}
	t.calls = append(t.calls, call)
	if len(t.calls) > loopWindow {
		t.calls = t.calls[len(t.calls)-loopWindow:]
	}
	n := 0
	for _, c := range t.calls {
		if c.key == call.key {
			n++
		}
	}
	if n >= loopRepeat {
		t.notice = &loopNotice{tool: call.tool, detail: call.detail, count: n, at: now}
	}
}

func (l *Limits) loopDone(sid string, ev map[string]any, now time.Time) {
	id, _ := ev["id"].(string)
	ok, _ := ev["ok"].(bool)
	command, _ := ev["command"].(string)
	if ok || id == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	t := l.loops[sid]
	if t == nil {
		return
	}
	var failed *loopCall
	for i := range t.calls {
		if t.calls[i].id == id {
			t.calls[i].failed = true
			if command != "" && t.calls[i].command == "" {
				t.calls[i].command = command
			}
			failed = &t.calls[i]
		}
	}
	if failed == nil || failed.command == "" {
		return
	}
	n := 0
	for _, c := range t.calls {
		if c.failed && c.command == failed.command {
			n++
		}
	}
	if n >= loopRepeat && (t.notice == nil || t.notice.count < n || !t.notice.failing) {
		t.notice = &loopNotice{tool: failed.tool, detail: clip(failed.command, loopDetail), count: n, failing: true, at: now}
	}
}
