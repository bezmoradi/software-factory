package agenthost

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	ActionTool      = "factory_action"
	ActionRoute     = "/action-request"
	maxActionName   = 40
	maxActionArgs   = 8
	maxActionArgKey = 24
	maxActionValue  = 512
)

var (
	actionResults = []string{"held", "done", "refused"}
	actionRunID   = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

const actionDescription = "Ask Software Factory to run an action the person saved on its page, such as deploy-pi: a fixed command that uses one of their credentials against one target, in a container of its own. Give name, and args with a value for each argument the action takes, by its name, each one line that matches the pattern the person saved and doesn't start with -. You never get the credential: the person sees the exact command line on a card and accepts it, unless a grant they gave for these arguments still lasts. It answers held at once; the output and exit code reach you with your next message. To read a finished run's whole output, call it with run, the id that message gives, instead of name. A refusal says why. One request waits for the person at a time, and what the person didn't run can't be asked for again with the same arguments for 10 minutes."

func actionToolDefinition() map[string]any {
	return map[string]any{
		"name":        ActionTool,
		"title":       "Run a saved action",
		"description": actionDescription,
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "minLength": 1, "maxLength": maxActionName, "description": "The name of an action the person saved, like deploy-pi; leave it out when you give run"},
				"args": map[string]any{"type": "object", "maxProperties": maxActionArgs, "additionalProperties": map[string]any{"type": "string", "maxLength": maxActionValue}, "description": "A value for each argument the action takes, by its name"},
				"run":  map[string]any{"type": "string", "pattern": "^[0-9a-f]{16}$", "description": "A finished run's id, to read its whole output; leave name and args out"},
			},
			"additionalProperties": false,
		},
		"outputSchema": resultSchema(actionResults),
		"annotations":  map[string]any{"destructiveHint": true, "idempotentHint": false, "openWorldHint": true},
	}
}

func actionArguments(raw json.RawMessage) (map[string]any, string) {
	var args map[string]json.RawMessage
	if json.Unmarshal(bytes.TrimSpace(raw), &args) != nil {
		return nil, "Give the arguments as an object with name and args, or run."
	}
	for k := range args {
		if k != "name" && k != "args" && k != "run" {
			return nil, "Only name, args and run are accepted; " + clipText(k, 64) + " isn't."
		}
	}
	_, hasName := args["name"]
	_, hasRun := args["run"]
	if hasName == hasRun {
		return nil, "Give either name, with args, or run."
	}
	if hasRun {
		var run string
		if _, hasArgs := args["args"]; hasArgs || json.Unmarshal(args["run"], &run) != nil || !actionRunID.MatchString(run) {
			return nil, "run must be the 16-digit id of a run, given alone."
		}
		return map[string]any{"run": run}, ""
	}
	var name string
	if json.Unmarshal(args["name"], &name) != nil || strings.TrimSpace(name) == "" || utf8.RuneCountInString(name) > maxActionName {
		return nil, "name must be the name of a saved action, like deploy-pi."
	}
	values := map[string]string{}
	if raw, ok := args["args"]; ok && json.Unmarshal(raw, &values) != nil {
		return nil, "args must be an object of strings, one for each argument the action takes."
	}
	if len(values) > maxActionArgs {
		return nil, "An action takes at most 8 arguments."
	}
	for k, v := range values {
		if utf8.RuneCountInString(k) > maxActionArgKey || len(v) > maxActionValue {
			return nil, "Each argument has a short name and a value of at most 512 bytes."
		}
	}
	return map[string]any{"name": name, "args": values}, ""
}

func (m *MCP) action(ctx context.Context, raw json.RawMessage) map[string]any {
	body, bad := actionArguments(raw)
	if bad != "" {
		return failed(bad)
	}
	ctx, cancel := context.WithTimeout(ctx, pushFor)
	defer cancel()
	a, err := m.ask(ctx, ActionRoute, body)
	if err != nil {
		return failed("The request didn't reach Software Factory's engine: " + clipText(err.Error(), 300) + ". Tell the person what you wanted to run.")
	}
	switch a.Result {
	case "done":
		return answered(a, clipText(a.Reason, 96<<10))
	case "held":
		return answered(a, "Held: "+clipText(a.Reason, 2000)+".")
	}
	return answered(a, "Refused: "+clipText(a.Reason, 2000)+".")
}
