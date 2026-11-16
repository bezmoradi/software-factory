package codex

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/agent"
)

type requestKind int

const (
	kindCommand requestKind = iota + 1
	kindFile
	kindElicitation
	kindPermissions
	kindQuestion
)

const (
	maxQuestionCount = 8
	maxQuestionText  = 1000
	maxHeaderText    = 120
	maxChoices       = 10
	maxChoiceText    = 200
	maxChoiceNote    = 500
	maxAnswerText    = 2000
	userNote         = "user_note: "
	secretAsked      = "Codex asked a question that wants a secret answer. The page doesn't take secrets, so the question went back unanswered."
)

var (
	allowOnce  = agent.Option{ID: decisionAccept, Kind: "allow_once", Name: "Yes"}
	rejectOnce = agent.Option{ID: decisionDecline, Kind: "reject_once", Name: "No"}
)

type asked struct {
	question agent.Question
	labels   map[string]string
}

type pending struct {
	id      string
	rpcID   json.RawMessage
	kind    requestKind
	turnID  string
	whole   bool
	item    *item
	version int
	tool    agent.Tool
	agent   string
	foreign bool
	asked   []asked
}

func (p *pending) options() []agent.Option {
	if p.whole {
		return []agent.Option{allowOnce, rejectOnce}
	}
	return []agent.Option{rejectOnce}
}

func (p *pending) choice(a agent.Answer) (bool, error) {
	if a.OptionID == "" {
		return a.Allow, nil
	}
	for _, o := range p.options() {
		if o.ID != a.OptionID {
			continue
		}
		allow := o.ID == decisionAccept
		if a.Allow && !allow {
			return false, ErrBadOption
		}
		return allow, nil
	}
	return false, ErrBadOption
}

func (p *pending) answered(given map[string]string) (any, error) {
	if len(given) == 0 {
		return nil, ErrBadOption
	}
	out := make(map[string]userInputAnswer, len(p.asked))
	for _, q := range p.asked {
		out[q.question.ID] = userInputAnswer{Answers: []string{}}
	}
	for id, text := range given {
		var q *asked
		for i := range p.asked {
			if p.asked[i].question.ID == id {
				q = &p.asked[i]
			}
		}
		text = strings.TrimSpace(text)
		if q == nil || text == "" || len(text) > maxAnswerText || !utf8.ValidString(text) {
			return nil, ErrBadOption
		}
		if label, ok := q.labels[text]; ok {
			out[id] = userInputAnswer{Answers: []string{label}}
		} else {
			out[id] = userInputAnswer{Answers: []string{userNote + text}}
		}
	}
	return outResult{ID: p.rpcID, Result: userInputReply{Answers: out}}, nil
}

func (p *pending) reply(allow, stop bool) any {
	switch p.kind {
	case kindCommand, kindFile:
		d := decisionDecline
		if allow {
			d = decisionAccept
		} else if stop {
			d = decisionCancel
		}
		return outResult{ID: p.rpcID, Result: decisionReply{Decision: d}}
	case kindElicitation:
		a := decisionDecline
		if allow {
			a = decisionAccept
		} else if stop {
			a = decisionCancel
		}
		return outResult{ID: p.rpcID, Result: elicitationReply{Action: a}}
	case kindQuestion:
		return outResult{ID: p.rpcID, Result: userInputReply{Answers: map[string]userInputAnswer{}}}
	}
	return outResult{ID: p.rpcID, Result: permissionsReply{Scope: "turn"}}
}

