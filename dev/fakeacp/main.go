package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type stdioServer struct {
	Name    string   `json:"name"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Env     []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"env"`
}

type message struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
}

var (
	mu      sync.Mutex
	out     = bufio.NewWriter(os.Stdout)
	prompt  json.RawMessage
	servers []string
	stdio   []stdioServer
	cwd     string
	calls   int
)

var tools = []string{"factory_push", "factory_issue", "factory_pull_request", "factory_checks", "factory_review_comments", "factory_split"}

const noteMarker = "[Software Factory] "

func toolNamed(text string) string {
	found, at := "", len(text)
	for _, t := range tools {
		if i := strings.Index(text, t); i >= 0 && i < at {
			found, at = t, i
		}
	}
	return found
}

func splitArgs(text string) map[string]any {
	var tasks []map[string]string
	for l := range strings.SplitSeq(text, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(l), "task:")
		if !ok {
			continue
		}
		title, message, _ := strings.Cut(rest, "|")
		tasks = append(tasks, map[string]string{"title": strings.TrimSpace(title), "message": strings.TrimSpace(message)})
	}
	if len(tasks) == 0 {
		for i, part := range []string{"the data model", "the page", "the documentation"} {
			tasks = append(tasks, map[string]string{"title": fmt.Sprintf("Part %d: %s", i+1, part), "message": "Build " + part + " for the goal the fake agent split."})
		}
	}
	return map[string]any{"tasks": tasks}
}

func toolArgs(tool, text string) map[string]any {
	if tool == "factory_split" {
		return splitArgs(text)
	}
	if tool != "factory_issue" && tool != "factory_pull_request" {
		return map[string]any{}
	}
	_, after, _ := strings.Cut(text, tool)
	title, _, _ := strings.Cut(strings.TrimSpace(after), "\n")
	title = strings.TrimSpace(title)
	if title == "" {
		title = "A fake request from the fake agent"
	}
	if len(title) > 100 {
		title = title[:100]
	}
	return map[string]any{"title": title, "body": "The fake agent asked for this with " + tool + "."}
}

func callTool(tool string, args map[string]any) string {
	for _, s := range stdio {
		if s.Name != "software-factory" {
			continue
		}
		cmd := exec.Command(s.Command, s.Args...)
		cmd.Env = os.Environ()
		for _, e := range s.Env {
			if e.Name != checkKeyVariable {
				cmd.Env = append(cmd.Env, e.Name+"="+e.Value)
			}
		}
		in, _ := cmd.StdinPipe()
		outPipe, _ := cmd.StdoutPipe()
		if err := cmd.Start(); err != nil {
			return "the MCP server didn't start: " + err.Error()
		}
		defer func() {
			_ = in.Close()
			_ = cmd.Wait()
		}()
		r := bufio.NewScanner(outPipe)
		r.Buffer(make([]byte, 1<<20), 1<<20)
		ask := func(line string) map[string]any {
			_, _ = io.WriteString(in, line+"\n")
			done := make(chan map[string]any, 1)
			go func() {
				var m map[string]any
				if r.Scan() {
					_ = json.Unmarshal(r.Bytes(), &m)
				}
				done <- m
			}()
			select {
			case m := <-done:
				return m
			case <-time.After(90 * time.Second):
				return nil
			}
		}
		ask(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"fake","version":"0"}}}`)
		_, _ = io.WriteString(in, `{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n")
		call, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}})
		m := ask(string(call))
		res, _ := m["result"].(map[string]any)
		content, _ := res["content"].([]any)
		if len(content) > 0 {
			if c, ok := content[0].(map[string]any); ok {
				text, _ := c["text"].(string)
				return text
			}
		}
		b, _ := json.Marshal(m)
		return "no answer from " + tool + ": " + string(b)
	}
	return "no software-factory MCP server was given"
}

const session = "sess-fake-1"

