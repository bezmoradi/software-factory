package agenthost

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	SplitTool       = "factory_split"
	SplitRoute      = "/split-request"
	maxSplitTasks   = 8
	maxSplitTitle   = 120
	maxSplitMessage = 8000
	maxSplitProject = 2048
)

var splitResults = []string{"held", "refused"}

const splitDescription = "Propose splitting this session's goal into smaller tasks, each to run as its own session, in this project or in another repository. Give 1 to 8 tasks, each with a title (one line, at most 120 characters) and the first message its session gets (at most 8,000 characters). For a task whose work belongs to another repository, add project, that repository's https address, such as https://github.com/team/repo.git; leave project out for this session's project. Nothing starts from this call: each task shows on Software Factory's page as a card marked as written by you, with its project's address in full, and the person reads it, may edit it, and starts it or not; when that project isn't in Projects yet, the person's Start adds it first. Each started task is a normal session from its project's main branch, with this session's work type when it's in this project, and at most 3 of them run at once. Work only in this session's worktree: never copy, change or publish another repository yourself, but propose a session there with this tool. After held, end your turn and don't propose the same tasks again."

type splitTaskJSON struct {
	Title   string `json:"title"`
	Message string `json:"message"`
	Project string `json:"project,omitempty"`
}

func splitToolDefinition() map[string]any {
	task := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"title":   map[string]any{"type": "string", "minLength": 1, "maxLength": maxSplitTitle, "description": "One line of plain text"},
			"message": map[string]any{"type": "string", "minLength": 1, "maxLength": maxSplitMessage, "description": "The task's first message"},
			"project": map[string]any{"type": "string", "minLength": 1, "maxLength": maxSplitProject, "description": "Only for a task in another repository: its https address, such as https://github.com/team/repo.git"},
		},
		"required":             []string{"title", "message"},
		"additionalProperties": false,
	}
	return map[string]any{
		"name":        SplitTool,
		"title":       "Propose tasks for separate sessions",
		"description": splitDescription,
		"inputSchema": map[string]any{
			"type":                 "object",
			"properties":           map[string]any{"tasks": map[string]any{"type": "array", "minItems": 1, "maxItems": maxSplitTasks, "items": task}},
			"required":             []string{"tasks"},
			"additionalProperties": false,
		},
		"outputSchema": resultSchema(splitResults),
		"annotations":  map[string]any{"destructiveHint": false, "idempotentHint": false, "openWorldHint": false},
	}
}

func splitArguments(raw json.RawMessage) ([]splitTaskJSON, string) {
	var args map[string]json.RawMessage
	if json.Unmarshal(bytes.TrimSpace(raw), &args) != nil {
		return nil, "Give the arguments as an object with tasks."
	}
	for k := range args {
		if k != "tasks" {
			return nil, "Only tasks is accepted; " + clipText(k, 64) + " isn't."
		}
	}
	var list []map[string]json.RawMessage
	if json.Unmarshal(args["tasks"], &list) != nil || len(list) == 0 {
		return nil, "tasks must be a list of 1 to 8 objects with title and message."
	}
	if len(list) > maxSplitTasks {
		return nil, "Propose at most 8 tasks at once; say in your reply what's left."
	}
	out := make([]splitTaskJSON, 0, len(list))
	for i, t := range list {
		n := strconv.Itoa(i + 1)
		var title, message, project string
		_, other := t["project"]
		want := 2
		if other {
			want = 3
		}
		if len(t) != want || json.Unmarshal(t["title"], &title) != nil || json.Unmarshal(t["message"], &message) != nil || other && json.Unmarshal(t["project"], &project) != nil {
			return nil, "Task " + n + " needs a title and a message, both strings, and nothing else but a project, also a string."
		}
		if other && (strings.TrimSpace(project) == "" || len(project) > maxSplitProject) {
			return nil, "Task " + n + "'s project must be a repository's https address, such as https://github.com/team/repo.git."
		}
		if strings.TrimSpace(title) == "" || utf8.RuneCountInString(title) > maxSplitTitle || strings.ContainsAny(title, "\r\n") {
			return nil, "Task " + n + "'s title must be one line of 1 to 120 characters."
		}
		if strings.TrimSpace(message) == "" || utf8.RuneCountInString(message) > maxSplitMessage {
			return nil, "Task " + n + "'s message must have 1 to 8,000 characters."
		}
		out = append(out, splitTaskJSON{Title: title, Message: message, Project: project})
	}
	return out, ""
}

func (m *MCP) split(ctx context.Context, args json.RawMessage) map[string]any {
	tasks, bad := splitArguments(args)
	if bad != "" {
		return failed(bad)
	}
	ctx, cancel := context.WithTimeout(ctx, pushFor)
	defer cancel()
	a, err := m.ask(ctx, SplitRoute, map[string]any{"tasks": tasks})
	if err != nil {
		return failed("The proposal didn't reach Software Factory's engine: " + clipText(err.Error(), 300) + ". End your turn and tell the person the tasks you had in mind.")
	}
	a.Reason = clipText(a.Reason, 1000)
	if a.Result == "held" {
		return answered(a, "Held: "+a.Reason+".")
	}
	return answered(a, "Refused: "+a.Reason+". Don't retry factory_split with the same tasks; tell the person what you wanted instead.")
}
