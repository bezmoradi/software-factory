package acp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"slices"
	"strings"
	"sync"

	"github.com/bezmoradi/software-factory/internal/core/agent"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	ProtocolVersion       = 1
	DefaultMaxLineBytes   = 16 << 20
	maxPendingPermissions = 32
	maxOptions            = 32
	maxTools              = 1024
	maxToolBytes          = 64 << 20
	maxQueuedEvents       = 4096
	maxQueuedBytes        = 64 << 20
	maxWriteBytes         = 64 << 20
	maxCommands           = 400
	maxPlanEntries        = 200
	maxReasonBytes        = 1024
)

var DefaultAskingModes = []string{"default", "ask", "manual"}

var (
	ErrClosed            = errors.New("acp: connection closed")
	ErrNotStarted        = errors.New("acp: session not started")
	ErrStarted           = errors.New("acp: session already started")
	ErrTurnActive        = errors.New("acp: a turn is already running")
	ErrUnknownRequest    = errors.New("acp: no pending permission request with that id")
	ErrUnknownOption     = errors.New("acp: the request offers no such option")
	ErrAllowAlways       = errors.New("acp: an allow_always option is never selected")
	ErrOptionMismatch    = errors.New("acp: the option's kind contradicts the answer")
	ErrNotWhole          = errors.New("acp: the tool call has no rawInput, so the request may only be rejected")
	ErrNoAllowOnce       = errors.New("acp: the agent offered no allow_once option, so the request was rejected")
	ErrNotAsking         = errors.New("acp: the agent offers modes but none of them is an asking mode")
	ErrProtocolVersion   = errors.New("acp: the agent answered an unsupported protocol version")
	ErrAuthNotRequired   = errors.New("acp: authenticate is only sent after session/new answered auth required")
	ErrUnknownAuthMethod = errors.New("acp: the agent advertised no such agent-handled sign-in method")
	ErrBadConfig         = errors.New("acp: invalid configuration")
	ErrNoImages          = errors.New("acp: the agent doesn't offer the image prompt capability")
)

type Config struct {
	Cwd           string
	MCPServers    []json.RawMessage
	AskingModes   []string
	ClientVersion string
	MaxLineBytes  int
	Logger        shared.Logger
}

type Client struct {
	cfg     Config
	log     shared.Logger
	rw      io.ReadWriteCloser
	maxLine int

	stop       chan struct{}
	abandon    chan struct{}
	writerDone chan struct{}
	wake       chan struct{}
	wwake      chan struct{}
	out        chan agent.Event

	startMu   sync.Mutex
	stopOnce  sync.Once
	closeOnce sync.Once
	connOnce  sync.Once
	connErr   error

	wmu     sync.Mutex
	wq      [][]byte
	wqBytes int
	wclosed bool

	mu           sync.Mutex
	nextID       int64
	calls        map[int64]callback
	ended        bool
	closed       bool
	overflow     bool
	queue        []agent.Event
	queuedBytes  int
	initialized  bool
	authMethods  []AuthMethod
	authRequired bool
	images       bool
	sessionID    string
	turn         bool
	perms        map[string]*permission
	permSeq      int
	tools        map[string]*toolState
	toolOrder    []string
	toolBytes    int
	mode         modeState
}

var (
	_ agent.Session            = (*Client)(nil)
	_ session.AttachingSession = (*Client)(nil)
)

func New(rw io.ReadWriteCloser, cfg Config) *Client {
	if len(cfg.AskingModes) == 0 {
		cfg.AskingModes = DefaultAskingModes
	}
	if cfg.ClientVersion == "" {
		cfg.ClientVersion = "0.0.0"
	}
	maxLine := cfg.MaxLineBytes
	if maxLine <= 0 {
		maxLine = DefaultMaxLineBytes
	}
	log := cfg.Logger
	if log == nil {
		log = quiet{}
	}
	c := &Client{
		cfg:        cfg,
		log:        log,
		rw:         rw,
		maxLine:    maxLine,
		stop:       make(chan struct{}),
		abandon:    make(chan struct{}),
		writerDone: make(chan struct{}),
		wake:       make(chan struct{}, 1),
		wwake:      make(chan struct{}, 1),
		out:        make(chan agent.Event, 16),
		calls:      map[int64]callback{},
		perms:      map[string]*permission{},
		tools:      map[string]*toolState{},
	}
	go c.writeLoop()
	go c.readLoop()
	go c.pump()
	return c
}

type initializeParams struct {
	ProtocolVersion    int                `json:"protocolVersion"`
	ClientCapabilities clientCapabilities `json:"clientCapabilities"`
	ClientInfo         implementation     `json:"clientInfo"`
}

type clientCapabilities struct {
	FS       fsCapabilities   `json:"fs"`
	Terminal bool             `json:"terminal"`
	Auth     authCapabilities `json:"auth"`
}

type fsCapabilities struct {
	ReadTextFile  bool `json:"readTextFile"`
	WriteTextFile bool `json:"writeTextFile"`
}

