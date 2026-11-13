package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"strconv"

	"github.com/bezmoradi/software-factory/internal/core/agent"
)

const (
	kindAllowOnce    = "allow_once"
	kindAllowAlways  = "allow_always"
	kindRejectOnce   = "reject_once"
	kindRejectAlways = "reject_always"
)

type permission struct {
	seq     int
	rpcID   json.RawMessage
	whole   bool
	options []agent.Option
}

type permissionParams struct {
	SessionID string            `json:"sessionId"`
	ToolCall  json.RawMessage   `json:"toolCall"`
	Options   []json.RawMessage `json:"options"`
}

func (c *Client) onPermission(id json.RawMessage, params json.RawMessage) {
	var p permissionParams
	if err := json.Unmarshal(params, &p); err != nil || !hasToolCallID(p.ToolCall) || len(p.Options) > maxOptions {
		c.warn("invalid-permission-request")
		c.replyError(id, codeInvalidParams, "Invalid params")
		return
	}
	options := parseOptions(p.Options)
	c.mu.Lock()
	defer c.signal()
	defer c.mu.Unlock()
	if c.sessionID == "" || p.SessionID != c.sessionID {
		c.warn("permission-wrong-session")
		c.replyError(id, codeInvalidParams, "Invalid params")
		return
	}
	if len(c.perms) >= maxPendingPermissions {
		c.warn("too-many-permission-requests")
		c.replyError(id, codeInternalError, "Too many pending permission requests")
		return
	}
	for _, other := range c.perms {
		if bytes.Equal(other.rpcID, id) {
			c.warn("duplicate-request-id")
			c.replyError(id, codeInvalidRequest, "Invalid request")
			return
		}
	}
	tool, _, ok := c.applyToolLocked(p.ToolCall, false)
	if !ok {
		c.warn("invalid-permission-request")
		c.replyError(id, codeInvalidParams, "Invalid params")
		return
	}
	visible := make([]agent.Option, 0, len(options))
	for _, o := range options {
		if o.Kind == kindAllowAlways || (o.Kind == kindAllowOnce && !tool.Whole) {
			continue
		}
		visible = append(visible, o)
	}
	c.permSeq++
	rid := "perm-" + strconv.Itoa(c.permSeq)
	c.perms[rid] = &permission{seq: c.permSeq, rpcID: append(json.RawMessage(nil), id...), whole: tool.Whole, options: options}
	c.emitLocked(agent.Event{Type: agent.Permission, Request: &agent.Request{ID: rid, Tool: tool, Options: visible}})
	c.note("permission-request")
}

func hasToolCallID(raw json.RawMessage) bool {
	var t struct {
		ToolCallID string `json:"toolCallId"`
	}
	return isObject(raw) && json.Unmarshal(raw, &t) == nil && t.ToolCallID != ""
}

func parseOptions(raw []json.RawMessage) []agent.Option {
	var out []agent.Option
	seen := map[string]bool{}
	for _, r := range raw {
		var o struct {
			OptionID string `json:"optionId"`
			Name     string `json:"name"`
			Kind     string `json:"kind"`
		}
		if json.Unmarshal(r, &o) != nil || o.OptionID == "" || seen[o.OptionID] {
			continue
		}
		seen[o.OptionID] = true
		switch o.Kind {
		case kindAllowOnce, kindAllowAlways, kindRejectOnce, kindRejectAlways:
			out = append(out, agent.Option{ID: o.OptionID, Kind: o.Kind, Name: o.Name})
		}
	}
	return out
}

func (c *Client) Answer(ctx context.Context, requestID string, a agent.Answer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ended {
		return ErrClosed
	}
	p := c.perms[requestID]
	if p == nil {
		return ErrUnknownRequest
	}
	optionID, rejected, err := decide(p, a)
	if err != nil {
		return err
	}
	if err := c.enqueue(permissionReply(p.rpcID, optionID)); err != nil {
		return err
	}
	delete(c.perms, requestID)
	c.note("permission-answer")
	if rejected {
		return ErrNoAllowOnce
	}
	return nil
}

func decide(p *permission, a agent.Answer) (string, bool, error) {
	if a.OptionID != "" {
		o, found := findOption(p.options, a.OptionID)
		switch {
		case !found:
			return "", false, ErrUnknownOption
		case o.Kind == kindAllowAlways:
			return "", false, ErrAllowAlways
		case (o.Kind == kindAllowOnce) != a.Allow:
			return "", false, ErrOptionMismatch
		case a.Allow && !p.whole:
			return "", false, ErrNotWhole
		}
		return o.ID, false, nil
	}
	if !a.Allow {
		return firstOfKind(p.options, kindRejectOnce), false, nil
	}
	if !p.whole {
		return "", false, ErrNotWhole
	}
	if id := firstOfKind(p.options, kindAllowOnce); id != "" {
		return id, false, nil
	}
	return firstOfKind(p.options, kindRejectOnce), true, nil
}

func findOption(options []agent.Option, id string) (agent.Option, bool) {
	for _, o := range options {
		if o.ID == id {
			return o, true
		}
	}
	return agent.Option{}, false
}

func firstOfKind(options []agent.Option, kind string) string {
	for _, o := range options {
		if o.Kind == kind {
			return o.ID
		}
	}
	return ""
}

func permissionReply(id json.RawMessage, optionID string) outResult {
	outcome := map[string]string{"outcome": "cancelled"}
	if optionID != "" {
		outcome = map[string]string{"outcome": "selected", "optionId": optionID}
	}
	return outResult{JSONRPC: "2.0", ID: id, Result: map[string]any{"outcome": outcome}}
}

func (c *Client) cancelPermissionsLocked() {
	ids := make([]string, 0, len(c.perms))
	for id := range c.perms {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return c.perms[ids[i]].seq < c.perms[ids[j]].seq })
	for _, id := range ids {
		_ = c.enqueue(permissionReply(c.perms[id].rpcID, ""))
		c.emitLocked(agent.Event{Type: agent.Withdrawn, Request: &agent.Request{ID: id}, Reason: "withdrawn"})
	}
	c.perms = map[string]*permission{}
}

func (c *Client) onCancelRequest(params json.RawMessage) {
	var p struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if json.Unmarshal(params, &p) != nil || p.RequestID == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for rid, perm := range c.perms {
		if bytes.Equal(perm.rpcID, bytes.TrimSpace(p.RequestID)) {
			_ = c.enqueue(permissionReply(perm.rpcID, ""))
			delete(c.perms, rid)
			c.emitLocked(agent.Event{Type: agent.Withdrawn, Request: &agent.Request{ID: rid}, Reason: "withdrawn"})
			c.note("permission-withdrawn")
			return
		}
	}
}
