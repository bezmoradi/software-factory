package codex

import (
	"cmp"
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/agent"
	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	maxSubagents  = 16
	subReadFor    = 10 * time.Second
	subRunning    = "running"
	subCompleted  = "completed"
	subFailed     = "failed"
	subStopped    = "interrupted"
	toolSpawn     = "spawnAgent"
	activityStart = "started"
)

var subName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _.-]{0,47}$`)

type subagent struct {
	id     string
	name   string
	status string
	turn   string
	order  int
}

func itemKey(sub, id string) string {
	if sub == "" {
		return id
	}
	return sub + "/" + id
}

func cleanName(s string) string {
	s = strings.TrimSpace(s)
	if subName.MatchString(s) {
		return s
	}
	return ""
}

func pathName(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		p = p[i+1:]
	}
	return cleanName(p)
}

func (c *Client) threadOfLocked(params json.RawMessage) (string, bool) {
	var head struct {
		ThreadID *string `json:"threadId"`
	}
	if json.Unmarshal(params, &head) != nil || head.ThreadID == nil || c.threadID == "" || *head.ThreadID == c.threadID {
		return "", true
	}
	if c.subs[*head.ThreadID] != nil {
		return *head.ThreadID, true
	}
	return "", false
}

func (c *Client) adoptLocked(id, name string) {
	if !session.ThreadPattern.MatchString(id) || id == c.threadID {
		return
	}
	if s := c.subs[id]; s != nil {
		if n := cleanName(name); n != "" && n != s.name {
			s.name = n
			c.listLocked()
		}
		return
	}
	if len(c.subs) >= maxSubagents {
		c.log.Warn("codex: subagent not shown, too many in this session")
		return
	}
	c.subCount++
	s := &subagent{id: id, name: cleanName(name), status: subRunning, order: c.subCount}
	if s.name == "" {
		s.name = "subagent " + strconv.Itoa(s.order)
	}
	c.subs[id] = s
	c.listLocked()
	c.log.Info("codex: subagent shown", "count", len(c.subs))
	go c.readSub(id)
}

func (c *Client) readSub(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), subReadFor)
	defer cancel()
	raw, err := c.call(ctx, methodThreadRead, threadReadParams{ThreadID: id})
	if err != nil {
		c.log.Info("codex: subagent thread not read")
		return
	}
	var r struct {
		Thread struct {
			ID             string  `json:"id"`
			ParentThreadID *string `json:"parentThreadId"`
			AgentNickname  *string `json:"agentNickname"`
			AgentRole      *string `json:"agentRole"`
		} `json:"thread"`
	}
	if json.Unmarshal(raw, &r) != nil || r.Thread.ID != id {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.subs[id]
	if s == nil {
		return
	}
	if r.Thread.ParentThreadID != nil && *r.Thread.ParentThreadID != c.threadID {
		delete(c.subs, id)
		for _, p := range c.withdrawLocked(func(p *pending) bool { return p.agent == id }) {
			c.reply(p.reply(false, false))
		}
		c.listLocked()
		c.log.Warn("codex: thread with another parent dropped")
		return
	}
	name := ""
	for _, v := range []*string{r.Thread.AgentNickname, r.Thread.AgentRole} {
		if v != nil && name == "" {
			name = cleanName(*v)
		}
	}
	if name != "" && name != s.name {
		s.name = name
		c.listLocked()
	}
}

func (c *Client) listLocked() {
	list := make([]*subagent, 0, len(c.subs))
	for _, s := range c.subs {
		list = append(list, s)
	}
	slices.SortFunc(list, func(a, b *subagent) int { return cmp.Compare(a.order, b.order) })
	out := make([]agent.Subagent, 0, len(list))
	for _, s := range list {
		out = append(out, agent.Subagent{ID: s.id, Name: s.name, Status: s.status})
	}
	c.emitLocked(agent.Event{Type: agent.Subagents, Agents: out})
}

func (c *Client) statusLocked(s *subagent, status string) {
	if s.status == status {
		return
	}
	s.status = status
	c.listLocked()
}

func (c *Client) spawnedLocked(x threadItem, completed bool) {
	switch x.Type {
	case "collabAgentToolCall":
		if !completed || stringOf(x.Tool) != toolSpawn || x.SenderThreadID != c.threadID {
			return
		}
		for i, id := range x.ReceiverThreadIDs {
			if i >= maxSubagents {
				break
			}
			c.adoptLocked(id, "")
		}
	case "subAgentActivity":
		if x.Kind == activityStart {
			c.adoptLocked(x.AgentThreadID, pathName(x.AgentPath))
		}
	}
}

func (c *Client) subNotificationLocked(s *subagent, method string, p json.RawMessage) {
	switch method {
	case methodItemStarted:
		c.itemLocked(p, false, s.id)
	case methodItemCompleted:
		c.itemLocked(p, true, s.id)
	case methodPatchUpdated:
		c.patchLocked(p, s.id)
	case methodTurnStarted:
		var n struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		if json.Unmarshal(p, &n) == nil && n.Turn.ID != "" && len(n.Turn.ID) <= maxIDLen {
			s.turn = n.Turn.ID
			c.statusLocked(s, subRunning)
		}
	case methodTurnCompleted:
		var n struct {
			Turn struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"turn"`
		}
		if json.Unmarshal(p, &n) != nil || n.Turn.ID == "" || len(n.Turn.ID) > maxIDLen {
			return
		}
		if s.turn == n.Turn.ID {
			s.turn = ""
		}
		id := n.Turn.ID
		c.withdrawLocked(func(q *pending) bool { return q.agent == s.id && q.turnID == id })
		c.keepItemsLocked(func(it *item) bool { return it.owner != s.id })
		switch turnStatus(n.Turn.Status) {
		case "failed":
			c.statusLocked(s, subFailed)
		case "interrupted":
			c.statusLocked(s, subStopped)
		default:
			c.statusLocked(s, subCompleted)
		}
	case methodSettingsUpdated:
		c.settingsLocked(p)
	case methodRequestResolved:
		c.resolvedLocked(p)
	case methodError:
		var n struct {
			WillRetry bool `json:"willRetry"`
		}
		if json.Unmarshal(p, &n) == nil && !n.WillRetry {
			c.statusLocked(s, subFailed)
		}
	case methodThreadStatus:
		var n struct {
			Status struct {
				Type string `json:"type"`
			} `json:"status"`
		}
		if json.Unmarshal(p, &n) == nil && n.Status.Type == "systemError" {
			c.statusLocked(s, subFailed)
		}
	case methodThreadClosed:
		if s.status == subRunning {
			c.statusLocked(s, subCompleted)
		}
	}
}
