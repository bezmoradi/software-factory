package session

import (
	"strconv"

	"github.com/bezmoradi/software-factory/internal/core/agent"
)

const (
	maxSubagents   = 32
	maxAgentName   = 80
	maxAgentStatus = 20
	maxHeader      = 120
	maxChoices     = 20
	maxChoiceText  = 500
)

func (b *bridge) ofAgentLocked(f Fields, sub string) Fields {
	if sub == "" {
		return f
	}
	f["agentId"] = clip(sub, maxToolID)
	if name := b.names[sub]; name != "" {
		f["agentName"] = name
	}
	return f
}

func (b *bridge) subagentsLocked(list []agent.Subagent) {
	names := map[string]string{}
	out := make([]any, 0, len(list))
	for _, a := range list {
		if a.ID == "" || len(out) >= maxSubagents {
			continue
		}
		id, name := clip(a.ID, maxToolID), clip(a.Name, maxAgentName)
		names[id] = name
		out = append(out, map[string]any{"id": id, "type": name, "description": "", "status": clip(a.Status, maxAgentStatus)})
	}
	b.names = names
	b.recordLocked(Fields{"type": "agents", "agents": out})
}

func askFields(id string, qs []agent.Question) (Fields, map[string]string) {
	texts := map[string]string{}
	list := make([]any, 0, len(qs))
	for _, q := range qs {
		if len(list) >= maxQuestions || q.ID == "" {
			continue
		}
		text := clip(q.Text, maxAnswer)
		for n := 2; ; n++ {
			if _, taken := texts[text]; !taken {
				break
			}
			text = clip(q.Text, maxAnswer-12) + " (" + strconv.Itoa(n) + ")"
		}
		texts[text] = q.ID
		options := make([]any, 0, len(q.Choices))
		for _, c := range q.Choices {
			if len(options) < maxChoices {
				options = append(options, map[string]any{"label": clip(c.Label, maxChoiceText), "description": clip(c.Description, maxChoiceText)})
			}
		}
		kind := "choice"
		if len(options) == 0 {
			kind = "text"
		}
		list = append(list, map[string]any{"question": text, "header": clip(q.Header, maxHeader), "kind": kind, "description": "",
			"multiSelect": false, "options": options, "placeholder": ""})
	}
	return Fields{"type": "ask", "id": id, "questions": list}, texts
}

func askAnswer(texts map[string]string, v any) agent.Answer {
	m, _ := v.(map[string]any)
	out := agent.Answer{Answers: map[string]string{}}
	for text, a := range m {
		s, ok := a.(string)
		if id := texts[text]; ok && id != "" {
			out.Answers[id] = s
		}
	}
	return out
}
