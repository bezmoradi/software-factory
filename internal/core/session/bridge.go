package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/agent"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	bridgeRing      = 2000
	bridgeRingBytes = 8 << 20
	bridgePoll      = 20 * time.Second
	bridgeCall      = 30 * time.Second
	textFlush       = 100 * time.Millisecond
	maxDetail       = 20000
	maxSummary      = 300
	maxToolID       = 200
)

type ringEntry struct {
	ev   Fields
	size int
}

type bridge struct {
	reg     *Registry
	log     shared.Logger
	sid     string
	cwd     string
	version string
	now     func() time.Time

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	prompting sync.Mutex

	mu        sync.Mutex
	sess      agent.Session
	secret    string
	push      string
	epoch     string
	seq       int64
	ring      []ringEntry
	ringBytes int
	text      Fields
	timer     *time.Timer
	turn      string
	turns     int
	cards     map[string]string
	reqs      map[string]string
	ended     bool
	polling   bool
	waiting   []Fields
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func newSID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func newBridge(reg *Registry, log shared.Logger, sid, cwd, version string) (*bridge, error) {
	ctx, cancel := context.WithCancel(context.Background())
	b := &bridge{
		reg: reg, log: log, sid: sid, cwd: cwd, version: version, now: reg.now,
		ctx: ctx, cancel: cancel, done: make(chan struct{}),
		epoch: randomHex(8), cards: map[string]string{}, reqs: map[string]string{},
	}
	if err := b.register(); err != nil {
		cancel()
		return nil, err
	}
	return b, nil
}

func (b *bridge) register() error {
	got, err := b.reg.Register(Registration{
		SID: b.sid, Cwd: b.cwd, Version: b.version, Epoch: b.epoch, Protocol: Protocol,
		LastSeq: b.seq, Controlled: true, GateKnown: false, Cards: b.openCards(), keepPush: b.push,
	})
	if err != nil {
		return err
	}
	b.secret = got.Secret
	b.push = got.Push
	return nil
}

func (b *bridge) openCards() []any {
	var out []any
	for _, e := range b.ring {
		if id, _ := e.ev["id"].(string); id != "" && b.reqs[id] != "" && cardTypes[e.ev["type"].(string)] {
			out = append(out, e.ev)
		}
	}
	return out
}

func (b *bridge) pushToken() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.push
}

func (b *bridge) start(sess agent.Session, onEnd func()) {
	b.mu.Lock()
	b.sess = sess
	b.polling = true
	b.mu.Unlock()
	takes := false
	if a, ok := sess.(AttachingSession); ok {
		takes = a.TakesImages()
	}
	b.reg.setImages(b.sid, takes)
	go b.pollLoop()
	go func() {
		for ev := range sess.Events() {
			if !b.handled(ev) {
				b.fail("The engine couldn't handle a message from the agent, so it ended this session.")
				_ = sess.Close()
				break
			}
		}
		b.finish()
		onEnd()
	}()
}

func (b *bridge) handled(ev agent.Event) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	b.onEvent(ev)
	return true
}

func (b *bridge) fail(message string) {
	b.mu.Lock()
	b.recordLocked(Fields{"type": "command", "name": "error", "text": clip(message, MaxText), "failed": true})
	b.mu.Unlock()
	b.finish()
}

func (b *bridge) finish() {
	b.mu.Lock()
	if b.ended {
		b.mu.Unlock()
		return
	}
	b.flushLocked()
	if b.turn != "" {
		b.recordLocked(Fields{"type": "idle", "turnId": b.turn, "reason": "error"})
		b.turn = ""
	}
	for card := range b.reqs {
		b.recordLocked(Fields{"type": "resolved", "id": card})
	}
	b.reqs = map[string]string{}
	b.cards = map[string]string{}
	b.recordLocked(Fields{"type": "ended"})
	b.ended = true
	if b.timer != nil {
		b.timer.Stop()
	}
	polling := b.polling
	b.mu.Unlock()
	b.cancel()
	if polling {
		select {
		case <-b.done:
		case <-time.After(5 * time.Second):
		}
	}
	b.mu.Lock()
	secret := b.secret
	b.mu.Unlock()
	b.reg.Leave(secret)
}

func (b *bridge) close() {
	b.mu.Lock()
	sess := b.sess
	b.mu.Unlock()
	if sess != nil {
		_ = sess.Close()
	}
}

