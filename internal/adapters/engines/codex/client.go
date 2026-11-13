package codex

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/bezmoradi/software-factory/internal/core/agent"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	ReasonNotSignedIn    = "not-signed-in"
	ReasonApprovalPolicy = "approval-policy"
	ReasonTurnError      = "turn-error"
	ReasonAgentError     = "agent-error"
	ReasonOverflow       = "overflow"
	ReasonWithdrawn      = "withdrawn"
	ReasonClosed         = "closed"
	ReasonAgentExited    = "agent-exited"
	ReasonThreadClosed   = "thread-closed"
	ReasonBadMessage     = "bad-message"
)

const (
	SignInNone       = "none"
	SignInChatGPT    = "chatgpt"
	SignInAPIKey     = "apiKey"
	SignInBedrock    = "amazonBedrock"
	SignInGCP        = "gcp"
	SignInUnknown    = "unknown"
	signInNotChecked = ""
)

const (
	maxQueued  = 4096
	maxPending = 32
	maxItems   = 2048
	maxReplies = 256
	maxPrompt  = 1 << 20
	clientName = "software_factory"
	clientText = "Software Factory"
)

const signInHelp = "Codex isn't signed in. Sign in inside the agent container with Codex's own login, then start the session again: docker compose exec -it agent codex login --device-auth"

var (
	ErrClosed         = errors.New("codex: session closed")
	ErrNotSignedIn    = errors.New("codex: not signed in")
	ErrPolicy         = errors.New("codex: approval policy is no longer on-request with the user as reviewer")
	ErrNoThread       = errors.New("codex: no thread")
	ErrUnknownRequest = errors.New("codex: unknown request")
	ErrNotWhole       = errors.New("codex: the request isn't shown whole, so it can only be declined")
	ErrChanged        = errors.New("codex: the request changed after it was shown, so it can only be declined")
	ErrBadOption      = errors.New("codex: option not offered for this request")
	ErrTooLong        = errors.New("codex: message too long")
	ErrConfig         = errors.New("codex: invalid config")
	ErrProtocol       = errors.New("codex: unexpected reply")
)

type RPCError struct {
	Method  string
	Code    int64
	Message string
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("codex: %s failed with code %d", e.Method, e.Code)
}

type Config struct {
	Cwd           string
	MCPServers    map[string]json.RawMessage
	ClientVersion string
	Logger        shared.Logger
}

var serverName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

var (
	_ agent.Session            = (*Client)(nil)
	_ session.AttachingSession = (*Client)(nil)
)

type Client struct {
	rw  io.ReadWriteCloser
	log shared.Logger
	wmu sync.Mutex

	mu        sync.Mutex
	nextID    int64
	calls     map[int64]chan envelope
	pending   map[string]*pending
	byRPC     map[string]string
	items     map[string]*item
	doneTurns map[string]bool
	threadID  string
	turnID    string
	signIn    string
	signedOut bool
	violated  bool
	closing   bool
	finished  bool
	ended     bool
	overflow  bool
	unknown   int
	queue     []agent.Event

	wake    chan struct{}
	events  chan agent.Event
	replies chan any
	done    chan struct{}
	quit    chan struct{}
	once    sync.Once
}

func Start(ctx context.Context, rw io.ReadWriteCloser, cfg Config) (*Client, error) {
	params, err := threadParams(cfg)
	if err != nil {
		_ = rw.Close()
		return nil, err
	}
	c := &Client{
		rw:        rw,
		log:       cfg.Logger,
		calls:     map[int64]chan envelope{},
		pending:   map[string]*pending{},
		byRPC:     map[string]string{},
		items:     map[string]*item{},
		doneTurns: map[string]bool{},
		wake:      make(chan struct{}, 1),
		events:    make(chan agent.Event),
		replies:   make(chan any, maxReplies),
		done:      make(chan struct{}),
		quit:      make(chan struct{}),
	}
	if c.log == nil {
		c.log = quiet{}
	}
	go c.read()
	go c.pump()
	go c.replier()
	if err := c.handshake(ctx, cfg, params); err != nil {
		close(c.quit)
		c.finish(ReasonClosed)
		return nil, err
	}
	return c, nil
}

