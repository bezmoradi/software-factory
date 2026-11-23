package agenthost

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

const (
	WorkTypeTool  = "factory_work_type"
	WorkTypeRoute = "/work-type-request"
	maxTypeID     = 32
	maxTypeReason = 200
)

const workTypeDescription = "Suggest this session's work type, after Software Factory held a file change because the session has none. Give type, the id of one of the project's work types (the hold's message lists them; the built-in ones are feature, fix, incident, docs and explore), and reason, one line of at most 200 characters saying why it fits. It doesn't set the work type: the person reads your suggestion on Software Factory's page and picks that type or another, and your next message says when you can go on. A new suggestion replaces the last one. After held, end your turn and don't change files another way."

func workTypeToolDefinition() map[string]any {
	return map[string]any{
		"name":        WorkTypeTool,
		"title":       "Suggest a work type",
		"description": workTypeDescription,
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"type":   map[string]any{"type": "string", "minLength": 1, "maxLength": maxTypeID, "description": "A work type's id, such as fix"},
				"reason": map[string]any{"type": "string", "minLength": 1, "maxLength": maxTypeReason, "description": "One line of plain text"},
			},
			"required":             []string{"type", "reason"},
			"additionalProperties": false,
		},
		"outputSchema": resultSchema(splitResults),
		"annotations":  map[string]any{"destructiveHint": false, "idempotentHint": true, "openWorldHint": false},
	}
}

func workTypeArguments(raw json.RawMessage) (string, string, string) {
	var args map[string]json.RawMessage
	if json.Unmarshal(bytes.TrimSpace(raw), &args) != nil {
		return "", "", "Give the arguments as an object with type and reason."
	}
	for k := range args {
		if k != "type" && k != "reason" {
			return "", "", "Only type and reason are accepted; " + clipText(k, 64) + " isn't."
		}
	}
	var id, reason string
	if json.Unmarshal(args["type"], &id) != nil || json.Unmarshal(args["reason"], &reason) != nil {
		return "", "", "type and reason must both be strings."
	}
	if strings.TrimSpace(id) == "" || utf8.RuneCountInString(id) > maxTypeID {
		return "", "", "type must be a work type's id, such as fix."
	}
	if strings.TrimSpace(reason) == "" || utf8.RuneCountInString(reason) > maxTypeReason || strings.ContainsAny(reason, "\r\n") {
		return "", "", "The reason must be one line of 1 to 200 characters."
	}
	return id, reason, ""
}

func (m *MCP) workType(ctx context.Context, args json.RawMessage) map[string]any {
	id, reason, bad := workTypeArguments(args)
	if bad != "" {
		return failed(bad)
	}
	ctx, cancel := context.WithTimeout(ctx, pushFor)
	defer cancel()
	a, err := m.ask(ctx, WorkTypeRoute, map[string]string{"type": id, "reason": reason})
	if err != nil {
		return failed("The suggestion didn't reach Software Factory's engine: " + clipText(err.Error(), 300) + ". End your turn and tell the person which work type you'd pick and why.")
	}
	a.Reason = clipText(a.Reason, 1000)
	if a.Result == "held" {
		return answered(a, "Held: "+a.Reason+".")
	}
	return answered(a, "Refused: "+a.Reason+".")
}