func send(v any) {
	mu.Lock()
	defer mu.Unlock()
	b, _ := json.Marshal(v)
	_, _ = out.Write(append(b, '\n'))
	_ = out.Flush()
}

func reply(id json.RawMessage, result any) {
	send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func update(u map[string]any) {
	send(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": session, "update": u}})
}

func chunk(text string) {
	update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": text}})
}

func main() {
	if authStatus(os.Args[1:]) || authLogin(os.Args[1:]) {
		return
	}
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 16<<20)
	for in.Scan() {
		var m message
		if json.Unmarshal(in.Bytes(), &m) != nil {
			continue
		}
		switch {
		case m.Method == "initialize":
			reply(m.ID, map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"promptCapabilities": map[string]any{"image": os.Getenv("FAKE_ACP_NO_IMAGES") == ""}}, "authMethods": []any{}})
		case m.Method == "session/new":
			var p struct {
				Cwd        string        `json:"cwd"`
				MCPServers []stdioServer `json:"mcpServers"`
			}
			_ = json.Unmarshal(m.Params, &p)
			cwd = p.Cwd
			for _, s := range p.MCPServers {
				servers = append(servers, s.Name)
			}
			stdio = p.MCPServers
			created := map[string]any{"sessionId": session, "modes": map[string]any{"currentModeId": mode, "availableModes": []any{
				map[string]any{"id": "default", "name": "Default"}, map[string]any{"id": "yolo", "name": "YOLO"},
			}}}
			if offersConfigMode() {
				created["configOptions"] = configOptions()
			}
			reply(m.ID, created)
		case m.Method == "session/set_config_option":
			var p struct {
				ConfigID string `json:"configId"`
				Value    string `json:"value"`
			}
			_ = json.Unmarshal(m.Params, &p)
			if p.ConfigID != "mode" || (p.Value != "default" && p.Value != "yolo") {
				send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": -32602, "message": "Invalid params"}})
				continue
			}
			if !ignoresSetMode() {
				mode = p.Value
			}
			reply(m.ID, map[string]any{"configOptions": configOptions()})
			commandsUpdate()
		case m.Method == "session/set_mode":
			reply(m.ID, map[string]any{})
			if !ignoresSetMode() {
				var p struct {
					ModeID string `json:"modeId"`
				}
				_ = json.Unmarshal(m.Params, &p)
				mode = p.ModeID
				update(map[string]any{"sessionUpdate": "current_mode_update", "currentModeId": p.ModeID})
			}
			commandsUpdate()
		case m.Method == "session/prompt":
			promptFor(m)
		case m.Method == "session/cancel":
			if prompt != nil {
				reply(prompt, map[string]any{"stopReason": "cancelled"})
				prompt = nil
			}
		case m.Method == "" && string(m.ID) == "100":
			var r struct {
				Outcome struct {
					Outcome  string `json:"outcome"`
					OptionID string `json:"optionId"`
				} `json:"outcome"`
			}
			_ = json.Unmarshal(m.Result, &r)
			if r.Outcome.OptionID == "allow" {
				update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "call-1", "status": "completed"})
				chunk("It ran.")
			} else {
				update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "call-1", "status": "failed"})
				chunk("It didn't run.")
			}
			if prompt != nil {
				reply(prompt, map[string]any{"stopReason": "end_turn"})
				prompt = nil
			}
		case m.ID != nil && m.Method != "":
			send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": -32601, "message": "Method not found"}})
		}
	}
}

var mode = "yolo"

func configOptions() []any {
	return []any{map[string]any{"id": "mode", "name": "Mode", "category": "mode", "type": "select", "currentValue": mode, "options": []any{
		map[string]any{"value": "default", "name": "Default"}, map[string]any{"value": "yolo", "name": "YOLO"},
	}}}
}

func commandsUpdate() {
	update(map[string]any{"sessionUpdate": "available_commands_update", "availableCommands": []any{
		map[string]any{"name": "test", "description": "Run the tests"},
	}})
}

