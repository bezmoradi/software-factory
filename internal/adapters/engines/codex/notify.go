package codex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/agent"
)

const (
	itemText = iota + 1
	itemThought
	itemCommand
	itemFile
	itemOther
)

const (
	maxTracked = 1 << 20
	maxChanges = 1000
	maxOutput  = 64 << 10
	maxUnknown = 20
)

type fileChange struct {
	Path     string  `json:"path"`
	Kind     string  `json:"kind"`
	MovePath *string `json:"movePath,omitempty"`
	Diff     string  `json:"diff"`
}

type item struct {
	kind      int
	streamed  []byte
	sawDelta  bool
	untracked bool
	changes   []fileChange
	tooBig    bool
	version   int
}

func (c *Client) handleLine(line []byte) {
	if len(bytes.TrimSpace(line)) == 0 {
		return
	}
	var env envelope
	if err := json.Unmarshal(line, &env); err != nil {
		c.log.Warn("codex: malformed line dropped")
		return
	}
	c.mu.Lock()
	gone := c.finished
	c.mu.Unlock()
	if gone {
		return
	}
	hasID := !isNull(env.ID)
	switch {
	case env.Method != "" && hasID:
		c.serverRequest(env)
	case env.Method != "":
		c.notification(env)
	case hasID:
		c.response(env)
	default:
		c.log.Warn("codex: message without method or id dropped")
	}
}

func (c *Client) response(env envelope) {
	var id int64
	if json.Unmarshal(env.ID, &id) != nil {
		c.log.Warn("codex: response with an unknown id dropped")
		return
	}
	c.mu.Lock()
	ch := c.calls[id]
	delete(c.calls, id)
	c.mu.Unlock()
	if ch == nil {
		c.log.Warn("codex: response with an unknown id dropped")
		return
	}
	ch <- env
}

func (c *Client) noteUnknown(what string) {
	c.mu.Lock()
	c.unknown++
	n := c.unknown
	c.mu.Unlock()
	if n <= maxUnknown {
		c.log.Info("codex: unknown method ignored", "kind", what)
	}
}

