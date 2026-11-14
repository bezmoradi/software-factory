package agenthost

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	reviewRunRoute  = "/v1/review-run"
	streamType      = "application/x-ndjson"
	progressEvery   = time.Second
	heartbeatEvery  = 5 * time.Second
	maxCountedLine  = 4 << 20
	typeHead        = 64
	maxStreamFrames = 20000
	maxFrameLine    = MaxRunStdout + session.MaxRateLine + 8*MaxRunStderr + 64<<10
)

type progressFrame struct {
	Type      string `json:"type"`
	ElapsedMs int64  `json:"elapsedMs"`
	ToolCalls int    `json:"toolCalls"`
}

type endFrame struct {
	Type      string          `json:"type"`
	ElapsedMs int64           `json:"elapsedMs"`
	ToolCalls int             `json:"toolCalls"`
	Code      int             `json:"code"`
	TimedOut  bool            `json:"timedOut,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	RateLimit json.RawMessage `json:"rateLimit,omitempty"`
	Stderr    string          `json:"stderr"`
}

type streamFrame struct {
	Type      string          `json:"type"`
	ElapsedMs int64           `json:"elapsedMs"`
	ToolCalls int             `json:"toolCalls"`
	Code      int             `json:"code"`
	TimedOut  bool            `json:"timedOut"`
	Result    json.RawMessage `json:"result"`
	RateLimit json.RawMessage `json:"rateLimit"`
	Stderr    string          `json:"stderr"`
}

type streamScan struct {
	mu     sync.Mutex
	head   []byte
	line   []byte
	limit  int
	over   bool
	tools  int
	result []byte
	rate   []byte
	lost   bool
}

func (s *streamScan) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		chunk := p
		if i >= 0 {
			chunk = p[:i]
		}
		s.add(chunk)
		if i < 0 {
			break
		}
		s.end()
		p = p[i+1:]
	}
	return n, nil
}

func (s *streamScan) add(chunk []byte) {
	if s.limit < 0 {
		return
	}
	if s.limit == 0 {
		room := typeHead - len(s.head)
		take := min(room, len(chunk))
		s.head = append(s.head, chunk[:take]...)
		chunk = chunk[take:]
		if len(s.head) < typeHead {
			return
		}
		s.decide()
		if s.limit < 0 {
			return
		}
	}
	if s.over || len(s.line)+len(chunk) > s.limit {
		s.over = true
		return
	}
	s.line = append(s.line, chunk...)
}

func (s *streamScan) decide() {
	s.limit = MaxRunStdout
	if kind, ok := headType(s.head); ok {
		switch kind {
		case "result":
			s.limit = MaxRunStdout
		case "rate_limit_event":
			s.limit = session.MaxRateLine
		case "assistant":
			s.limit = maxCountedLine
		default:
			s.limit = -1
		}
	}
	if s.limit > 0 {
		s.line = append(s.line[:0], s.head...)
		s.over = len(s.line) > s.limit
	}
}

func headType(head []byte) (string, bool) {
	rest, ok := bytes.CutPrefix(head, []byte(`{"type":"`))
	if !ok {
		return "", false
	}
	i := bytes.IndexByte(rest, '"')
	if i < 0 || bytes.IndexByte(rest[:i], '\\') >= 0 {
		return "", false
	}
	return string(rest[:i]), true
}

func (s *streamScan) end() {
	if s.limit == 0 {
		s.decide()
	}
	line := bytes.TrimSpace(s.line)
	switch {
	case s.limit < 0:
	case s.over:
		if t, ok := headType(s.head); ok && t == "result" {
			s.lost = true
		}
	case len(line) > 0 && line[0] == '{':
		s.take(line)
	}
	s.head, s.line, s.limit, s.over = s.head[:0], s.line[:0], 0, false
}

func (s *streamScan) take(line []byte) {
	var m struct {
		Type    string `json:"type"`
		Message struct {
			Content []struct {
				Type string `json:"type"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &m) != nil {
		return
	}
	switch m.Type {
	case "result":
		s.result = bytes.Clone(line)
		s.lost = false
	case "rate_limit_event":
		if len(line) <= session.MaxRateLine {
			s.rate = bytes.Clone(line)
		}
	case "assistant":
		for _, c := range m.Message.Content {
			if c.Type == "tool_use" && s.tools < session.MaxToolCalls {
				s.tools++
			}
		}
	}
}

func (s *streamScan) finish() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.head) > 0 || len(s.line) > 0 {
		s.end()
	}
}

func (s *streamScan) counts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tools
}

func (s *Server) runTimeout(q commandJSON) time.Duration {
	timeout := DefaultRunTimeout
	longest := MaxRunTimeout
	if s.cfg.Review {
		longest = ReviewRunTimeout
	}
	if q.TimeoutMs > 0 {
		timeout = min(time.Duration(q.TimeoutMs)*time.Millisecond, longest)
	}
	return timeout
}

func (s *Server) reviewRun(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Review {
		refuse(w, fail(http.StatusNotFound, "route", ""))
		return
	}
	var q commandJSON
	if p := decode(w, r, &q); p != nil {
		refuse(w, p)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.runTimeout(q))
	defer cancel()
	cmd, p := s.prepare(ctx, q)
	if p != nil {
		refuse(w, p)
		return
	}
	s.mu.Lock()
	if s.runs >= MaxRuns {
		s.mu.Unlock()
		refuse(w, fail(http.StatusTooManyRequests, "full", "agent-host runs as many commands as it allows."))
		return
	}
	s.runs++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.runs--
		s.mu.Unlock()
	}()
	scan := &streamScan{}
	stderr := &capped{limit: MaxRunStderr}
	cmd.Stdout, cmd.Stderr = scan, stderr
	cmd.Cancel = func() error { return signalGroup(cmd.Process.Pid, true) }
	cmd.WaitDelay = drainWait
	started := time.Now()
	if err := cmd.Start(); err != nil {
		refuse(w, fail(http.StatusUnprocessableEntity, "start", "The command couldn't start."))
		return
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	h := w.Header()
	h.Set("Content-Type", streamType)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	send := func(v any) {
		if enc.Encode(v) != nil || rc.Flush() != nil {
			cancel()
		}
	}
	tick := time.NewTicker(progressEvery)
	defer tick.Stop()
	last, sentAt := -1, started
	var err error
wait:
	for {
		select {
		case err = <-done:
			break wait
		case <-tick.C:
			n := scan.counts()
			if n != last || time.Since(sentAt) >= heartbeatEvery {
				last, sentAt = n, time.Now()
				send(progressFrame{Type: "progress", ElapsedMs: time.Since(started).Milliseconds(), ToolCalls: n})
			}
		}
	}
	scan.finish()
	code := -1
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) && !errors.Is(err, exec.ErrWaitDelay) && ctx.Err() == nil {
		s.log.Warn("agent-host review run", "event", "wait-failed", "command", filepath.Base(q.Argv[0]))
	}
	scan.mu.Lock()
	out := endFrame{Type: "end", ElapsedMs: time.Since(started).Milliseconds(), ToolCalls: scan.tools, Code: code, TimedOut: errors.Is(ctx.Err(), context.DeadlineExceeded),
		Result: scan.result, RateLimit: scan.rate, Stderr: string(stderr.buf)}
	lost := scan.lost
	scan.mu.Unlock()
	if lost {
		s.log.Warn("agent-host review run", "event", "result-too-large", "command", filepath.Base(q.Argv[0]))
	}
	s.log.Info("agent-host review run", "command", filepath.Base(q.Argv[0]), "exit", code, "tools", out.ToolCalls)
	send(out)
}

func readFrame(br *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := br.ReadSlice('\n')
		if len(line)+len(chunk) > maxFrameLine {
			return nil, &session.HostError{Code: "too-large", Message: "a line of agent-host's answer was too large"}
		}
		line = append(line, chunk...)
		if !errors.Is(err, bufio.ErrBufferFull) {
			return line, err
		}
	}
}

func (c *Client) RunStreamed(ctx context.Context, cmd session.Command, progress func(session.RunProgress)) (session.StreamedRun, error) {
	data, err := json.Marshal(wire(cmd))
	if err != nil {
		return session.StreamedRun{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+c.address+reviewRunRoute, bytes.NewReader(data))
	if err != nil {
		return session.StreamedRun{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.key != "" {
		req.Header.Set(KeyHeader, c.key)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return session.StreamedRun{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxProblem))
		return session.StreamedRun{}, hostError(resp.StatusCode, body)
	}
	if kind, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type")); err != nil || kind != streamType {
		return session.StreamedRun{}, &session.HostError{Code: "reply", Message: "agent-host's answer wasn't a stream of review progress"}
	}
	br := bufio.NewReaderSize(resp.Body, 64<<10)
	longest := (ReviewRunTimeout + clientSlack).Milliseconds()
	for range maxStreamFrames {
		line, err := readFrame(br)
		if err != nil && (len(line) == 0 || !errors.Is(err, io.EOF)) {
			if _, ok := errors.AsType[*session.HostError](err); ok {
				return session.StreamedRun{}, err
			}
			if ctx.Err() != nil {
				return session.StreamedRun{}, ctx.Err()
			}
			return session.StreamedRun{}, &session.HostError{Code: "reply", Message: "agent-host's answer ended before the command did"}
		}
		var f streamFrame
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.DisallowUnknownFields()
		if dec.Decode(&f) != nil || dec.More() || f.ElapsedMs < 0 || f.ElapsedMs > longest || f.ToolCalls < 0 || f.ToolCalls > session.MaxToolCalls {
			return session.StreamedRun{}, &session.HostError{Code: "reply", Message: "agent-host's answer couldn't be read"}
		}
		elapsed := time.Duration(f.ElapsedMs) * time.Millisecond
		switch f.Type {
		case "progress":
			if progress != nil {
				progress(session.RunProgress{Elapsed: elapsed, ToolCalls: f.ToolCalls})
			}
			continue
		case "end":
		default:
			return session.StreamedRun{}, &session.HostError{Code: "reply", Message: "agent-host's answer couldn't be read"}
		}
		if f.Code < -1 || f.Code > 255 || len(f.Result) > MaxRunStdout || len(f.RateLimit) > session.MaxRateLine || len(f.Stderr) > 4*MaxRunStderr {
			return session.StreamedRun{}, &session.HostError{Code: "reply", Message: "agent-host's answer couldn't be read"}
		}
		if rest, _ := br.Peek(1); len(rest) > 0 {
			return session.StreamedRun{}, &session.HostError{Code: "reply", Message: "agent-host's answer went on after the command ended"}
		}
		return session.StreamedRun{Code: f.Code, Result: string(f.Result), RateLimit: string(f.RateLimit), Stderr: f.Stderr, TimedOut: f.TimedOut, Elapsed: elapsed, ToolCalls: f.ToolCalls}, nil
	}
	return session.StreamedRun{}, &session.HostError{Code: "reply", Message: "agent-host sent more progress than the engine reads"}
}

var _ session.StreamHost = (*Client)(nil)