func offersConfigMode() bool {
	_, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".sf-fake-config-mode"))
	return err == nil
}

func ignoresSetMode() bool {
	_, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".sf-fake-ignore-set-mode"))
	return err == nil
}

func received(params json.RawMessage) string {
	var p struct {
		Prompt []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			MimeType string `json:"mimeType"`
			Data     string `json:"data"`
			URI      string `json:"uri"`
			Name     string `json:"name"`
		} `json:"prompt"`
	}
	if json.Unmarshal(params, &p) != nil || len(p.Prompt) < 2 {
		return ""
	}
	out := []string{"The fake agent received " + fmt.Sprint(len(p.Prompt)) + " content blocks:"}
	for _, b := range p.Prompt {
		switch b.Type {
		case "text":
			out = append(out, fmt.Sprintf("- text: %q", b.Text))
		case "image":
			data, err := base64.StdEncoding.DecodeString(b.Data)
			sum := sha256.Sum256(data)
			out = append(out, fmt.Sprintf("- image: %s, %d bytes, sha256 %s, uri %s, decoded: %v", b.MimeType, len(data), hex.EncodeToString(sum[:])[:16], b.URI, err == nil))
		case "resource_link":
			u, _ := url.Parse(b.URI)
			body, err := os.ReadFile(u.Path)
			out = append(out, fmt.Sprintf("- resource_link: %s (%s), readable here: %v, %d bytes", b.URI, b.Name, err == nil, len(body)))
		default:
			out = append(out, "- "+b.Type)
		}
	}
	return strings.Join(out, "\n")
}