func (c *Client) notification(env envelope) {
	var head struct {
		ThreadID *string `json:"threadId"`
	}
	if json.Unmarshal(env.Params, &head) != nil && !isNull(env.Params) {
		c.log.Warn("codex: unreadable notification dropped", "method", knownMethod(env.Method))
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if head.ThreadID != nil && c.threadID != "" && *head.ThreadID != c.threadID {
		return
	}
	p := env.Params
	switch env.Method {
	case methodAgentDelta:
		c.deltaLocked(p, itemText, agent.Text)
	case methodReasoningSummary, methodReasoningText:
		c.deltaLocked(p, itemThought, agent.Thought)
	case methodItemStarted:
		c.itemLocked(p, false)
	case methodItemCompleted:
		c.itemLocked(p, true)
	case methodPatchUpdated:
		c.patchLocked(p)
	case methodTurnStarted:
		c.turnStartedLocked(p)
	case methodTurnCompleted:
		c.turnCompletedLocked(p)
	case methodPlanUpdated:
		c.planLocked(p)
	case methodTokenUsage:
		c.usageLocked(p)
	case methodSettingsUpdated:
		var n struct {
			ThreadSettings struct {
				ApprovalPolicy    json.RawMessage `json:"approvalPolicy"`
				ApprovalsReviewer json.RawMessage `json:"approvalsReviewer"`
			} `json:"threadSettings"`
		}
		if json.Unmarshal(p, &n) != nil {
			c.violated = true
			c.emitLocked(agent.Event{Type: agent.Failed, Reason: ReasonApprovalPolicy, Text: "Codex sent thread settings this client can't read; stop this session."})
			return
		}
		c.checkSettingsLocked(n.ThreadSettings.ApprovalPolicy, n.ThreadSettings.ApprovalsReviewer)
	case methodRequestResolved:
		var n struct {
			RequestID json.RawMessage `json:"requestId"`
		}
		if json.Unmarshal(p, &n) != nil {
			return
		}
		if id, ok := validID(n.RequestID); ok {
			ours := c.byRPC[string(id)]
			c.withdrawLocked(func(q *pending) bool { return q.id == ours })
		}
	case methodError:
		c.errorLocked(p)
	case methodThreadClosed:
		c.log.Info("codex: thread closed")
		go c.finish(ReasonThreadClosed)
	case methodThreadStatus:
		var n struct {
			Status struct {
				Type string `json:"type"`
			} `json:"status"`
		}
		if json.Unmarshal(p, &n) == nil && n.Status.Type == "systemError" {
			c.emitLocked(agent.Event{Type: agent.Failed, Reason: ReasonAgentError, Text: "Codex reported a system error."})
		}
	default:
		if quietNotifications[env.Method] {
			return
		}
		c.unknown++
		if c.unknown <= maxUnknown {
			c.log.Info("codex: unknown method ignored", "kind", "notification")
		}
	}
}

func knownMethod(m string) string {
	if quietNotifications[m] {
		return m
	}
	switch m {
	case methodAgentDelta, methodReasoningSummary, methodReasoningText, methodItemStarted, methodItemCompleted,
		methodPatchUpdated, methodTurnStarted, methodTurnCompleted, methodPlanUpdated, methodTokenUsage,
		methodSettingsUpdated, methodRequestResolved, methodError, methodThreadClosed, methodThreadStatus:
		return m
	}
	return "unknown"
}

func (c *Client) checkSettingsLocked(policy, reviewer json.RawMessage) {
	mode := policyName(policy)
	who := reviewerName(reviewer)
	c.emitLocked(agent.Event{Type: agent.Mode, Mode: mode})
	if mode == policyOnRequest && who == reviewerUser {
		return
	}
	c.violated = true
	c.emitLocked(agent.Event{
		Type:   agent.Failed,
		Reason: ReasonApprovalPolicy,
		Text:   fmt.Sprintf("Codex reports approval policy %q with reviewer %q instead of %q with %q; stop this session.", mode, who, policyOnRequest, reviewerUser),
	})
	c.log.Warn("codex: approval policy changed", "policy", mode, "reviewer", who)
}

func policyName(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		switch s {
		case policyOnRequest, "untrusted", "never", "on-failure":
			return s
		}
		return "unknown"
	}
	var g struct {
		Granular json.RawMessage `json:"granular"`
	}
	if json.Unmarshal(raw, &g) == nil && !isNull(g.Granular) {
		return "granular"
	}
	return "unknown"
}

func reviewerName(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		switch s {
		case reviewerUser, "auto_review", "guardian_subagent":
			return s
		}
	}
	return "unknown"
}

func (c *Client) trackLocked(id string, kind int) *item {
	it := c.items[id]
	if it != nil {
		return it
	}
	if len(c.items) >= maxItems {
		kept := map[string]*item{}
		for _, p := range c.pending {
			if p.item != nil {
				for k, v := range c.items {
					if v == p.item {
						kept[k] = v
					}
				}
			}
		}
		c.items = kept
	}
	it = &item{kind: kind}
	c.items[id] = it
	return it
}

func (c *Client) deltaLocked(p json.RawMessage, kind int, typ agent.EventType) {
	var n struct {
		ItemID string `json:"itemId"`
		Delta  string `json:"delta"`
	}
	if json.Unmarshal(p, &n) != nil || n.Delta == "" {
		return
	}
	it := c.trackLocked(n.ItemID, kind)
	it.sawDelta = true
	if !it.untracked && len(it.streamed)+len(n.Delta) <= maxTracked {
		it.streamed = append(it.streamed, n.Delta...)
	} else {
		it.untracked = true
		it.streamed = nil
	}
	c.emitLocked(agent.Event{Type: typ, Text: n.Delta})
}

