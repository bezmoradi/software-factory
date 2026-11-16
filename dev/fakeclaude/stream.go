package main

import (
	"bufio"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	sessionID  = "fake-reviewer"
	usageFile  = ".sf-fake-usage"
	pacedReads = 10
	readPause  = 200 * time.Millisecond
	slowEvery  = 3 * time.Second
)

type stream struct {
	w     *bufio.Writer
	calls int
	rated bool
}

func newStream() *stream {
	return &stream{w: bufio.NewWriterSize(os.Stdout, 64<<10)}
}

func (s *stream) line(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	_, _ = s.w.Write(data)
	_ = s.w.WriteByte('\n')
	_ = s.w.Flush()
}

func (s *stream) init(args []string) {
	cwd, _ := os.Getwd()
	tools, _ := value(args, "--tools")
	s.line(map[string]any{"type": "system", "subtype": "init", "cwd": cwd, "session_id": sessionID, "tools": strings.Split(tools, ","), "mcp_servers": []any{},
		"model": fakeModel, "permissionMode": "dontAsk", "slash_commands": []any{}, "apiKeySource": "none", "output_style": "default", "agents": []any{}})
}

func (s *stream) read(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	s.calls++
	id := "toolu_fake_" + strconv.Itoa(s.calls)
	abs, _ := filepath.Abs(path)
	s.line(map[string]any{"type": "assistant", "parent_tool_use_id": nil, "session_id": sessionID, "message": map[string]any{
		"id": "msg_fake_" + strconv.Itoa(s.calls), "type": "message", "role": "assistant", "model": fakeModel, "stop_reason": "tool_use",
		"content": []any{map[string]any{"type": "tool_use", "id": id, "name": "Read", "input": map[string]any{"file_path": abs}}}}})
	s.line(map[string]any{"type": "user", "parent_tool_use_id": nil, "session_id": sessionID, "message": map[string]any{
		"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": string(data)}}}})
	if !s.rated {
		s.rated = true
		s.rate()
	}
	if s.calls <= pacedReads {
		time.Sleep(readPause)
	}
}

func (s *stream) readAll() {
	s.read("change.diff")
	s.read("commits.txt")
	_ = filepath.WalkDir("head", func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			s.read(path)
		}
		return nil
	})
}

func (s *stream) slow(d time.Duration) {
	for end := time.Now().Add(d); time.Now().Before(end); {
		time.Sleep(min(slowEvery, time.Until(end)))
		s.read("change.diff")
	}
}

func (s *stream) say(text string) {
	s.line(map[string]any{"type": "assistant", "parent_tool_use_id": nil, "session_id": sessionID, "message": map[string]any{
		"id": "msg_fake_reply", "type": "message", "role": "assistant", "model": fakeModel, "stop_reason": "end_turn",
		"content": []any{map[string]any{"type": "text", "text": text}}}})
}

func (s *stream) rate() {
	now := time.Now()
	windows := map[string]any{}
	add := func(kind string, used float64, reset time.Duration, status string) {
		windows[kind] = map[string]any{"utilization": used, "resetsAt": now.Add(reset).Unix(), "status": status}
	}
	resets := map[string]time.Duration{"five_hour": 2 * time.Hour, "seven_day": 3 * 24 * time.Hour, "seven_day_overage_included": 3 * 24 * time.Hour}
	data, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), usageFile))
	if err != nil {
		add("five_hour", 0.25, resets["five_hour"], "allowed")
		add("seven_day", 0.4, resets["seven_day"], "allowed")
		add("seven_day_overage_included", 0.1, resets["seven_day_overage_included"], "allowed")
	}
	for l := range strings.SplitSeq(string(data), "\n") {
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		reset, known := resets[f[0]]
		used, err := strconv.ParseFloat(f[1], 64)
		if !known || err != nil {
			continue
		}
		status := "allowed"
		if len(f) > 2 {
			status = f[2]
		}
		add(f[0], used, reset, status)
	}
	s.line(map[string]any{"type": "rate_limit_event", "uuid": "fake-rate-limit", "session_id": sessionID, "rate_limit_info": map[string]any{
		"status": "allowed", "resetsAt": now.Add(2 * time.Hour).Unix(), "rateLimitType": "five_hour", "utilization": 0.25, "unifiedWindows": windows}})
}
