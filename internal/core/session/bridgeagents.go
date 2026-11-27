package session

import (
	"slices"

	"github.com/bezmoradi/software-factory/internal/core/agent"
)

var agentTools = map[string]bool{"Agent": true, "Task": true}

var subagentEnds = map[string]string{"completed": "completed", "failed": "failed", "declined": "failed", "cancelled": "killed"}

func inLoop(f Fields, t *agent.Tool) Fields {
	if t.Parent != "" {
		f["agentId"] = clip(t.Parent, maxToolID)
	}
	return f
}

func (b *bridge) subagentLocked(t *agent.Tool) {
	if !agentTools[t.Name] {
		return
	}
	id := clip(t.ID, maxToolID)
	for _, s := range b.subs {
		if s["id"] == id {
			return
		}
	}
	if len(b.subs) >= maxSubagents {
		ended := slices.IndexFunc(b.subs, func(s Fields) bool { return s["status"] != "running" })
		if ended < 0 {
			return
		}
		b.subs = slices.Delete(b.subs, ended, ended+1)
	}
	in := toolInput(t)
	kind, _ := in["subagent_type"].(string)
	task, _ := in["description"].(string)
	b.subs = append(b.subs, Fields{"id": id, "type": clip(kind, maxSubagentType), "description": clip(task, maxSummary), "status": "running"})
	b.agentsLocked()
}

func (b *bridge) subagentEndedLocked(t *agent.Tool) {
	status, ended := subagentEnds[t.Status]
	if !ended {
		return
	}
	id := clip(t.ID, maxToolID)
	for _, s := range b.subs {
		if s["id"] != id || s["status"] != "running" {
			continue
		}
		s["status"] = status
		b.agentsLocked()
		return
	}
}

func (b *bridge) agentsLocked() {
	list := make([]any, 0, len(b.subs))
	for _, s := range b.subs {
		list = append(list, map[string]any{"id": s["id"], "type": s["type"], "description": s["description"], "status": s["status"]})
	}
	b.recordLocked(Fields{"type": "agents", "agents": list})
}

var taskTools = map[string]string{"Bash": TaskShell, "Monitor": TaskMonitorKind}

func taskEnded(t *agent.Tool) bool {
	return t.Background != "" && t.Background != TaskReportRunning
}

func (b *bridge) taskLocked(t *agent.Tool) {
	if t.Background == "" {
		return
	}
	f := Fields{"type": "task", "id": clip(t.ID, maxToolID), "state": t.Background}
	if t.Background == TaskReportRunning {
		kind := taskTools[t.Name]
		if kind == "" {
			return
		}
		f["kind"] = kind
		in := toolInput(t)
		if c, ok := in["command"].(string); ok && c != "" {
			f["command"] = clip(c, maxCommand)
		}
		if d, ok := in["description"].(string); ok && d != "" {
			f["description"] = clip(d, maxSummary)
		}
	}
	b.recordLocked(f)
}