func (c *Client) finalTextLocked(id, text string, kind int, typ agent.EventType) {
	it := c.items[id]
	rest := text
	if it != nil && it.sawDelta {
		if it.untracked || !strings.HasPrefix(text, string(it.streamed)) {
			return
		}
		rest = text[len(it.streamed):]
	}
	if it == nil {
		it = c.trackLocked(id, kind)
	}
	it.sawDelta = true
	it.streamed = append(it.streamed, rest...)
	if rest != "" {
		c.emitLocked(agent.Event{Type: typ, Text: rest})
	}
}

type threadItem struct {
	Type             string          `json:"type"`
	ID               string          `json:"id"`
	Text             string          `json:"text"`
	Summary          []string        `json:"summary"`
	Content          json.RawMessage `json:"content"`
	Command          string          `json:"command"`
	Cwd              string          `json:"cwd"`
	Status           string          `json:"status"`
	AggregatedOutput *string         `json:"aggregatedOutput"`
	ExitCode         *int            `json:"exitCode"`
	Changes          json.RawMessage `json:"changes"`
	Server           string          `json:"server"`
	Tool             json.RawMessage `json:"tool"`
	Arguments        json.RawMessage `json:"arguments"`
	Error            *struct {
		Message string `json:"message"`
	} `json:"error"`
	Query string `json:"query"`
}

func (c *Client) itemLocked(p json.RawMessage, completed bool) {
	var n struct {
		Item threadItem `json:"item"`
	}
	if json.Unmarshal(p, &n) != nil || n.Item.ID == "" {
		c.log.Warn("codex: unreadable item dropped")
		return
	}
	x := n.Item
	switch x.Type {
	case "agentMessage":
		if completed {
			c.finalTextLocked(x.ID, x.Text, itemText, agent.Text)
		}
	case "reasoning":
		if completed {
			c.finalTextLocked(x.ID, reasoningText(x), itemThought, agent.Thought)
		}
	case "commandExecution":
		c.commandItemLocked(x, completed)
	case "fileChange":
		c.fileItemLocked(x, completed)
	case "mcpToolCall":
		tool := stringOf(x.Tool)
		c.toolLocked(completed, agent.Tool{
			ID:     x.ID,
			Title:  x.Server + ": " + tool,
			Kind:   "mcp",
			Status: statusName(x.Status),
			Input:  mustJSON(map[string]any{"server": x.Server, "tool": tool, "arguments": x.Arguments}),
			Whole:  true,
		}, errorText(x))
	case "webSearch":
		c.toolLocked(completed, agent.Tool{ID: x.ID, Title: x.Query, Kind: "search", Status: doneStatus(completed)}, "")
	case "userMessage", "hookPrompt", "plan", "contextCompaction", "functionCallOutput":
	default:
		c.toolLocked(completed, agent.Tool{ID: x.ID, Title: x.Type, Kind: "other", Status: doneStatus(completed)}, "")
	}
}

func reasoningText(x threadItem) string {
	if len(x.Summary) > 0 {
		return strings.Join(x.Summary, "\n\n")
	}
	var parts []string
	if json.Unmarshal(x.Content, &parts) == nil {
		return strings.Join(parts, "\n\n")
	}
	return ""
}

func stringOf(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return ""
}

func errorText(x threadItem) string {
	if x.Error != nil {
		return x.Error.Message
	}
	return ""
}

func doneStatus(completed bool) string {
	if completed {
		return "completed"
	}
	return "in_progress"
}

func statusName(s string) string {
	switch s {
	case "inProgress":
		return "in_progress"
	case "completed", "failed", "declined":
		return s
	}
	return ""
}

func (c *Client) toolLocked(completed bool, tool agent.Tool, text string) {
	if completed {
		c.emitLocked(agent.Event{Type: agent.ToolUpdate, Tool: &tool, Text: clip(text)})
		return
	}
	c.emitLocked(agent.Event{Type: agent.ToolCall, Tool: &tool})
}