type authCapabilities struct {
	Terminal bool `json:"terminal"`
}

type implementation struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	Version string `json:"version"`
}

type newSessionParams struct {
	Cwd        string            `json:"cwd"`
	MCPServers []json.RawMessage `json:"mcpServers"`
}

func (c *Client) Start(ctx context.Context) error {
	c.startMu.Lock()
	defer c.startMu.Unlock()
	if !path.IsAbs(c.cfg.Cwd) {
		return fmt.Errorf("%w: cwd must be an absolute path", ErrBadConfig)
	}
	servers := c.cfg.MCPServers
	if servers == nil {
		servers = []json.RawMessage{}
	}
	for _, s := range servers {
		if !isObject(s) {
			return fmt.Errorf("%w: each MCP server entry must be a JSON object", ErrBadConfig)
		}
	}
	c.mu.Lock()
	started, initialized, ended := c.sessionID != "", c.initialized, c.ended
	c.mu.Unlock()
	if ended {
		return ErrClosed
	}
	if started {
		return ErrStarted
	}
	if !initialized {
		if err := c.initialize(ctx); err != nil {
			return err
		}
	}
	var created newSessionResult
	_, err := c.call(ctx, "session/new", newSessionParams{Cwd: c.cfg.Cwd, MCPServers: servers}, func(result json.RawMessage) {
		if json.Unmarshal(result, &created) != nil || created.SessionID == "" {
			return
		}
		c.mu.Lock()
		c.sessionID = created.SessionID
		c.authRequired = false
		c.mu.Unlock()
	})
	var rerr *RPCError
	if errors.As(err, &rerr) && rerr.Code == codeAuthRequired {
		return c.authRequiredError(rerr)
	}
	if err != nil {
		return err
	}
	if created.SessionID == "" {
		return errors.New("acp: session/new answered no sessionId")
	}
	return c.askingMode(ctx, created)
}

func (c *Client) initialize(ctx context.Context) error {
	params := initializeParams{
		ProtocolVersion: ProtocolVersion,
		ClientInfo:      implementation{Name: "software-factory", Title: "Software Factory", Version: c.cfg.ClientVersion},
	}
	result, err := c.call(ctx, "initialize", params, nil)
	if err != nil {
		return err
	}
	var r struct {
		ProtocolVersion   *int              `json:"protocolVersion"`
		AuthMethods       []json.RawMessage `json:"authMethods"`
		AgentCapabilities struct {
			PromptCapabilities struct {
				Image bool `json:"image"`
			} `json:"promptCapabilities"`
		} `json:"agentCapabilities"`
	}
	if err := json.Unmarshal(result, &r); err != nil || r.ProtocolVersion == nil {
		return errors.New("acp: malformed initialize answer")
	}
	if *r.ProtocolVersion != ProtocolVersion {
		return ErrProtocolVersion
	}
	c.mu.Lock()
	c.initialized = true
	c.images = r.AgentCapabilities.PromptCapabilities.Image
	c.authMethods = agentAuthMethods(r.AuthMethods)
	c.mu.Unlock()
	c.note("initialized")
	return nil
}

func (c *Client) Prompt(ctx context.Context, text string) error {
	return c.prompt(ctx, []map[string]any{{"type": "text", "text": text}})
}

func (c *Client) TakesImages() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.images
}

func (c *Client) PromptWith(ctx context.Context, text string, parts []session.Attachment) error {
	blocks := []map[string]any{}
	note := session.AttachedNote(parts, c.TakesImages())
	if t := strings.TrimSpace(note + "\n\n" + text); t != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": t})
	}
	for _, p := range parts {
		if p.Image {
			if !c.TakesImages() {
				return ErrNoImages
			}
			blocks = append(blocks, map[string]any{"type": "image", "mimeType": p.Type, "data": base64.StdEncoding.EncodeToString(p.Data), "uri": fileURI(p.Path)})
			continue
		}
		blocks = append(blocks, map[string]any{"type": "resource_link", "uri": fileURI(p.Path), "name": p.Name})
	}
	return c.prompt(ctx, blocks)
}

func fileURI(p string) string {
	return (&url.URL{Scheme: "file", Path: p}).String()
}

func (c *Client) prompt(ctx context.Context, blocks []map[string]any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ended {
		return ErrClosed
	}
	if c.sessionID == "" {
		return ErrNotStarted
	}
	if c.turn {
		return ErrTurnActive
	}
	id, err := c.registerLocked(c.onPromptResult)
	if err != nil {
		return err
	}
	params := map[string]any{
		"sessionId": c.sessionID,
		"prompt":    blocks,
	}
	if err := c.enqueue(outRequest{JSONRPC: "2.0", ID: id, Method: "session/prompt", Params: params}); err != nil {
		delete(c.calls, id)
		return err
	}
	c.turn = true
	c.note("prompt")
	return nil
}

