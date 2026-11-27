package agenthost

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

const (
	AccessTool          = "factory_request_access"
	AccessRoute         = "/access-request"
	maxAccessHost       = 253
	maxAccessReason     = 200
	maxAccessCredential = 40
)

var accessResults = []string{"allowed", "held", "refused"}

const accessDescription = "Ask the person to let this session reach an HTTPS host that egress refuses, such as api.example.com, or .example.com for every name under it; or a port of one host over plain TCP, such as port 22 of a machine on their network at 192.168.1.20 or port 5432 of db.example.com, by giving host and port; or to hand over a credential they saved on Software Factory's page, by its name, such as npm-ci. Give host (with port for plain TCP) or credential, and reason, one line of at most 200 characters saying why the task needs it. It doesn't open or hand over anything: Software Factory's page shows a card to the person, who decides whether to allow it, for how long and for which sessions. It answers allowed when the host is already open or the credential already given (saying where: the variable or the file; for an SSH key, the agent socket Software Factory signs logins through, so ssh, scp and rsync work without the key; for AWS keys, the AWS profile holding short-lived copies), held while the person decides (their answer reaches you with your next message), or refused with the reason. You never get a credential's value in the answer, only where it is. Software Factory refuses every host, port and credential that would open a git host, since it reaches the git host itself: push with factory_push, open a pull request with factory_pull_request or an issue with factory_issue, and read CI with factory_checks. One request waits at a time, and what the person didn't allow can't be asked for again for 10 minutes."

func accessToolDefinition() map[string]any {
	return map[string]any{
		"name":        AccessTool,
		"title":       "Ask for network access",
		"description": accessDescription,
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"host":       map[string]any{"type": "string", "minLength": 1, "maxLength": maxAccessHost, "description": "A host name like api.example.com, or .example.com for every name under it; with port, one host name or an address like 192.168.1.20; leave it out when you give credential"},
				"port":       map[string]any{"type": "integer", "minimum": 1, "maximum": 65535, "description": "A port of host to reach over plain TCP, like 22 for SSH; leave it out for HTTPS"},
				"credential": map[string]any{"type": "string", "minLength": 1, "maxLength": maxAccessCredential, "description": "The name of a credential the person saved, like npm-ci; leave it out when you give host"},
				"reason":     map[string]any{"type": "string", "minLength": 1, "maxLength": maxAccessReason, "description": "One line of plain text"},
			},
			"required":             []string{"reason"},
			"additionalProperties": false,
		},
		"outputSchema": resultSchema(accessResults),
		"annotations":  map[string]any{"destructiveHint": false, "idempotentHint": true, "openWorldHint": true},
	}
}

type accessAsk struct {
	host, credential, reason string
	port                     int
}

func accessArguments(raw json.RawMessage) (accessAsk, string) {
	var args map[string]json.RawMessage
	if json.Unmarshal(bytes.TrimSpace(raw), &args) != nil {
		return accessAsk{}, "Give the arguments as an object with host or credential, and reason."
	}
	for k := range args {
		if k != "host" && k != "port" && k != "credential" && k != "reason" {
			return accessAsk{}, "Only host, port, credential and reason are accepted; " + clipText(k, 64) + " isn't."
		}
	}
	var q accessAsk
	_, hasHost := args["host"]
	_, hasCredential := args["credential"]
	if hasHost == hasCredential {
		return accessAsk{}, "Give either host or credential, not both."
	}
	if hasHost && json.Unmarshal(args["host"], &q.host) != nil || hasCredential && json.Unmarshal(args["credential"], &q.credential) != nil || json.Unmarshal(args["reason"], &q.reason) != nil {
		return accessAsk{}, "host, credential and reason must be strings."
	}
	if hasHost && (strings.TrimSpace(q.host) == "" || utf8.RuneCountInString(q.host) > maxAccessHost) {
		return accessAsk{}, "host must be a host name like api.example.com, or an address like 192.168.1.20 with port."
	}
	if raw, hasPort := args["port"]; hasPort && (!hasHost || json.Unmarshal(raw, &q.port) != nil || q.port < 1 || q.port > 65535) {
		return accessAsk{}, "port must be a whole number from 1 to 65535, given with host."
	}
	if hasCredential && (strings.TrimSpace(q.credential) == "" || utf8.RuneCountInString(q.credential) > maxAccessCredential) {
		return accessAsk{}, "credential must be the name of a saved credential, like npm-ci."
	}
	if strings.TrimSpace(q.reason) == "" || utf8.RuneCountInString(q.reason) > maxAccessReason || strings.ContainsAny(q.reason, "\r\n") {
		return accessAsk{}, "The reason must be one line of 1 to 200 characters."
	}
	return q, ""
}

func (m *MCP) access(ctx context.Context, args json.RawMessage) map[string]any {
	q, bad := accessArguments(args)
	if bad != "" {
		return failed(bad)
	}
	ctx, cancel := context.WithTimeout(ctx, pushFor)
	defer cancel()
	body := map[string]any{"reason": q.reason}
	if q.credential != "" {
		body["credential"] = q.credential
	} else {
		body["host"] = q.host
	}
	if q.port != 0 {
		body["port"] = q.port
	}
	a, err := m.ask(ctx, AccessRoute, body)
	if err != nil {
		return failed("The request didn't reach Software Factory's engine: " + clipText(err.Error(), 300) + ". Tell the person what you need and why.")
	}
	a.Reason = clipText(a.Reason, 1000)
	switch a.Result {
	case "allowed":
		return answered(a, "Allowed: "+a.Reason+".")
	case "held":
		return answered(a, "Held: "+a.Reason+".")
	}
	return answered(a, "Refused: "+a.Reason+".")
}
