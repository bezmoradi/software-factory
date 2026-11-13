package acp

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/agent"
)

type toolState struct {
	id     string
	title  string
	kind   string
	status string
	input  json.RawMessage
}

var toolKinds = map[string]bool{
	"read": true, "edit": true, "delete": true, "move": true, "search": true,
	"execute": true, "think": true, "fetch": true, "switch_mode": true, "other": true,
}

var toolStatuses = map[string]bool{"pending": true, "in_progress": true, "completed": true, "failed": true}

func (c *Client) onUpdate(params json.RawMessage) {
	var n struct {
		SessionID string          `json:"sessionId"`
		Update    json.RawMessage `json:"update"`
	}
	if json.Unmarshal(params, &n) != nil {
		c.warn("invalid-update")
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(n.Update, &fields) != nil || fields == nil {
		c.warn("invalid-update")
		return
	}
	kind, _ := stringField(fields, "sessionUpdate")
	c.mu.Lock()
	defer c.signal()
	defer c.mu.Unlock()
	if c.sessionID == "" || n.SessionID != c.sessionID {
		c.warn("update-wrong-session")
		return
	}
	switch kind {
	case "agent_message_chunk":
		if text := chunkText(fields); text != "" {
			c.emitLocked(agent.Event{Type: agent.Text, Text: text})
		}
	case "agent_thought_chunk":
		if text := chunkText(fields); text != "" {
			c.emitLocked(agent.Event{Type: agent.Thought, Text: text})
		}
	case "tool_call", "tool_call_update":
		tool, text, ok := c.applyToolLocked(n.Update, kind == "tool_call")
		if !ok {
			c.warn("invalid-update")
			return
		}
		t := agent.ToolCall
		if kind == "tool_call_update" {
			t = agent.ToolUpdate
		}
		c.emitLocked(agent.Event{Type: t, Tool: &tool, Text: text})
	case "plan":
		c.emitLocked(agent.Event{Type: agent.Plan, Text: planText(fields)})
	case "available_commands_update":
		c.emitLocked(agent.Event{Type: agent.Commands, Commands: commands(fields)})
	case "current_mode_update":
		if id, ok := stringField(fields, "currentModeId"); ok && id != "" {
			c.mode.current = id
			c.emitLocked(agent.Event{Type: agent.Mode, Mode: id})
		}
	case "config_option_update":
		c.onConfigOptionsLocked(fields["configOptions"])
	case "usage_update":
		if spend := usage(fields); spend != nil {
			c.emitLocked(agent.Event{Type: agent.Usage, Spend: spend})
		}
	default:
		c.note("ignored-update")
	}
}

func stringField(fields map[string]json.RawMessage, key string) (string, bool) {
	raw, ok := fields[key]
	if !ok {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

func chunkText(fields map[string]json.RawMessage) string {
	return blockText(fields["content"])
}

func blockText(raw json.RawMessage) string {
	var b struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &b) != nil || b.Type != "text" {
		return ""
	}
	return b.Text
}

func (c *Client) applyToolLocked(raw json.RawMessage, create bool) (agent.Tool, string, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return agent.Tool{}, "", false
	}
	id, ok := stringField(fields, "toolCallId")
	if !ok || id == "" {
		return agent.Tool{}, "", false
	}
	st := c.tools[id]
	if st == nil || create {
		if st != nil {
			c.toolBytes -= len(st.input)
		} else {
			c.trackToolLocked(id)
		}
		st = &toolState{id: id, kind: "other", status: "pending"}
		c.tools[id] = st
	}
	if title, ok := stringField(fields, "title"); ok {
		st.title = title
	}
	if kind, ok := stringField(fields, "kind"); ok && toolKinds[kind] {
		st.kind = kind
	}
	if status, ok := stringField(fields, "status"); ok && toolStatuses[status] {
		st.status = status
	}
	if input, ok := fields["rawInput"]; ok {
		input = bytes.TrimSpace(input)
		if len(input) > 0 && !bytes.Equal(input, []byte("null")) {
			c.toolBytes += len(input) - len(st.input)
			st.input = append(json.RawMessage(nil), input...)
		}
	}
	c.evictToolsLocked(id)
	return snapshot(st), toolText(fields["content"]), true
}

func snapshot(st *toolState) agent.Tool {
	return agent.Tool{
		ID:     st.id,
		Title:  st.title,
		Kind:   st.kind,
		Status: st.status,
		Input:  append(json.RawMessage(nil), st.input...),
		Whole:  st.input != nil,
	}
}

func (c *Client) trackToolLocked(id string) {
	c.toolOrder = append(c.toolOrder, id)
}

func (c *Client) evictToolsLocked(keep string) {
	for len(c.toolOrder) > 0 && (len(c.toolOrder) > maxTools || c.toolBytes > maxToolBytes) {
		oldest := c.toolOrder[0]
		if oldest == keep && len(c.toolOrder) == 1 {
			return
		}
		c.toolOrder = c.toolOrder[1:]
		if st := c.tools[oldest]; st != nil {
			c.toolBytes -= len(st.input)
			delete(c.tools, oldest)
		}
	}
}

func (c *Client) cancelToolsLocked() {
	for _, id := range c.toolOrder {
		st := c.tools[id]
		if st == nil || (st.status != "pending" && st.status != "in_progress") {
			continue
		}
		st.status = "cancelled"
		tool := snapshot(st)
		c.emitLocked(agent.Event{Type: agent.ToolUpdate, Tool: &tool})
	}
}

func toolText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var items []struct {
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &items) != nil {
		return ""
	}
	var parts []string
	for _, it := range items {
		if it.Type != "content" {
			continue
		}
		if text := blockText(it.Content); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func planText(fields map[string]json.RawMessage) string {
	var entries []json.RawMessage
	if json.Unmarshal(fields["entries"], &entries) != nil {
		return ""
	}
	var lines []string
	for i, raw := range entries {
		if i >= maxPlanEntries {
			break
		}
		var e struct {
			Content  string `json:"content"`
			Priority string `json:"priority"`
			Status   string `json:"status"`
		}
		if json.Unmarshal(raw, &e) != nil || e.Content == "" {
			continue
		}
		lines = append(lines, "["+e.Status+"] "+strings.ReplaceAll(e.Content, "\n", " ")+" ("+e.Priority+")")
	}
	return strings.Join(lines, "\n")
}

func commands(fields map[string]json.RawMessage) []agent.Command {
	var raw []json.RawMessage
	if json.Unmarshal(fields["availableCommands"], &raw) != nil {
		return nil
	}
	out := make([]agent.Command, 0, len(raw))
	for _, r := range raw {
		if len(out) >= maxCommands {
			break
		}
		var cmd struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		if json.Unmarshal(r, &cmd) != nil || cmd.Name == "" {
			continue
		}
		out = append(out, agent.Command{Name: cmd.Name, Description: cmd.Description})
	}
	return out
}

func usage(fields map[string]json.RawMessage) *agent.Spend {
	var cost struct {
		Amount   *float64 `json:"amount"`
		Currency string   `json:"currency"`
	}
	raw, ok := fields["cost"]
	if !ok || json.Unmarshal(raw, &cost) != nil || cost.Amount == nil || cost.Currency != "USD" {
		return nil
	}
	return &agent.Spend{CostUSD: *cost.Amount, Estimate: true}
}