func (c *Client) serverRequest(env envelope) {
	id, ok := validID(env.ID)
	if !ok {
		c.log.Warn("codex: request with an invalid id dropped")
		return
	}
	c.mu.Lock()
	sub, ours := c.threadOfLocked(env.Params)
	c.mu.Unlock()
	req := &pending{rpcID: id, agent: sub, foreign: !ours}
	switch env.Method {
	case methodCommandApproval:
		c.commandApproval(req, env.Params)
	case methodFileApproval:
		c.fileApproval(req, env.Params)
	case methodElicitation:
		c.elicitation(req, env.Params)
	case methodPermissions:
		c.permissions(req, env.Params)
	case methodUserInput:
		c.userInput(req, env.Params)
	case methodLegacyExec, methodLegacyPatch:
		r := legacyReply{}
		r.Decision.Denied.Rejection = legacyRejection
		c.reply(outResult{ID: id, Result: r})
		c.log.Info("codex: legacy approval declined", "method", env.Method)
	case methodDynamicTool, methodTokenRefresh, methodAttestation:
		c.reply(outError{ID: id, Error: rpcError{Code: codeNotSupported, Message: notSupportedText}})
		c.log.Info("codex: request refused", "method", env.Method)
	default:
		c.reply(outError{ID: id, Error: rpcError{Code: codeNotSupported, Message: notSupportedText}})
		c.noteUnknown("request")
	}
}

func (c *Client) commandApproval(req *pending, params json.RawMessage) {
	var p struct {
		Kind                   string            `json:"kind"`
		TurnID                 string            `json:"turnId"`
		ItemID                 string            `json:"itemId"`
		Reason                 *string           `json:"reason"`
		Command                *string           `json:"command"`
		Cwd                    *string           `json:"cwd"`
		NetworkApprovalContext json.RawMessage   `json:"networkApprovalContext"`
		AdditionalPermissions  json.RawMessage   `json:"additionalPermissions"`
		AvailableDecisions     []json.RawMessage `json:"availableDecisions"`
	}
	req.kind = kindCommand
	if json.Unmarshal(params, &p) != nil {
		c.refuse(req, "command")
		return
	}
	req.turnID = p.TurnID
	command := ""
	if p.Command != nil {
		command = *p.Command
	}
	req.whole = (p.Kind == "" || p.Kind == commandKind) &&
		strings.TrimSpace(command) != "" &&
		isNull(p.NetworkApprovalContext) &&
		isNull(p.AdditionalPermissions) &&
		offersAccept(p.AvailableDecisions)
	input := map[string]any{"command": command}
	if p.Cwd != nil {
		input["cwd"] = *p.Cwd
	}
	if p.Reason != nil {
		input["reason"] = *p.Reason
	}
	if !isNull(p.NetworkApprovalContext) {
		input["network"] = p.NetworkApprovalContext
	}
	if !isNull(p.AdditionalPermissions) {
		input["additionalPermissions"] = p.AdditionalPermissions
	}
	if p.Kind != "" && p.Kind != commandKind {
		input["kind"] = p.Kind
	}
	title := command
	if title == "" {
		title = "Run a command"
	}
	req.tool = agent.Tool{ID: p.ItemID, Title: title, Kind: "execute", Input: mustJSON(input)}
	c.register(req, "command")
}

func offersAccept(decisions []json.RawMessage) bool {
	if decisions == nil {
		return true
	}
	for _, d := range decisions {
		var s string
		if json.Unmarshal(d, &s) == nil && s == decisionAccept {
			return true
		}
	}
	return false
}

func (c *Client) fileApproval(req *pending, params json.RawMessage) {
	var p struct {
		TurnID    string  `json:"turnId"`
		ItemID    string  `json:"itemId"`
		Reason    *string `json:"reason"`
		GrantRoot *string `json:"grantRoot"`
	}
	req.kind = kindFile
	if json.Unmarshal(params, &p) != nil {
		c.refuse(req, "file")
		return
	}
	req.turnID = p.TurnID
	input := map[string]any{}
	if p.Reason != nil {
		input["reason"] = *p.Reason
	}
	grant := p.GrantRoot != nil && *p.GrantRoot != ""
	if grant {
		input["grantRoot"] = *p.GrantRoot
	}
	c.mu.Lock()
	it := c.items[itemKey(req.agent, p.ItemID)]
	if it != nil && it.kind == itemFile {
		req.item = it
		req.version = it.version
		input["changes"] = it.changes
		req.whole = len(it.changes) > 0 && !it.tooBig && !grant
		req.tool = agent.Tool{ID: p.ItemID, Title: changesTitle(it.changes), Kind: "edit"}
	} else {
		req.tool = agent.Tool{ID: p.ItemID, Title: "Change files", Kind: "edit"}
	}
	req.tool.Input = mustJSON(input)
	c.mu.Unlock()
	c.register(req, "file")
}