func threadParams(cfg Config) (threadStartParams, error) {
	p := threadStartParams{
		Cwd:               cfg.Cwd,
		ApprovalPolicy:    policyOnRequest,
		ApprovalsReviewer: reviewerUser,
		Sandbox:           sandboxNone,
	}
	if cfg.Cwd == "" || !filepath.IsAbs(cfg.Cwd) {
		return p, fmt.Errorf("%w: the working folder must be an absolute path", ErrConfig)
	}
	for name, raw := range cfg.MCPServers {
		if !serverName.MatchString(name) {
			return p, fmt.Errorf("%w: MCP server names use letters, digits, - and _ only", ErrConfig)
		}
		if !isObject(raw) {
			return p, fmt.Errorf("%w: an MCP server's settings must be a JSON object", ErrConfig)
		}
		if p.Config == nil {
			p.Config = map[string]json.RawMessage{}
		}
		p.Config["mcp_servers."+name] = raw
	}
	return p, nil
}

func (c *Client) handshake(ctx context.Context, cfg Config, params threadStartParams) error {
	version := cfg.ClientVersion
	if version == "" {
		version = "0.0.0"
	}
	_, err := c.call(ctx, methodInitialize, initializeParams{
		ClientInfo:   clientInfo{Name: clientName, Title: clientText, Version: version},
		Capabilities: capabilities{OptOut: optOut},
	})
	if err != nil {
		return err
	}
	if err := c.write(outNotification{Method: methodInitialized}); err != nil {
		return err
	}
	raw, err := c.call(ctx, methodAccountRead, struct{}{})
	if err != nil {
		return err
	}
	var acct struct {
		Account *struct {
			Type string `json:"type"`
		} `json:"account"`
		RequiresOpenaiAuth bool `json:"requiresOpenaiAuth"`
	}
	if json.Unmarshal(raw, &acct) != nil {
		return ErrProtocol
	}
	c.mu.Lock()
	c.signIn = signInName(acct.Account)
	if acct.Account == nil && acct.RequiresOpenaiAuth {
		c.signedOut = true
		c.emitLocked(agent.Event{Type: agent.Failed, Reason: ReasonNotSignedIn, Text: signInHelp})
		c.mu.Unlock()
		c.log.Warn("codex: not signed in")
		return nil
	}
	c.mu.Unlock()
	raw, err = c.call(ctx, methodThreadStart, params)
	if err != nil {
		return err
	}
	var ts struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
		ApprovalPolicy    json.RawMessage `json:"approvalPolicy"`
		ApprovalsReviewer json.RawMessage `json:"approvalsReviewer"`
		Sandbox           struct {
			Type string `json:"type"`
		} `json:"sandbox"`
	}
	if json.Unmarshal(raw, &ts) != nil || ts.Thread.ID == "" || len(ts.Thread.ID) > maxIDLen {
		return ErrProtocol
	}
	c.mu.Lock()
	c.threadID = ts.Thread.ID
	c.checkSettingsLocked(ts.ApprovalPolicy, ts.ApprovalsReviewer)
	c.mu.Unlock()
	c.log.Info("codex: thread started", "sandbox", sandboxName(ts.Sandbox.Type))
	return nil
}

func signInName(acct *struct {
	Type string `json:"type"`
}) string {
	if acct == nil {
		return SignInNone
	}
	switch acct.Type {
	case SignInChatGPT, SignInAPIKey, SignInBedrock, SignInGCP:
		return acct.Type
	}
	return SignInUnknown
}

func sandboxName(t string) string {
	switch t {
	case "dangerFullAccess", "readOnly", "workspaceWrite", "externalSandbox":
		return t
	}
	return "unknown"
}