func (b *bridge) pollLoop() {
	defer close(b.done)
	for b.ctx.Err() == nil {
		ctx, cancel := context.WithTimeout(b.ctx, bridgePoll)
		b.mu.Lock()
		secret := b.secret
		b.mu.Unlock()
		messages, err := b.reg.Poll(ctx, secret)
		cancel()
		if b.ctx.Err() != nil {
			return
		}
		if err != nil {
			var e *Error
			if errors.As(err, &e) && e.Kind == Unauthorized {
				b.mu.Lock()
				rerr := b.register()
				b.mu.Unlock()
				if rerr == nil {
					continue
				}
			}
			select {
			case <-b.ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		for _, m := range messages {
			b.handle(m)
		}
	}
}

func (b *bridge) handle(m Fields) {
	kind, _ := m["kind"].(string)
	switch kind {
	case "prompt":
		if text, ok := m["text"].(string); ok {
			b.prompt(text, m)
		}
	case "interrupt":
		b.mu.Lock()
		sess := b.sess
		b.waiting = nil
		b.mu.Unlock()
		if sess == nil {
			return
		}
		ctx, cancel := context.WithTimeout(b.ctx, bridgeCall)
		defer cancel()
		if err := sess.Cancel(ctx); err != nil {
			b.log.Warn("agent session", "event", "interrupt-failed")
		}
	case "control":
		if on, ok := m["on"].(bool); ok {
			b.mu.Lock()
			b.recordLocked(Fields{"type": "control", "on": on})
			b.mu.Unlock()
		}
	case "replay":
		stream, _ := m["stream"].(string)
		epoch, _ := m["epoch"].(string)
		since, _ := m["since"].(float64)
		b.replay(stream, epoch, int64(since))
	}
}

func (b *bridge) prompt(text string, m Fields) {
	_, attached := m["images"]
	if _, files := m["files"]; files {
		attached = true
	}
	var parts []Attachment
	if attached {
		var ok bool
		if parts, ok = b.reg.attachments(b.sid, m); !ok {
			b.mu.Lock()
			b.recordLocked(Fields{"type": "command", "name": "error", "text": "The engine couldn't read an image or file sent with the message, so the message wasn't passed on.", "failed": true})
			b.mu.Unlock()
			return
		}
	}
	b.mu.Lock()
	sess := b.sess
	if sess == nil || b.ended {
		b.mu.Unlock()
		return
	}
	if b.turn != "" {
		if len(b.waiting) < maxQueued {
			b.waiting = append(b.waiting, m)
		} else {
			b.recordLocked(Fields{"type": "command", "name": "error", "text": "Twenty messages already wait for the agent's turn to end, so this one wasn't passed on.", "failed": true})
		}
		b.mu.Unlock()
		return
	}
	b.turns++
	turn := "t" + strconv.Itoa(b.turns)
	b.turn = turn
	shown := Fields{"type": "prompt", "text": text, "from": "page"}
	images := 0
	var names []any
	for _, p := range parts {
		if p.Image {
			images++
		} else {
			names = append(names, p.Name)
		}
	}
	if images > 0 {
		shown["images"] = float64(images)
	}
	if len(names) > 0 {
		shown["files"] = names
	}
	b.recordLocked(shown)
	b.recordLocked(Fields{"type": "busy", "turnId": turn})
	b.mu.Unlock()
	go func() {
		b.prompting.Lock()
		defer b.prompting.Unlock()
		ctx, cancel := context.WithTimeout(b.ctx, bridgeCall)
		defer cancel()
		var err error
		if a, ok := sess.(AttachingSession); ok && len(parts) > 0 {
			err = a.PromptWith(ctx, text, parts)
		} else if len(parts) > 0 {
			err = sess.Prompt(ctx, strings.TrimSpace(AttachedNote(parts, false)+"\n\n"+text))
		} else {
			err = sess.Prompt(ctx, text)
		}
		if err == nil {
			b.log.Info("agent session", "event", "prompt")
			return
		}
		b.log.Warn("agent session", "event", "prompt-refused")
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.ended {
			return
		}
		b.recordLocked(Fields{"type": "command", "name": "error", "text": "The agent didn't take the message: " + clip(err.Error(), 500), "failed": true})
		if b.turn == turn {
			b.recordLocked(Fields{"type": "idle", "turnId": turn, "reason": "error"})
			b.turn = ""
			b.nextLocked()
		}
	}()
}

func (b *bridge) nextLocked() {
	if len(b.waiting) == 0 || b.ended {
		return
	}
	m := b.waiting[0]
	b.waiting = b.waiting[1:]
	go b.handle(m)
}

func (b *bridge) replay(stream, epoch string, since int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.flushLocked()
	reset := epoch != b.epoch
	if len(b.ring) > 0 {
		if oldest, _ := b.ring[0].ev["seq"].(float64); since < int64(oldest)-1 {
			reset = true
		}
	}
	events := []any{}
	for _, e := range b.ring {
		if seq, _ := e.ev["seq"].(float64); reset || int64(seq) > since {
			events = append(events, e.ev)
		}
	}
	_ = b.reg.Report(b.secret, Batch{Stream: stream, Reset: reset, Events: events})
}

func (b *bridge) onEvent(ev agent.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ended {
		return
	}
	switch ev.Type {
	case agent.Text:
		b.addTextLocked(ev.Text)
	case agent.Plan:
		if ev.Text != "" {
			b.addTextLocked("\n\nPlan:\n" + ev.Text + "\n\n")
		}
	case agent.ToolCall:
		if ev.Tool != nil && ev.Tool.ID != "" {
			b.recordLocked(Fields{"type": "tool", "id": clip(ev.Tool.ID, maxToolID), "tool": toolName(ev.Tool), "summary": clip(ev.Tool.Title, maxSummary)})
			if done, ok := toolDone(ev.Tool.Status); ok {
				b.recordLocked(Fields{"type": "tool_done", "id": clip(ev.Tool.ID, maxToolID), "ok": done, "outcome": ""})
			}
		}
	case agent.ToolUpdate:
		if ev.Tool != nil && ev.Tool.ID != "" {
			if done, ok := toolDone(ev.Tool.Status); ok {
				b.recordLocked(Fields{"type": "tool_done", "id": clip(ev.Tool.ID, maxToolID), "ok": done, "outcome": ""})
			}
		}
	case agent.Permission:
		if ev.Request != nil {
			b.cardLocked(*ev.Request)
		}
	case agent.Withdrawn:
		if ev.Request != nil {
			if card := b.cards[ev.Request.ID]; card != "" {
				delete(b.cards, ev.Request.ID)
				delete(b.reqs, card)
				b.recordLocked(Fields{"type": "resolved", "id": card})
			}
		}
	case agent.Mode:
		if ev.Mode != "" {
			b.recordLocked(Fields{"type": "mode", "mode": clip(ev.Mode, 40)})
		}
	case agent.Commands:
		list := make([]any, 0, len(ev.Commands))
		for _, c := range ev.Commands {
			list = append(list, map[string]any{"name": clip(c.Name, 200), "description": clip(c.Description, 1000), "source": "agent"})
		}
		b.recordLocked(Fields{"type": "catalog", "commands": list, "agents": []any{}})
	case agent.Usage:
		if f := usageFrame(ev.Spend); f != nil {
			b.recordLocked(f)
		}
	case agent.TurnEnded:
		turn := b.turn
		if turn == "" {
			turn = "t" + strconv.Itoa(b.turns)
		}
		b.recordLocked(Fields{"type": "idle", "turnId": turn, "reason": idleReason(ev.Reason)})
		b.turn = ""
		b.nextLocked()
	case agent.Failed:
		text := ev.Reason
		if ev.Text != "" {
			text = ev.Text
		}
		b.recordLocked(Fields{"type": "command", "name": "error", "text": clip(text, MaxText), "failed": true})
	}
}

func toolName(t *agent.Tool) string {
	if t.Kind != "" {
		return clip(t.Kind, 80)
	}
	return "tool"
}

func toolDone(status string) (bool, bool) {
	switch status {
	case "completed":
		return true, true
	case "failed", "declined":
		return false, true
	}
	return false, false
}

func idleReason(r string) string {
	switch r {
	case "cancelled", "interrupted", "aborted":
		return "aborted"
	case "error", "failed", "refusal":
		return "error"
	}
	return ""
}

func (b *bridge) addTextLocked(chunk string) {
	turn := b.turn
	if turn == "" {
		turn = "t" + strconv.Itoa(b.turns)
	}
	if b.text != nil && b.text["turnId"] != turn {
		b.flushLocked()
	}
	if b.text == nil {
		b.text = Fields{"type": "text", "turnId": turn, "text": ""}
	}
	b.text["text"] = b.text["text"].(string) + chunk
	if len(b.text["text"].(string)) > 50000 {
		b.flushLocked()
		return
	}
	if b.timer == nil {
		b.timer = time.AfterFunc(textFlush, func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			b.timer = nil
			if !b.ended {
				b.flushLocked()
			}
		})
	}
}

func (b *bridge) flushLocked() {
	if b.text == nil {
		return
	}
	t := b.text
	b.text = nil
	b.recordLocked(t)
}

func (b *bridge) cardLocked(req agent.Request) {
	id := randomHex(16)
	allow := false
	for _, o := range req.Options {
		if o.Kind == "allow_once" {
			allow = true
		}
	}
	detail := ""
	if len(req.Tool.Input) > 0 {
		detail = prettyJSON(req.Tool.Input)
	}
	complete := req.Tool.Whole && allow && len(detail) <= maxDetail
	tool := req.Tool.Title
	if tool == "" {
		tool = toolName(&req.Tool)
	}
	b.cards[req.ID] = id
	b.reqs[id] = req.ID
	b.recordLocked(Fields{
		"type": "permission", "id": id, "tool": clip(tool, maxSummary), "summary": clip(req.Tool.Title, maxSummary),
		"detail": clip(detail, maxDetail), "complete": complete, "always": "", "auto": false,
	})
	b.log.Info("agent session", "event", "card")
	go b.waitCard(id, req.ID)
}

func prettyJSON(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return string(raw)
	}
	return string(out)
}