func (c *Client) onPromptResult(result json.RawMessage, rerr *RPCError, err error) {
	c.mu.Lock()
	defer c.signal()
	defer c.mu.Unlock()
	if !c.turn || err != nil {
		return
	}
	c.turn = false
	if rerr != nil {
		c.emitLocked(agent.Event{Type: agent.Failed, Reason: rerr.Error()})
		c.emitLocked(agent.Event{Type: agent.TurnEnded, Reason: "error"})
		return
	}
	var r struct {
		StopReason string `json:"stopReason"`
	}
	_ = json.Unmarshal(result, &r)
	c.emitLocked(agent.Event{Type: agent.TurnEnded, Reason: stopReason(r.StopReason)})
}

func stopReason(s string) string {
	switch s {
	case "end_turn", "max_tokens", "max_turn_requests", "refusal", "cancelled":
		return s
	default:
		return "unknown"
	}
}

func (c *Client) Cancel(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.signal()
	defer c.mu.Unlock()
	if c.ended {
		return ErrClosed
	}
	if c.sessionID == "" {
		return ErrNotStarted
	}
	if err := c.enqueue(outNotification{JSONRPC: "2.0", Method: "session/cancel", Params: map[string]string{"sessionId": c.sessionID}}); err != nil {
		return err
	}
	c.cancelPermissionsLocked()
	c.cancelToolsLocked()
	c.note("cancel")
	return nil
}

func (c *Client) Events() <-chan agent.Event {
	return c.out
}

func (c *Client) Mode() (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mode.current, c.askingLocked(c.mode.current)
}

func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		if !c.ended {
			if c.turn && c.sessionID != "" {
				_ = c.enqueue(outNotification{JSONRPC: "2.0", Method: "session/cancel", Params: map[string]string{"sessionId": c.sessionID}})
			}
			c.cancelPermissionsLocked()
		}
		c.closed = true
		c.mu.Unlock()
		close(c.abandon)
		c.finish("closed")
		c.closeConn()
	})
	return c.connErr
}

func (c *Client) finish(reason string) {
	c.mu.Lock()
	if c.ended {
		c.mu.Unlock()
		return
	}
	if !c.closed {
		if c.turn && !c.overflow {
			c.queue = append(c.queue, agent.Event{Type: agent.Failed, Reason: "the agent ended during a turn"})
		}
		c.queue = append(c.queue, agent.Event{Type: agent.Ended, Reason: reason})
	}
	c.ended = true
	c.turn = false
	c.perms = map[string]*permission{}
	calls := c.calls
	c.calls = map[int64]callback{}
	c.mu.Unlock()
	c.signal()
	ids := make([]int64, 0, len(calls))
	for id := range calls {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		calls[id](nil, nil, ErrClosed)
	}
	c.wmu.Lock()
	c.wclosed = true
	c.wmu.Unlock()
	c.stopOnce.Do(func() { close(c.stop) })
	c.note("ended")
	go c.closeConn()
}

func (c *Client) emitLocked(ev agent.Event) {
	if c.ended || c.overflow {
		return
	}
	size := eventSize(ev)
	if len(c.queue) >= maxQueuedEvents || c.queuedBytes+size > maxQueuedBytes {
		c.overflow = true
		c.queue = append(c.queue, agent.Event{Type: agent.Failed, Reason: "events were not read in time"})
		c.warn("event-backlog")
		go c.finish("events were not read in time")
		return
	}
	c.queue = append(c.queue, ev)
	c.queuedBytes += size
}

func (c *Client) signal() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *Client) pump() {
	defer close(c.out)
	for {
		c.mu.Lock()
		if len(c.queue) == 0 {
			done := c.ended
			c.mu.Unlock()
			if done {
				return
			}
			select {
			case <-c.wake:
			case <-c.abandon:
				return
			}
			continue
		}
		ev := c.queue[0]
		c.queue[0] = agent.Event{}
		c.queue = c.queue[1:]
		c.queuedBytes -= eventSize(ev)
		c.mu.Unlock()
		select {
		case c.out <- ev:
		case <-c.abandon:
			return
		}
	}
}

func eventSize(ev agent.Event) int {
	n := len(ev.Text) + len(ev.Reason) + 64
	if ev.Tool != nil {
		n += len(ev.Tool.Input) + len(ev.Tool.Title)
	}
	if ev.Request != nil {
		n += len(ev.Request.Tool.Input) + len(ev.Request.Tool.Title) + 64*len(ev.Request.Options)
	}
	for _, cmd := range ev.Commands {
		n += len(cmd.Name) + len(cmd.Description)
	}
	return n
}

func (c *Client) note(kind string) {
	c.log.Info("acp", "event", kind)
}

func (c *Client) warn(kind string) {
	c.log.Warn("acp", "event", kind)
}

func isObject(raw json.RawMessage) bool {
	var m map[string]json.RawMessage
	return json.Unmarshal(raw, &m) == nil && m != nil
}

type quiet struct{}

func (quiet) Error(string, ...any) {}
func (quiet) Fatal(error)          {}
func (quiet) Info(string, ...any)  {}
func (quiet) Warn(string, ...any)  {}