func (c *Client) elicitation(req *pending, params json.RawMessage) {
	var p struct {
		ServerName      string          `json:"serverName"`
		TurnID          *string         `json:"turnId"`
		Mode            string          `json:"mode"`
		Message         string          `json:"message"`
		URL             *string         `json:"url"`
		RequestedSchema json.RawMessage `json:"requestedSchema"`
		Meta            json.RawMessage `json:"_meta"`
	}
	req.kind = kindElicitation
	if json.Unmarshal(params, &p) != nil {
		c.refuse(req, "elicitation")
		return
	}
	if p.TurnID != nil {
		req.turnID = *p.TurnID
	}
	var meta struct {
		Kind       string          `json:"codex_approval_kind"`
		ToolName   string          `json:"tool_name"`
		ToolParams json.RawMessage `json:"tool_params"`
	}
	if !isNull(p.Meta) && json.Unmarshal(p.Meta, &meta) != nil {
		meta.Kind = ""
	}
	req.whole = p.Mode == elicitModeForm &&
		meta.Kind == elicitToolCall &&
		p.ServerName != "" &&
		meta.ToolName != "" &&
		isObject(meta.ToolParams) &&
		emptyForm(p.RequestedSchema)
	input := map[string]any{"server": p.ServerName, "message": p.Message}
	title := p.ServerName
	if meta.Kind == elicitToolCall && meta.ToolName != "" {
		input["tool"] = meta.ToolName
		input["arguments"] = meta.ToolParams
		title = p.ServerName + ": " + meta.ToolName
	}
	if p.URL != nil {
		input["url"] = *p.URL
	}
	if !req.whole && !isNull(p.RequestedSchema) {
		input["requestedSchema"] = p.RequestedSchema
	}
	req.id = newID()
	req.tool = agent.Tool{ID: req.id, Title: title, Kind: "mcp", Input: mustJSON(input)}
	c.register(req, "elicitation")
}