func (c *Client) commandItemLocked(x threadItem, completed bool) {
	tool := agent.Tool{
		ID:     x.ID,
		Title:  x.Command,
		Kind:   "execute",
		Status: statusName(x.Status),
		Input:  mustJSON(map[string]any{"command": x.Command, "cwd": x.Cwd}),
		Whole:  x.Command != "",
	}
	if !completed {
		c.trackLocked(x.ID, itemCommand)
		c.emitLocked(agent.Event{Type: agent.ToolCall, Tool: &tool})
		return
	}
	text := ""
	if x.AggregatedOutput != nil {
		text = *x.AggregatedOutput
	}
	c.emitLocked(agent.Event{Type: agent.ToolUpdate, Tool: &tool, Text: clip(text)})
}

func (c *Client) fileItemLocked(x threadItem, completed bool) {
	changes, ok := parseChanges(x.Changes)
	it := c.trackLocked(x.ID, itemFile)
	if it.kind == itemFile {
		c.setChangesLocked(it, changes, ok)
	}
	tool := agent.Tool{
		ID:     x.ID,
		Title:  changesTitle(changes),
		Kind:   "edit",
		Status: statusName(x.Status),
		Input:  mustJSON(map[string]any{"changes": changes}),
		Whole:  ok && len(changes) > 0,
	}
	if completed {
		c.emitLocked(agent.Event{Type: agent.ToolUpdate, Tool: &tool})
		return
	}
	c.emitLocked(agent.Event{Type: agent.ToolCall, Tool: &tool})
}

func (c *Client) setChangesLocked(it *item, changes []fileChange, ok bool) {
	it.changes = changes
	it.tooBig = !ok
	it.version++
}

func (c *Client) patchLocked(p json.RawMessage) {
	var n struct {
		ItemID  string          `json:"itemId"`
		Changes json.RawMessage `json:"changes"`
	}
	if json.Unmarshal(p, &n) != nil {
		return
	}
	changes, ok := parseChanges(n.Changes)
	it := c.trackLocked(n.ItemID, itemFile)
	if it.kind != itemFile {
		return
	}
	c.setChangesLocked(it, changes, ok)
	tool := agent.Tool{
		ID:     n.ItemID,
		Title:  changesTitle(changes),
		Kind:   "edit",
		Input:  mustJSON(map[string]any{"changes": changes}),
		Whole:  ok && len(changes) > 0,
		Status: "in_progress",
	}
	c.emitLocked(agent.Event{Type: agent.ToolUpdate, Tool: &tool})
}

func parseChanges(raw json.RawMessage) ([]fileChange, bool) {
	var list []struct {
		Path string `json:"path"`
		Kind struct {
			Type     string  `json:"type"`
			MovePath *string `json:"move_path"`
		} `json:"kind"`
		Diff *string `json:"diff"`
	}
	if json.Unmarshal(raw, &list) != nil || len(list) > maxChanges {
		return nil, false
	}
	out := make([]fileChange, 0, len(list))
	ok := true
	for _, ch := range list {
		switch ch.Kind.Type {
		case "add", "delete", "update":
		default:
			ok = false
		}
		if ch.Path == "" || ch.Diff == nil {
			ok = false
		}
		diff := ""
		if ch.Diff != nil {
			diff = *ch.Diff
		}
		out = append(out, fileChange{Path: ch.Path, Kind: ch.Kind.Type, MovePath: ch.Kind.MovePath, Diff: diff})
	}
	return out, ok
}

func changesTitle(changes []fileChange) string {
	switch len(changes) {
	case 0:
		return "Change files"
	case 1:
		return changes[0].Path
	}
	return fmt.Sprintf("%d files", len(changes))
}