func answerOf(v any) agent.Answer {
	m, _ := v.(map[string]any)
	choice, _ := m["choice"].(string)
	note, _ := m["note"].(string)
	if choice == "yes" {
		return agent.Answer{Allow: true}
	}
	return agent.Answer{Allow: false, Note: note}
}

func (b *bridge) waitCard(card, req string) {
	for b.ctx.Err() == nil {
		ctx, cancel := context.WithTimeout(b.ctx, bridgePoll)
		b.mu.Lock()
		secret := b.secret
		b.mu.Unlock()
		answer, found, err := b.reg.Wait(ctx, secret, card)
		cancel()
		if b.ctx.Err() != nil {
			return
		}
		if err != nil {
			var e *Error
			if errors.As(err, &e) && e.Kind == Conflict {
				select {
				case <-b.ctx.Done():
					return
				case <-time.After(time.Second):
				}
				continue
			}
			return
		}
		if !found {
			continue
		}
		b.mu.Lock()
		sess := b.sess
		if b.reqs[card] != req {
			b.mu.Unlock()
			return
		}
		delete(b.reqs, card)
		delete(b.cards, req)
		b.mu.Unlock()
		actx, acancel := context.WithTimeout(b.ctx, bridgeCall)
		aerr := sess.Answer(actx, req, answerOf(answer))
		acancel()
		b.mu.Lock()
		if !b.ended {
			b.recordLocked(Fields{"type": "resolved", "id": card})
			if aerr != nil {
				b.recordLocked(Fields{"type": "command", "name": "error", "text": "The answer was refused or couldn't be passed on: " + clip(aerr.Error(), 500), "failed": true})
			}
		}
		b.mu.Unlock()
		b.log.Info("agent session", "event", "card-answered")
		return
	}
}

