package agenthost

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	maxReply    = MaxRunStdout + MaxRunStderr + 64<<10
	maxProblem  = 8 << 10
	maxHeaders  = 64 << 10
	clientSlack = 30 * time.Second
)

var errHeadersTooLarge = errors.New("agent-host's answer headers are too large")

type headerBudget struct {
	r      io.Reader
	left   int
	lifted bool
}

func (h *headerBudget) Read(p []byte) (int, error) {
	if h.lifted {
		return h.r.Read(p)
	}
	if h.left <= 0 {
		return 0, errHeadersTooLarge
	}
	if len(p) > h.left {
		p = p[:h.left]
	}
	n, err := h.r.Read(p)
	h.left -= n
	return n, err
}

var processID = regexp.MustCompile(`^[0-9a-f]{16}$`)

type Client struct {
	address string
	http    *http.Client
	dialer  *net.Dialer
	key     string
}

var _ session.Host = (*Client)(nil)

func NewClient(address string) (*Client, error) {
	return NewClientFor(address, MaxRunTimeout)
}

func NewClientFor(address string, longest time.Duration) (*Client, error) {
	if _, _, err := net.SplitHostPort(address); err != nil {
		return nil, fmt.Errorf("agent-host address must be host:port, got %q", address)
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := &http.Transport{
		DialContext:            dialer.DialContext,
		Proxy:                  nil,
		MaxIdleConnsPerHost:    8,
		IdleConnTimeout:        time.Minute,
		DisableCompression:     true,
		MaxResponseHeaderBytes: maxHeaders,
	}
	return &Client{address: address, dialer: dialer, http: &http.Client{Transport: transport, Timeout: longest + clientSlack}}, nil
}

func (c *Client) WithKey(key string) *Client {
	c.key = key
	return c
}

func hostError(status int, body []byte) error {
	var p problemJSON
	if json.Unmarshal(body, &p) != nil || p.Error == "" {
		return &session.HostError{Code: "http", Message: fmt.Sprintf("agent-host answered %d", status)}
	}
	return &session.HostError{Code: clipText(p.Error, 40), Message: clipText(p.Message, 1000)}
}

func clipText(s string, n int) string {
	s = strings.ToValidUTF8(s, "")
	if len(s) <= n {
		return s
	}
	s = s[:n]
	return strings.ToValidUTF8(s, "")
}

func (c *Client) call(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://"+c.address+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.key != "" {
		req.Header.Set(KeyHeader, c.key)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxReply+1))
	if err != nil {
		return err
	}
	if len(data) > maxReply {
		return &session.HostError{Code: "too-large"}
	}
	if resp.StatusCode != http.StatusOK {
		return hostError(resp.StatusCode, data)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return &session.HostError{Code: "reply", Message: "agent-host's answer couldn't be read"}
	}
	return nil
}

func wire(cmd session.Command) commandJSON {
	return commandJSON{Argv: cmd.Argv, Cwd: cmd.Dir, Pass: cmd.Pass, Env: cmd.Env, TimeoutMs: cmd.Timeout.Milliseconds()}
}

func (c *Client) AddWorktree(ctx context.Context, repo, name, branch, base string) (string, error) {
	var out worktreeReply
	if err := c.call(ctx, http.MethodPost, "/v1/worktrees", worktreeJSON{Repo: repo, Name: name, Branch: branch, Base: base}, &out); err != nil {
		return "", err
	}
	if !strings.HasPrefix(out.Path, "/") || strings.ContainsRune(out.Path, 0) || len(out.Path) > 1000 {
		return "", &session.HostError{Code: "reply", Message: "agent-host answered an unusable worktree path"}
	}
	return out.Path, nil
}

func (c *Client) RemoveWorktree(ctx context.Context, repo, name string, force bool) error {
	return c.call(ctx, http.MethodPost, "/v1/worktrees/remove", worktreeJSON{Repo: repo, Name: name, Force: force}, nil)
}

func (c *Client) Run(ctx context.Context, cmd session.Command) (session.RunResult, error) {
	var out runReply
	if err := c.call(ctx, http.MethodPost, "/v1/run", wire(cmd), &out); err != nil {
		return session.RunResult{}, err
	}
	return session.RunResult{Code: out.Code, Stdout: out.Stdout, Stderr: out.Stderr, TimedOut: out.TimedOut}, nil
}

func (c *Client) Stop(ctx context.Context, id string) error {
	err := c.call(ctx, http.MethodPost, "/v1/processes/stop", stopJSON{ID: id}, nil)
	var h *session.HostError
	if errors.As(err, &h) && h.Code == "process" {
		return nil
	}
	return err
}

type Stream struct {
	id   string
	conn net.Conn
	r    *bufio.Reader
}

func (s *Stream) ID() string                  { return s.id }
func (s *Stream) Read(p []byte) (int, error)  { return s.r.Read(p) }
func (s *Stream) Write(p []byte) (int, error) { return s.conn.Write(p) }
func (s *Stream) Close() error                { return s.conn.Close() }

func (c *Client) Start(ctx context.Context, cmd session.Command) (session.Process, error) {
	data, err := json.Marshal(wire(cmd))
	if err != nil {
		return nil, err
	}
	conn, err := c.dialer.DialContext(ctx, "tcp", c.address)
	if err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	}
	req, err := http.NewRequest(http.MethodPost, "http://"+c.address+"/v1/processes", bytes.NewReader(data))
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.key != "" {
		req.Header.Set(KeyHeader, c.key)
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", Upgrade)
	if err := req.Write(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	budget := &headerBudget{r: conn, left: maxHeaders}
	br := bufio.NewReaderSize(budget, 64<<10)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		_ = conn.Close()
		if errors.Is(err, errHeadersTooLarge) {
			return nil, &session.HostError{Code: "too-large", Message: "agent-host's answer headers were over 64 KiB"}
		}
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxProblem))
		_ = resp.Body.Close()
		_ = conn.Close()
		return nil, hostError(resp.StatusCode, body)
	}
	id := resp.Header.Get(ProcessHeader)
	if !processID.MatchString(id) || !strings.EqualFold(resp.Header.Get("Upgrade"), Upgrade) {
		_ = conn.Close()
		return nil, &session.HostError{Code: "reply", Message: "agent-host answered an unusable process id"}
	}
	budget.lifted = true
	_ = conn.SetDeadline(time.Time{})
	return &Stream{id: id, conn: conn, r: br}, nil
}

type ProcessInfo struct {
	ID      string
	Command string
	Running bool
	Exit    *int
}

func (c *Client) Processes(ctx context.Context) ([]ProcessInfo, error) {
	var out struct {
		Processes []processJSON `json:"processes"`
	}
	if err := c.call(ctx, http.MethodGet, "/v1/processes", nil, &out); err != nil {
		return nil, err
	}
	list := make([]ProcessInfo, 0, len(out.Processes))
	for _, p := range out.Processes {
		list = append(list, ProcessInfo{ID: p.ID, Command: p.Command, Running: p.Running, Exit: p.Exit})
	}
	return list, nil
}