func (c *Client) turnStartedLocked(p json.RawMessage) {
	var n struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if json.Unmarshal(p, &n) != nil || n.Turn.ID == "" || len(n.Turn.ID) > maxIDLen || c.doneTurns[n.Turn.ID] {
		return
	}
	c.turnID = n.Turn.ID
}

func (c *Client) turnCompletedLocked(p json.RawMessage) {
	var n struct {
		Turn struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"turn"`
	}
	if json.Unmarshal(p, &n) != nil || n.Turn.ID == "" || len(n.Turn.ID) > maxIDLen {
		c.log.Warn("codex: unreadable turn end dropped")
		return
	}
	id := n.Turn.ID
	if len(c.doneTurns) >= 64 {
		c.doneTurns = map[string]bool{}
	}
	c.doneTurns[id] = true
	if c.turnID == id {
		c.turnID = ""
	}
	c.withdrawLocked(func(q *pending) bool { return q.turnID == id })
	kept := map[string]*item{}
	for k, v := range c.items {
		for _, q := range c.pending {
			if q.item == v {
				kept[k] = v
			}
		}
	}
	c.items = kept
	text := ""
	if n.Turn.Error != nil {
		text = n.Turn.Error.Message
	}
	c.emitLocked(agent.Event{Type: agent.TurnEnded, Reason: turnStatus(n.Turn.Status), Text: clip(text)})
}

func turnStatus(s string) string {
	switch s {
	case "completed", "interrupted", "failed":
		return s
	}
	return "unknown"
}

func (c *Client) planLocked(p json.RawMessage) {
	var n struct {
		Explanation *string `json:"explanation"`
		Plan        []struct {
			Step   string `json:"step"`
			Status string `json:"status"`
		} `json:"plan"`
	}
	if json.Unmarshal(p, &n) != nil {
		return
	}
	var b strings.Builder
	if n.Explanation != nil && *n.Explanation != "" {
		b.WriteString(*n.Explanation)
		b.WriteString("\n")
	}
	for _, s := range n.Plan {
		mark := "[ ]"
		switch s.Status {
		case "completed":
			mark = "[x]"
		case "inProgress":
			mark = "[~]"
		}
		b.WriteString("- " + mark + " " + s.Step + "\n")
	}
	c.emitLocked(agent.Event{Type: agent.Plan, Text: clip(b.String())})
}

func (c *Client) usageLocked(p json.RawMessage) {
	var n struct {
		TokenUsage struct {
			Total struct {
				InputTokens  int64 `json:"inputTokens"`
				OutputTokens int64 `json:"outputTokens"`
			} `json:"total"`
		} `json:"tokenUsage"`
	}
	if json.Unmarshal(p, &n) != nil {
		return
	}
	c.emitLocked(agent.Event{Type: agent.Usage, Spend: &agent.Spend{
		InputTokens:  max(n.TokenUsage.Total.InputTokens, 0),
		OutputTokens: max(n.TokenUsage.Total.OutputTokens, 0),
	}})
}

func (c *Client) errorLocked(p json.RawMessage) {
	var n struct {
		Error struct {
			Message        string          `json:"message"`
			CodexErrorInfo json.RawMessage `json:"codexErrorInfo"`
		} `json:"error"`
		WillRetry bool `json:"willRetry"`
	}
	if json.Unmarshal(p, &n) != nil || n.WillRetry {
		return
	}
	if stringOf(n.Error.CodexErrorInfo) == errorUnauthorized {
		c.emitLocked(agent.Event{Type: agent.Failed, Reason: ReasonNotSignedIn, Text: signInHelp})
		return
	}
	c.emitLocked(agent.Event{Type: agent.Failed, Reason: ReasonTurnError, Text: clip(n.Error.Message)})
}

func clip(s string) string {
	if len(s) <= maxOutput {
		return s
	}
	return strings.ToValidUTF8(s[:maxOutput], "")
}
