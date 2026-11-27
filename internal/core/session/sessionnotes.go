package session

import (
	"maps"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	NotifyFrame  = "notify"
	NotifyTurn   = "turn finished"
	NotifyReview = "review finished"
)

func notifyOf(sid, what, key string) Fields {
	return Fields{"type": NotifyFrame, "sid": sid, "what": what, "key": key}
}

func reviewFinished(sid string, was *ReviewView, now ReviewView) (string, bool) {
	if was == nil || !was.Running || now.Running || now.Commit == "" {
		return "", false
	}
	states := make([]string, 0, len(now.Roles))
	for _, role := range now.Roles {
		states = append(states, string(role.State))
	}
	return "review:" + sid + ":" + now.Commit + ":" + strings.Join(states, ","), true
}

func (r *Registry) noteReport(sid, kind string, ev map[string]any, now time.Time) bool {
	a := r.admitted[sid]
	if a == nil {
		return false
	}
	a.lastReport = now
	switch kind {
	case "compact":
		was := a.compacting
		a.compacting = ev["state"] == "start"
		return was != a.compacting
	case "agents":
		list, _ := ev["agents"].([]any)
		a.agentStates = map[string]string{}
		for _, x := range list {
			m, ok := x.(map[string]any)
			if !ok {
				continue
			}
			if kind, ok := m["type"].(string); ok {
				a.agentStates[kind], _ = m["status"].(string)
			}
		}
		return true
	case "prompt", "idle":
		return a.noteStep(kind, ev)
	}
	return false
}

var stepCommand = regexp.MustCompile(`^\s*(?:<command-name>\s*)?/knackpack:(challenge-requirements|plan-implementation)\b`)

func stepOf(text string) string {
	switch m := stepCommand.FindStringSubmatch(text); {
	case m == nil:
		return ""
	case m[1] == "challenge-requirements":
		return "define"
	}
	return "plan"
}

func (a *admitted) noteStep(kind string, ev map[string]any) bool {
	was := a.step
	switch kind {
	case "idle":
		a.step = ""
	case "prompt":
		text, _ := ev["text"].(string)
		a.step = stepOf(text)
	}
	grew := a.step != "" && !slices.Contains(a.stepsRan, a.step)
	if grew {
		a.stepsRan = append(a.stepsRan, a.step)
	}
	return grew || was != a.step
}

func (r *Registry) workingInto(sid string, info *Info) {
	if a := r.admitted[sid]; a != nil && info.Live {
		info.Compacting = a.compacting
		info.Subagents, info.SubagentRows = a.agents.running(info.Epoch), a.agents.view(info.Epoch)
		info.TasksRunning, info.TaskRows = a.tasks.running(info.Epoch), a.tasks.view(info.Epoch)
	}
	if a := r.admitted[sid]; a != nil {
		info.CheckpointSaved = a.checkpointed != "" && a.checkpointed == a.contextLevel
		info.Step, info.StepsRan, info.AgentStates = a.step, slices.Clone(a.stepsRan), maps.Clone(a.agentStates)
	}
}

func (r *Registry) noteContext(sid, kind string, ev map[string]any) bool {
	a := r.admitted[sid]
	if a == nil || kind != "context" {
		return false
	}
	lvl := ""
	if cp := r.checkpointOf(sid, kind, ev); cp != nil {
		lvl, _ = cp["level"].(string)
	}
	was := a.checkpointed != "" && a.checkpointed == a.contextLevel
	a.contextLevel = lvl
	if lvl == "" {
		a.checkpointed = ""
	}
	return was != (a.checkpointed != "" && a.checkpointed == a.contextLevel)
}

func (r *Registry) checkpointAsked(sid, text string) bool {
	a := r.admitted[sid]
	if a == nil || a.contextLevel == "" || commandName(text) != CheckpointCommand[1:] {
		return false
	}
	a.checkpointed = a.contextLevel
	return true
}

func (r *Registry) stallInto(sid string, info *Info, now time.Time) {
	a := r.admitted[sid]
	if a == nil || a.lastReport.IsZero() || !Controllable(*info) || !info.Busy || info.Needs > 0 || a.agents.running(info.Epoch) || now.Sub(a.lastReport) < StallAfter {
		return
	}
	info.Stalled, info.StalledSince = true, a.lastReport
}

func (r *Registry) checkpointOf(sid, kind string, ev map[string]any) Fields {
	if kind != "context" {
		return nil
	}
	lvl, _ := ev["level"].(string)
	tokens, ok := ev["tokens"].(float64)
	w := r.workflows[sid]
	if lvl != "warn" && lvl != "stop" || !ok || math.IsNaN(tokens) || math.IsInf(tokens, 0) || tokens < 0 || w == nil || !w.Knackpack {
		return nil
	}
	k := strconv.FormatFloat(math.Round(tokens/1000), 'f', 0, 64) + "k"
	text := "About " + k + " tokens in context. Save a checkpoint before you compact, while the details are still there."
	if lvl == "stop" {
		text = "About " + k + " tokens, past knackpack's 500k stop point. Save a checkpoint, then run /compact."
	}
	return Fields{"level": lvl, "text": text, "said": "Long conversation: about " + k + " tokens. Save a checkpoint before you compact.", "stop": lvl == "stop"}
}

func (r *Registry) Session(sid string) (Info, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	if r.admitted[sid] == nil && len(r.liveFor(sid, now)) == 0 {
		return Info{}, false
	}
	return r.info(sid, now), true
}