func (c *Client) SignIn() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.signIn
}

func (c *Client) Events() <-chan agent.Event {
	return c.events
}

func (c *Client) Prompt(ctx context.Context, text string) error {
	return c.prompt(ctx, text, nil)
}

func (c *Client) TakesImages() bool { return true }

func (c *Client) PromptWith(ctx context.Context, text string, parts []session.Attachment) error {
	var images []any
	for _, p := range parts {
		if p.Image {
			images = append(images, localImage{Type: "localImage", Path: p.Path})
		}
	}
	return c.prompt(ctx, strings.TrimSpace(session.AttachedNote(parts, true)+"\n\n"+text), images)
}

func (c *Client) prompt(ctx context.Context, text string, images []any) error {
	if len(text) > maxPrompt {
		return ErrTooLong
	}
	c.mu.Lock()
	thread, err := c.usableLocked()
	c.mu.Unlock()
	if err != nil {
		return err
	}
	raw, err := c.call(ctx, methodTurnStart, turnStartParams{
		ThreadID:          thread,
		Input:             append([]any{textInput{Type: "text", Text: text, TextElements: []string{}}}, images...),
		ApprovalPolicy:    policyOnRequest,
		ApprovalsReviewer: reviewerUser,
	})
	if err != nil {
		return err
	}
	var res struct {
		Turn struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"turn"`
	}
	if json.Unmarshal(raw, &res) != nil || res.Turn.ID == "" || len(res.Turn.ID) > maxIDLen {
		return ErrProtocol
	}
	c.mu.Lock()
	if !c.doneTurns[res.Turn.ID] && res.Turn.Status == "inProgress" {
		c.turnID = res.Turn.ID
	}
	c.mu.Unlock()
	return nil
}

func (c *Client) usableLocked() (string, error) {
	switch {
	case c.finished || c.closing:
		return "", ErrClosed
	case c.signedOut:
		return "", ErrNotSignedIn
	case c.violated:
		return "", ErrPolicy
	case c.threadID == "":
		return "", ErrNoThread
	}
	return c.threadID, nil
}

func (c *Client) Cancel(ctx context.Context) error {
	c.mu.Lock()
	if c.finished || c.closing {
		c.mu.Unlock()
		return ErrClosed
	}
	stopped := c.withdrawLocked(func(*pending) bool { return true })
	thread, turn := c.threadID, c.turnID
	c.mu.Unlock()
	for _, p := range stopped {
		if err := c.write(p.reply(false, true)); err != nil {
			return err
		}
	}
	if turn == "" {
		return nil
	}
	_, err := c.call(ctx, methodTurnInterrupt, turnInterruptParams{ThreadID: thread, TurnID: turn})
	if rerr, ok := errors.AsType[*RPCError](err); ok {
		c.log.Info("codex: interrupt refused", "code", rerr.Code)
		return nil
	}
	return err
}

func (c *Client) Answer(ctx context.Context, requestID string, a agent.Answer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	if c.finished || c.closing {
		c.mu.Unlock()
		return ErrClosed
	}
	p := c.pending[requestID]
	if p == nil {
		c.mu.Unlock()
		return ErrUnknownRequest
	}
	allow, err := p.choice(a)
	if err == nil && allow {
		switch {
		case !p.whole:
			err = ErrNotWhole
		case c.violated:
			err = ErrPolicy
		case p.item != nil && p.item.version != p.version:
			err = ErrChanged
		}
	}
	if err != nil {
		c.mu.Unlock()
		return err
	}
	c.dropPendingLocked(p)
	c.mu.Unlock()
	return c.write(p.reply(allow, false))
}

func (c *Client) Close() error {
	c.mu.Lock()
	if c.closing || c.finished {
		c.mu.Unlock()
		return nil
	}
	c.closing = true
	c.mu.Unlock()
	err := c.rw.Close()
	c.finish(ReasonClosed)
	return err
}

func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	if c.finished {
		c.mu.Unlock()
		return nil, ErrClosed
	}
	c.nextID++
	id := c.nextID
	ch := make(chan envelope, 1)
	c.calls[id] = ch
	c.mu.Unlock()
	if err := c.write(outRequest{ID: id, Method: method, Params: params}); err != nil {
		c.dropCall(id)
		return nil, err
	}
	select {
	case env, ok := <-ch:
		if !ok {
			return nil, ErrClosed
		}
		if env.Error != nil {
			return nil, &RPCError{Method: method, Code: env.Error.Code, Message: env.Error.Message}
		}
		return env.Result, nil
	case <-ctx.Done():
		c.dropCall(id)
		return nil, ctx.Err()
	}
}

func (c *Client) dropCall(id int64) {
	c.mu.Lock()
	delete(c.calls, id)
	c.mu.Unlock()
}

func (c *Client) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if _, err := c.rw.Write(b); err != nil {
		return fmt.Errorf("%w: %w", ErrClosed, err)
	}
	return nil
}

func (c *Client) reply(v any) {
	select {
	case c.replies <- v:
	default:
		c.log.Warn("codex: reply dropped, too many waiting")
	}
}

func (c *Client) replier() {
	for {
		select {
		case v := <-c.replies:
			if err := c.write(v); err != nil {
				return
			}
		case <-c.done:
			return
		}
	}
}

func (c *Client) read() {
	lr := newLineReader(c.rw)
	for {
		line, err := lr.next()
		if errors.Is(err, errLineTooLong) {
			c.log.Warn("codex: line over the limit dropped")
			continue
		}
		if err != nil {
			break
		}
		if !c.handled(line) {
			c.log.Warn("codex: handler panic")
			c.finish(ReasonBadMessage)
			return
		}
	}
	c.finish(ReasonAgentExited)
}

func (c *Client) handled(line []byte) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	c.handleLine(line)
	return true
}

func (c *Client) finish(reason string) {
	c.once.Do(func() {
		c.mu.Lock()
		if c.closing {
			reason = ReasonClosed
		}
		c.finished = true
		c.withdrawLocked(func(*pending) bool { return true })
		for id, ch := range c.calls {
			close(ch)
			delete(c.calls, id)
		}
		if c.turnID != "" {
			c.queue = append(c.queue, agent.Event{Type: agent.TurnEnded, Reason: reason})
			c.turnID = ""
		}
		c.queue = append(c.queue, agent.Event{Type: agent.Ended, Reason: reason})
		c.ended = true
		c.signalLocked()
		c.mu.Unlock()
		close(c.done)
		_ = c.rw.Close()
		c.log.Info("codex: session ended", "reason", reason)
	})
}

func (c *Client) emitLocked(ev agent.Event) {
	if c.overflow || c.ended {
		return
	}
	if len(c.queue) >= maxQueued {
		c.overflow = true
		c.queue = append(c.queue, agent.Event{Type: agent.Failed, Reason: ReasonOverflow})
		c.signalLocked()
		c.log.Error("codex: event queue full, closing")
		go func() { _ = c.Close() }()
		return
	}
	c.queue = append(c.queue, ev)
	c.signalLocked()
}

func (c *Client) signalLocked() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *Client) pump() {
	defer close(c.events)
	for {
		c.mu.Lock()
		for len(c.queue) == 0 {
			if c.ended {
				c.mu.Unlock()
				return
			}
			c.mu.Unlock()
			select {
			case <-c.wake:
			case <-c.quit:
				return
			}
			c.mu.Lock()
		}
		ev := c.queue[0]
		c.queue[0] = agent.Event{}
		c.queue = c.queue[1:]
		c.mu.Unlock()
		select {
		case c.events <- ev:
		case <-c.quit:
			return
		}
	}
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type quiet struct{}

func (quiet) Error(string, ...any) {}
func (quiet) Fatal(error)          {}
func (quiet) Info(string, ...any)  {}
func (quiet) Warn(string, ...any)  {}
