package page

import (
	"context"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	actionAskRoute  = "/action-request"
	maxActionText   = 2 * session.MaxActionArgs * session.MaxActionArg
	maxSlotsText    = 2 * session.MaxActionSlots * (session.MaxSlotPattern + 64)
	maxArgName      = 64
	maxActionTarget = 300
)

var actionRun = regexp.MustCompile(`^[0-9a-f]{16}$`)

func actionViewsJSON(list []session.ActionView) []map[string]any {
	out := make([]map[string]any, 0, len(list))
	for _, a := range list {
		slots := make([]map[string]any, 0, len(a.Slots))
		for _, s := range a.Slots {
			slots = append(slots, map[string]any{"name": s.Name, "pattern": s.Pattern, "max": s.Max})
		}
		entry := map[string]any{"id": a.ID, "name": a.Name, "command": a.Command, "argv": a.Argv, "credential": a.Credential, "target": a.Target, "slots": slots, "files": a.Files,
			"minutes": a.Minutes, "saved": unixOr(a.Saved), "level": a.Level, "label": a.Label}
		if a.Broken != "" {
			entry["broken"] = a.Broken
		}
		out = append(out, entry)
	}
	return out
}

func actionRunsJSON(list []session.ActionRunView) []map[string]any {
	out := make([]map[string]any, 0, len(list))
	for _, r := range list {
		entry := map[string]any{"id": r.ID, "name": r.Name, "command": r.Command, "target": r.Target, "credential": r.Credential, "running": r.Running, "state": r.State,
			"started": unixOr(r.Started), "allowed": r.Allowed, "by": r.By}
		if !r.Running {
			entry["code"], entry["output"], entry["cut"], entry["said"], entry["ended"] = r.Code, r.Output, r.Cut, r.Said, unixOr(r.Ended)
		}
		out = append(out, entry)
	}
	return out
}

func actionGrantsJSON(list []session.ActionGrantView) []map[string]any {
	out := make([]map[string]any, 0, len(list))
	for _, g := range list {
		out = append(out, map[string]any{"id": g.ID, "name": g.Name, "command": g.Command, "expires": unixOr(g.Until)})
	}
	return out
}

func argsOf(v any) (map[string]string, bool) {
	if v == nil {
		return map[string]string{}, true
	}
	raw, ok := v.(map[string]any)
	if !ok || len(raw) > session.MaxActionSlots {
		return nil, false
	}
	out := map[string]string{}
	for k, x := range raw {
		t, ok := x.(string)
		if !ok || len(k) > maxArgName || len(t) > 4*session.MaxSlotValue {
			return nil, false
		}
		out[k] = t
	}
	return out, true
}

func commandLines(text string) []string {
	var out []string
	for line := range strings.SplitSeq(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

func slotLines(text string) []session.ActionSlot {
	var out []session.ActionSlot
	for _, line := range commandLines(text) {
		name, rest, _ := strings.Cut(line, " ")
		size, pattern, _ := strings.Cut(strings.TrimSpace(rest), " ")
		n, err := strconv.Atoi(size)
		if err != nil {
			n = -1
		}
		out = append(out, session.ActionSlot{Name: name, Pattern: strings.TrimSpace(pattern), Max: n})
	}
	return out
}

func actionDraftOf(raw any) (session.ActionDraft, bool) {
	body, ok := raw.(map[string]any)
	if !ok || !keysWithin(body, "id", "name", "command", "credential", "target", "slots", "files", "minutes") {
		return session.ActionDraft{}, false
	}
	text := func(key string, limit int) string {
		v, good := accessString(body, key, limit)
		ok = ok && good
		return v
	}
	d := session.ActionDraft{ID: text("id", maxCredentialRef), Name: text("name", maxCredentialRef), Credential: text("credential", maxCredentialRef), Target: text("target", maxActionTarget)}
	d.Command = commandLines(text("command", maxActionText))
	d.Slots = slotLines(text("slots", maxSlotsText))
	if !ok || d.ID != "" && !pushCard.MatchString(d.ID) {
		return session.ActionDraft{}, false
	}
	if v, given := body["files"]; given {
		if d.Files, ok = v.(bool); !ok {
			return session.ActionDraft{}, false
		}
	}
	if v, given := body["minutes"]; given {
		if d.Minutes, ok = wholeNumber(v, 0, 1<<10); !ok {
			return session.ActionDraft{}, false
		}
		if d.Minutes == 0 {
			d.Minutes = -1
		}
	}
	return d, true
}

func (s *Server) accessActions(ctx context.Context, w http.ResponseWriter, op, sid string, body map[string]any) (string, bool) {
	switch op {
	case "action-run":
		id, okID := accessCardID(body, "action")
		args, okArgs := argsOf(body["args"])
		if !keysWithin(body, "op", "sid", "action", "args") || !okID || !okArgs {
			refuseFields(w)
			return "", false
		}
		said, err := s.cfg.Access.StartAction(ctx, sid, id, args)
		if err != nil {
			code, p := status(err)
			send(w, code, p)
			return "", false
		}
		return said, true
	case "action-stop":
		run, okRun := body["run"].(string)
		if !keysWithin(body, "op", "sid", "run") || !okRun || !actionRun.MatchString(run) {
			refuseFields(w)
			return "", false
		}
		said, err := s.cfg.Access.StopAction(ctx, sid, run)
		if err != nil {
			code, p := status(err)
			send(w, code, p)
			return "", false
		}
		return said, true
	}
	refuse(w, http.StatusBadRequest, "op", saidAccessOp)
	return "", false
}

func (s *Server) actionRequest(w http.ResponseWriter, r *http.Request) {
	body, bad := readObject(w, r, MaxAgentBody)
	if bad != 0 {
		agentRefuse(w, bad, "body")
		return
	}
	name, okName := accessString(body, "name", maxCredentialRef)
	run, okRun := accessString(body, "run", 32)
	args, okArgs := argsOf(body["args"])
	if !keysWithin(body, "name", "args", "run") || !okName || !okRun || !okArgs || (name == "") == (run == "") || run != "" && (body["args"] != nil || !actionRun.MatchString(run)) {
		agentRefuse(w, http.StatusBadRequest, "input")
		return
	}
	sid, ok := s.forgeSession(w, r)
	if !ok {
		return
	}
	if s.cfg.Access == nil {
		send(w, http.StatusOK, map[string]string{"result": session.AccessRefused, "reason": "this engine has no control channel to egress, so it runs no actions; tell the person what you need and why"})
		return
	}
	var answer session.AccessAnswer
	if run != "" {
		answer = s.cfg.Access.ActionResult(sid, run)
	} else {
		answer = s.cfg.Access.RunAction(context.WithoutCancel(r.Context()), sid, name, args)
	}
	shared.For(r.Context(), s.logger).Info("action request", "side", "agent", "result", answer.Result)
	send(w, http.StatusOK, map[string]string{"result": answer.Result, "reason": answer.Reason})
}
