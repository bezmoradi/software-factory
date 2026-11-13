package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
	codeAuthRequired   = -32000
)

type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	return "acp: agent error " + strconv.Itoa(e.Code) + ": " + clip(e.Message)
}

type inbound struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *RPCError       `json:"error"`
}

type outRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

type outNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

type outResult struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result"`
}

type outError struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Error   RPCError        `json:"error"`
}

type callback func(result json.RawMessage, rerr *RPCError, err error)

var (
	errLineTooLong = errors.New("acp: line too long")
	nullID         = json.RawMessage("null")
)

func readLine(br *bufio.Reader, max int) ([]byte, error) {
	var line []byte
	over := false
	for {
		chunk, err := br.ReadSlice('\n')
		if !over {
			if len(line)+len(chunk) > max+1 {
				over = true
				line = nil
			} else {
				line = append(line, chunk...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if over {
			if err == nil {
				return nil, errLineTooLong
			}
			return nil, err
		}
		return line, err
	}
}

func (c *Client) readLoop() {
	br := bufio.NewReaderSize(c.rw, 64<<10)
	for {
		line, err := readLine(br, c.maxLine)
		if errors.Is(err, errLineTooLong) {
			c.warn("oversized-line")
			continue
		}
		if len(line) > 0 {
			c.handle(line)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				c.finish("the agent closed its output")
			} else {
				c.finish("reading from the agent failed")
			}
			return
		}
	}
}

func (c *Client) handle(line []byte) {
	defer func() {
		if recover() != nil {
			c.warn("handler-panic")
		}
	}()
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return
	}
	var m inbound
	if err := json.Unmarshal(line, &m); err != nil {
		c.warn("malformed-line")
		c.replyError(nullID, codeParseError, "Parse error")
		return
	}
	switch {
	case m.Method != "" && m.ID != nil:
		if !validID(m.ID) {
			c.warn("invalid-request")
			c.replyError(nullID, codeInvalidRequest, "Invalid request")
			return
		}
		c.handleRequest(m)
	case m.Method != "":
		c.handleNotification(m)
	case m.ID != nil && (m.Result != nil || m.Error != nil):
		c.handleResponse(m)
	default:
		c.warn("invalid-request")
		c.replyError(nullID, codeInvalidRequest, "Invalid request")
	}
}

func validID(id json.RawMessage) bool {
	if len(id) == 0 {
		return false
	}
	if id[0] == '"' {
		var s string
		return json.Unmarshal(id, &s) == nil
	}
	_, err := strconv.ParseInt(string(id), 10, 64)
	return err == nil
}

func (c *Client) handleRequest(m inbound) {
	switch m.Method {
	case "session/request_permission":
		c.onPermission(m.ID, m.Params)
	default:
		c.note(refusedKind(m.Method))
		c.replyError(m.ID, codeMethodNotFound, "Method not found")
	}
}

func refusedKind(method string) string {
	switch {
	case strings.HasPrefix(method, "fs/"):
		return "refused-fs"
	case strings.HasPrefix(method, "terminal/"):
		return "refused-terminal"
	case strings.HasPrefix(method, "elicitation/"):
		return "refused-elicitation"
	default:
		return "refused-unknown-method"
	}
}

func (c *Client) handleNotification(m inbound) {
	switch m.Method {
	case "session/update":
		c.onUpdate(m.Params)
	case "$/cancel_request":
		c.onCancelRequest(m.Params)
	default:
		c.note("ignored-notification")
	}
}

func (c *Client) handleResponse(m inbound) {
	id, err := strconv.ParseInt(string(m.ID), 10, 64)
	if err != nil {
		c.note("unknown-response")
		return
	}
	c.mu.Lock()
	cb := c.calls[id]
	delete(c.calls, id)
	c.mu.Unlock()
	if cb == nil {
		c.note("unknown-response")
		return
	}
	if m.Error != nil {
		cb(nil, m.Error, nil)
		return
	}
	cb(m.Result, nil, nil)
}

func (c *Client) replyError(id json.RawMessage, code int, message string) {
	_ = c.enqueue(outError{JSONRPC: "2.0", ID: id, Error: RPCError{Code: code, Message: message}})
}

func (c *Client) enqueue(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if bytes.IndexByte(b, '\n') >= 0 {
		return errors.New("acp: message contains a newline")
	}
	b = append(b, '\n')
	c.wmu.Lock()
	if c.wclosed {
		c.wmu.Unlock()
		return ErrClosed
	}
	if c.wqBytes+len(b) > maxWriteBytes {
		c.wmu.Unlock()
		c.warn("write-backlog")
		go c.finish("the agent stopped reading its input")
		return ErrClosed
	}
	c.wq = append(c.wq, b)
	c.wqBytes += len(b)
	c.wmu.Unlock()
	select {
	case c.wwake <- struct{}{}:
	default:
	}
	return nil
}

func (c *Client) writeLoop() {
	defer close(c.writerDone)
	for {
		b, ok := c.nextWrite()
		if !ok {
			select {
			case <-c.wwake:
				continue
			case <-c.stop:
				for {
					b, ok := c.nextWrite()
					if !ok {
						return
					}
					if _, err := c.rw.Write(b); err != nil {
						return
					}
				}
			}
		}
		if _, err := c.rw.Write(b); err != nil {
			go c.finish("writing to the agent failed")
			return
		}
	}
}

func (c *Client) nextWrite() ([]byte, bool) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if len(c.wq) == 0 {
		return nil, false
	}
	b := c.wq[0]
	c.wq[0] = nil
	c.wq = c.wq[1:]
	c.wqBytes -= len(b)
	return b, true
}

func (c *Client) registerLocked(cb callback) (int64, error) {
	if c.ended {
		return 0, ErrClosed
	}
	c.nextID++
	c.calls[c.nextID] = cb
	return c.nextID, nil
}

type reply struct {
	result json.RawMessage
	rerr   *RPCError
	err    error
}

func (c *Client) call(ctx context.Context, method string, params any, inReader func(json.RawMessage)) (json.RawMessage, error) {
	ch := make(chan reply, 1)
	c.mu.Lock()
	id, err := c.registerLocked(func(result json.RawMessage, rerr *RPCError, err error) {
		if err == nil && rerr == nil && inReader != nil {
			inReader(result)
		}
		ch <- reply{result, rerr, err}
	})
	if err == nil {
		err = c.enqueue(outRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params})
		if err != nil {
			delete(c.calls, id)
		}
	}
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, r.err
		}
		if r.rerr != nil {
			return nil, r.rerr
		}
		return r.result, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.calls, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (c *Client) closeConn() {
	c.connOnce.Do(func() {
		select {
		case <-c.writerDone:
		case <-time.After(time.Second):
		}
		c.connErr = c.rw.Close()
	})
}

func clip(s string) string {
	s = strings.ToValidUTF8(s, "�")
	if len(s) <= maxReasonBytes {
		return s
	}
	cut := maxReasonBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