func fieldsSize(f Fields) int {
	n := 64
	for k, v := range f {
		n += len(k) + 8
		switch x := v.(type) {
		case string:
			n += len(x)
		case []any:
			for _, item := range x {
				if m, ok := item.(map[string]any); ok {
					n += fieldsSize(m)
				}
			}
		}
	}
	return n
}

func (b *bridge) recordLocked(f Fields) {
	if f["type"] != "text" {
		b.flushLocked()
	}
	b.seq++
	f["seq"] = float64(b.seq)
	f["ts"] = float64(b.now().UnixMilli())
	entry := ringEntry{ev: f, size: fieldsSize(f)}
	b.ring = append(b.ring, entry)
	b.ringBytes += entry.size
	for len(b.ring) > bridgeRing || (b.ringBytes > bridgeRingBytes && len(b.ring) > 1) {
		b.ringBytes -= b.ring[0].size
		b.ring[0] = ringEntry{}
		b.ring = b.ring[1:]
	}
	if err := b.reg.Report(b.secret, Batch{Events: []any{f}}); err != nil {
		kind := "report-refused"
		if strings.Contains(err.Error(), "disputed") {
			kind = "report-disputed"
		}
		b.log.Warn("agent session", "event", kind)
	}
}

func usageFrame(sp *agent.Spend) Fields {
	if sp == nil {
		return nil
	}
	f := Fields{"type": "usage"}
	if n := sp.InputTokens + sp.OutputTokens; n > 0 {
		f["total"] = float64(n)
	}
	if c := sp.CostUSD; c > 0 && c < 1e7 && !math.IsInf(c, 0) && !math.IsNaN(c) {
		f["costUsd"] = c
		f["estimate"] = true
	}
	if len(f) == 1 {
		return nil
	}
	return f
}