func promptText(params json.RawMessage) string {
	var p struct {
		Prompt []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"prompt"`
	}
	_ = json.Unmarshal(params, &p)
	var texts []string
	for _, b := range p.Prompt {
		if b.Type == "text" {
			texts = append(texts, b.Text)
		}
	}
	return strings.Join(texts, "\n")
}

func splitNote(text string) (string, string) {
	note, rest, _ := strings.Cut(strings.TrimPrefix(text, noteMarker), "\n\n")
	return note, rest
}

func scripted(id json.RawMessage, text string) bool {
	lines := strings.Split(text, "\n")
	said, done := false, false
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "say:") {
			said = true
		}
	}
	if !said {
		return false
	}
	for _, l := range lines {
		key, value, _ := strings.Cut(strings.TrimSpace(l), ":")
		value = strings.TrimSpace(value)
		calls++
		callID := fmt.Sprintf("script-%d", calls)
		switch key {
		case "run":
			update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": callID, "title": "Terminal", "kind": "execute", "status": "completed", "rawInput": map[string]any{"command": value}})
		case "fail":
			update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": callID, "title": "Terminal", "kind": "execute", "status": "failed", "rawInput": map[string]any{"command": value}})
		case "cost":
			if usd, err := strconv.ParseFloat(value, 64); err == nil {
				update(map[string]any{"sessionUpdate": "usage_update", "used": 1200, "size": 200000, "cost": map[string]any{"amount": usd, "currency": "USD"}})
			}
		case "edit":
			file := filepath.Join(cwd, filepath.Clean("/"+value))
			status := "completed"
			if f, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0); err != nil {
				status = "failed"
			} else {
				_, _ = f.WriteString("\n")
				_ = f.Close()
			}
			update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": callID, "title": "Edit " + value, "kind": "edit", "status": status, "rawInput": map[string]any{"file_path": file}})
		case "write":
			file := filepath.Join(cwd, filepath.Clean("/"+value))
			status := "completed"
			if err := os.WriteFile(file, []byte("ok\n"), 0o644); err != nil {
				status = "failed"
			}
			update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": callID, "title": "Write " + value, "kind": "edit", "status": status, "rawInput": map[string]any{"file_path": file}})
		case "grow-for":
			growFor(callID, value)
		case "say":
			chunk(value + "\n")
		case "done":
			done = true
		default:
			if !tryLine(callID, key, value) {
				placeLine(callID, key, value)
			}
		}
	}
	reply(id, map[string]any{"stopReason": "end_turn"})
	if done {
		time.Sleep(300 * time.Millisecond)
		os.Exit(0)
	}
	return true
}

func answer(id json.RawMessage, text string) bool {
	if scripted(id, text) {
		return true
	}
	if tool := toolNamed(text); tool != "" {
		args := toolArgs(tool, text)
		go func() {
			chunk(tool + " answered: " + callTool(tool, args))
			reply(id, map[string]any{"stopReason": "end_turn"})
		}()
		return true
	}
	if strings.EqualFold(strings.TrimSpace(text), "stall") {
		prompt = id
		chunk("The fake agent is thinking and won't finish this turn until it's stopped. ")
		return true
	}
	return false
}

func promptFor(m message) {
	text := promptText(m.Params)
	if strings.HasPrefix(text, noteMarker) {
		for strings.HasPrefix(text, noteMarker) {
			var note string
			note, text = splitNote(text)
			chunk("The fake agent got a note: " + note + "\n\n")
		}
		if !answer(m.ID, text) {
			reply(m.ID, map[string]any{"stopReason": "end_turn"})
		}
		return
	}
	if answer(m.ID, text) {
		return
	}
	if r := received(m.Params); r != "" {
		chunk(r)
		reply(m.ID, map[string]any{"stopReason": "end_turn"})
		return
	}
	hello(m.ID)
}

func hello(id json.RawMessage) {
	prompt = id
	key := "absent"
	if os.Getenv("ANTHROPIC_API_KEY") != "" || os.Getenv("FAKE_SECRET_TOKEN") != "" {
		key = "present"
	}
	socket := "absent"
	if os.Getenv("SF_PLUGIN_SOCKET") != "" {
		socket = "present"
	}
	chunk("Hello from the fake agent. mcp: " + strings.Join(servers, ",") + "; key: " + key + "; socket: " + socket + ". ")
	update(map[string]any{"sessionUpdate": "usage_update", "used": 1200, "size": 200000, "cost": map[string]any{"amount": 0.0042, "currency": "USD"}})
	update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "call-1", "title": "Run echo hi", "kind": "execute", "status": "pending", "rawInput": map[string]any{"command": "echo hi"}})
	send(map[string]any{"jsonrpc": "2.0", "id": 100, "method": "session/request_permission", "params": map[string]any{
		"sessionId": session, "toolCall": map[string]any{"toolCallId": "call-1"},
		"options": []any{
			map[string]any{"optionId": "allow", "name": "Allow", "kind": "allow_once"},
			map[string]any{"optionId": "always", "name": "Always", "kind": "allow_always"},
			map[string]any{"optionId": "reject", "name": "Reject", "kind": "reject_once"},
		},
	}})
}

func growFor(callID, value string) {
	fields := strings.Fields(value)
	status := "failed"
	file := ""
	if len(fields) == 3 {
		seconds, errSeconds := strconv.Atoi(fields[0])
		mib, errMiB := strconv.Atoi(fields[1])
		file = filepath.Join(cwd, filepath.Clean("/"+fields[2]))
		if errSeconds == nil && errMiB == nil && seconds > 0 && seconds <= 120 && mib > 0 && mib <= 8192 && growFile(file, int64(mib)<<20) {
			status = "completed"
			go func() {
				time.Sleep(time.Duration(seconds) * time.Second)
				_ = os.Remove(file)
			}()
		}
	}
	update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": callID, "title": "Grow " + value, "kind": "edit", "status": status, "rawInput": map[string]any{"file_path": file}})
}

func growFile(file string, size int64) bool {
	f, err := os.OpenFile(file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return false
	}
	grown := f.Truncate(size) == nil
	return f.Close() == nil && grown
}