func emptyForm(raw json.RawMessage) bool {
	var s struct {
		Type       string                     `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if json.Unmarshal(raw, &s) != nil {
		return false
	}
	return s.Type == "object" && len(s.Properties) == 0 && len(s.Required) == 0
}

func (c *Client) permissions(req *pending, params json.RawMessage) {
	var p struct {
		TurnID      string          `json:"turnId"`
		ItemID      string          `json:"itemId"`
		Cwd         *string         `json:"cwd"`
		Reason      *string         `json:"reason"`
		Permissions json.RawMessage `json:"permissions"`
	}
	req.kind = kindPermissions
	if json.Unmarshal(params, &p) != nil {
		c.refuse(req, "permissions")
		return
	}
	req.turnID = p.TurnID
	input := map[string]any{"permissions": p.Permissions}
	if p.Cwd != nil {
		input["cwd"] = *p.Cwd
	}
	if p.Reason != nil {
		input["reason"] = *p.Reason
	}
	req.tool = agent.Tool{ID: p.ItemID, Title: "Grant extra permissions", Kind: "permissions", Input: mustJSON(input)}
	c.register(req, "permissions")
}

func (c *Client) userInput(req *pending, params json.RawMessage) {
	var p struct {
		TurnID    string `json:"turnId"`
		ItemID    string `json:"itemId"`
		Questions []struct {
			ID       string `json:"id"`
			Header   string `json:"header"`
			Question string `json:"question"`
			IsSecret bool   `json:"isSecret"`
			Options  []struct {
				Label       string `json:"label"`
				Description string `json:"description"`
			} `json:"options"`
		} `json:"questions"`
	}
	req.kind = kindQuestion
	if json.Unmarshal(params, &p) != nil || len(p.Questions) == 0 || len(p.Questions) > maxQuestionCount {
		c.refuse(req, "question")
		return
	}
	req.turnID = p.TurnID
	seen := map[string]bool{}
	for i, q := range p.Questions {
		if q.IsSecret {
			c.reply(req.reply(false, false))
			c.mu.Lock()
			c.emitLocked(agent.Event{Type: agent.Notice, Text: secretAsked})
			c.mu.Unlock()
			c.log.Info("codex: question for a secret answered empty")
			return
		}
		if q.ID == "" || len(q.ID) > maxIDLen || seen[q.ID] || len(q.Options) > maxChoices {
			c.refuse(req, "question")
			return
		}
		seen[q.ID] = true
		text := firstText(clipTo(q.Question, maxQuestionText), clipTo(q.Header, maxHeaderText), "Question "+strconv.Itoa(i+1))
		a := asked{question: agent.Question{ID: q.ID, Header: clipTo(q.Header, maxHeaderText), Text: text}, labels: map[string]string{}}
		for _, o := range q.Options {
			label := strings.TrimSpace(clipTo(o.Label, maxChoiceText))
			if label == "" || a.labels[label] != "" {
				continue
			}
			a.labels[label] = o.Label
			a.question.Choices = append(a.question.Choices, agent.Choice{Label: label, Description: clipTo(o.Description, maxChoiceNote)})
		}
		req.asked = append(req.asked, a)
	}
	req.whole = true
	req.tool = agent.Tool{ID: p.ItemID, Title: "Codex asks", Kind: "question"}
	c.register(req, "question")
}

func clipTo(s string, n int) string {
	s = strings.ToValidUTF8(s, "")
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}

func firstText(list ...string) string {
	for _, s := range list {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func (c *Client) refuse(req *pending, kind string) {
	c.reply(req.reply(false, false))
	c.log.Warn("codex: unreadable request declined", "kind", kind)
}

func (c *Client) register(req *pending, kind string) {
	if req.foreign {
		c.reply(req.reply(false, false))
		c.log.Warn("codex: request from another thread declined", "kind", kind)
		return
	}
	if req.id == "" {
		req.id = newID()
	}
	req.tool.Whole = req.whole
	c.mu.Lock()
	key := string(req.rpcID)
	if earlier := c.pending[c.byRPC[key]]; earlier != nil {
		c.withdrawLocked(func(p *pending) bool { return p == earlier })
		c.mu.Unlock()
		c.reply(req.reply(false, false))
		c.log.Warn("codex: repeated request id declined", "kind", kind)
		return
	}
	if c.finished || c.closing || len(c.pending) >= maxPending || (req.agent != "" && c.subs[req.agent] == nil) {
		c.mu.Unlock()
		c.reply(req.reply(false, false))
		c.log.Warn("codex: request declined without a card", "kind", kind)
		return
	}
	c.pending[req.id] = req
	c.byRPC[key] = req.id
	r := &agent.Request{ID: req.id, Tool: req.tool, Options: req.options()}
	for _, a := range req.asked {
		r.Questions = append(r.Questions, a.question)
	}
	c.emitLocked(agent.Event{Type: agent.Permission, Request: r, Agent: req.agent})
	c.mu.Unlock()
	c.log.Info("codex: request", "kind", kind, "whole", req.whole)
}

func (c *Client) dropPendingLocked(p *pending) {
	delete(c.pending, p.id)
	delete(c.byRPC, string(p.rpcID))
}

func (c *Client) withdrawLocked(match func(*pending) bool) []*pending {
	var out []*pending
	for _, p := range c.pending {
		if !match(p) {
			continue
		}
		c.dropPendingLocked(p)
		out = append(out, p)
		tool := p.tool
		c.emitLocked(agent.Event{Type: agent.Withdrawn, Request: &agent.Request{ID: p.id, Tool: tool}, Reason: ReasonWithdrawn})
	}
	return out
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}
