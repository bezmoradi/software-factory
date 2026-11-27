package page

import (
	"context"
	"net/http"

	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	accessRoute     = "/api/access"
	accessAskRoute  = "/access-request"
	maxAccessReason = 4 * session.MaxHostReason
	saidAccessOp    = "The engine reads, checks, allows, refuses, applies and takes back access grants, and runs and stops actions, nothing else."
	saidAccessNone  = "This engine has no control channel to egress, so it gives no access grants."
)

func (s *Server) UseAccess(a session.AccessPage) {
	s.cfg.Access = a
}

type accessRiskJSON struct {
	Scope   string   `json:"scope"`
	Expires string   `json:"expires"`
	Level   string   `json:"level"`
	Label   string   `json:"label"`
	Says    []string `json:"says"`
	Accept  string   `json:"accept,omitempty"`
	Off     string   `json:"off,omitempty"`
}

type accessCardJSON struct {
	ID         string              `json:"id,omitempty"`
	Kind       string              `json:"kind"`
	Host       string              `json:"host,omitempty"`
	Port       int                 `json:"port,omitempty"`
	Credential string              `json:"credential,omitempty"`
	Title      string              `json:"title"`
	Why        string              `json:"why,omitempty"`
	Reason     string              `json:"reason,omitempty"`
	Asked      int64               `json:"asked"`
	Action     string              `json:"action,omitempty"`
	Command    string              `json:"command,omitempty"`
	Argv       []string            `json:"argv,omitempty"`
	Target     string              `json:"target,omitempty"`
	Files      bool                `json:"files,omitempty"`
	Minutes    int                 `json:"minutes,omitempty"`
	Risks      []accessRiskJSON    `json:"risks"`
	Scopes     []map[string]string `json:"scopes"`
	Expiries   []map[string]string `json:"expiries"`
}

func accessCardOf(c session.AccessCard) accessCardJSON {
	out := accessCardJSON{ID: c.ID, Kind: c.Kind, Host: c.Host, Port: c.Port, Credential: c.Credential, Title: c.Title, Why: c.Why, Reason: c.Reason, Asked: millis(c.Asked), Risks: make([]accessRiskJSON, 0, len(c.Risks)),
		Scopes: choicesJSON(session.GrantScopes), Expiries: choicesJSON(session.GrantExpiries), Action: c.Action, Command: c.Command, Argv: c.Argv, Target: c.Target, Files: c.Files, Minutes: c.Minutes}
	if len(c.Scopes) > 0 {
		out.Scopes = choicesJSON(c.Scopes)
	}
	if len(c.Expiries) > 0 {
		out.Expiries = choicesJSON(c.Expiries)
	}
	for _, r := range c.Risks {
		says := r.Says
		if says == nil {
			says = []string{}
		}
		out.Risks = append(out.Risks, accessRiskJSON{Scope: r.Scope, Expires: r.Expires, Level: r.Level, Label: r.Label, Says: says, Accept: r.Accept, Off: r.Off})
	}
	return out
}

func accessCardsOf(list []session.AccessCard) []accessCardJSON {
	if len(list) == 0 {
		return nil
	}
	out := make([]accessCardJSON, 0, len(list))
	for _, c := range list {
		out = append(out, accessCardOf(c))
	}
	return out
}

func choicesJSON(list []session.GrantChoice) []map[string]string {
	out := make([]map[string]string, 0, len(list))
	for _, c := range list {
		out = append(out, map[string]string{"value": c.Value, "label": c.Label})
	}
	return out
}

func accessViewJSON(v session.AccessView) map[string]any {
	grants := make([]map[string]any, 0, len(v.Grants))
	for _, g := range v.Grants {
		label := session.GrantScopes[0].Label
		if g.Scope == session.GrantProject {
			label = session.GrantScopes[1].Label
		}
		entry := map[string]any{
			"id": g.ID, "host": g.Host, "scope": g.Scope, "scopeLabel": label, "own": g.Own, "expires": unixOr(g.Expires), "until": g.Until,
			"who": g.Who, "reason": g.Reason, "made": unixOr(g.Made),
		}
		if g.Credential != "" {
			entry["credential"], entry["bound"], entry["delivery"], entry["deliveryTag"] = g.Name, g.Bound, g.Delivery, g.DeliveryTag
		}
		if g.Copy != "" {
			entry["copy"] = g.Copy
		}
		if g.Port > 0 {
			entry["port"], entry["target"] = g.Port, g.Target()
		}
		grants = append(grants, entry)
	}
	cards := make([]accessCardJSON, 0, len(v.Cards))
	for _, c := range v.Cards {
		cards = append(cards, accessCardOf(c))
	}
	creds := make([]map[string]string, 0, len(v.Credentials))
	for _, c := range v.Credentials {
		creds = append(creds, map[string]string{"id": c.ID, "name": c.Name, "typeLabel": c.Label})
	}
	out := map[string]any{
		"grants": grants, "cards": cards, "scopes": choicesJSON(session.GrantScopes), "expiries": choicesJSON(session.GrantExpiries),
		"network": v.Network, "now": v.Now.Unix(), "presets": presetsJSON(v.Presets), "credentials": creds,
	}
	if v.ActionsOn {
		out["actions"], out["actionGrants"], out["runs"] = actionViewsJSON(v.Actions), actionGrantsJSON(v.ActionGrants), actionRunsJSON(v.Runs)
		if v.ActionsOff != "" {
			out["actionsOff"] = v.ActionsOff
		}
	}
	return out
}

func accessString(body map[string]any, key string, limit int) (string, bool) {
	v, present := body[key]
	if !present {
		return "", true
	}
	t, ok := v.(string)
	return t, ok && len(t) <= limit
}

func accessPort(body map[string]any) (int, bool) {
	v, given := body["port"]
	if !given {
		return 0, true
	}
	n, ok := wholeNumber(v, 0, 1<<20)
	if n == 0 {
		n = -1
	}
	return n, ok
}

func accessCardID(body map[string]any, key string) (string, bool) {
	v, ok := body[key].(string)
	return v, ok && pushCard.MatchString(v)
}

func (s *Server) access(w http.ResponseWriter, r *http.Request, body map[string]any) {
	if s.cfg.Access == nil {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: saidAccessNone})
		return
	}
	op, okOp := body["op"].(string)
	if !okOp {
		refuse(w, http.StatusBadRequest, "op", saidAccessOp)
		return
	}
	sid, okSID := body["sid"].(string)
	if !okSID || sid == "" || len(sid) > 80 {
		refuseFields(w)
		return
	}
	ctx := context.WithoutCancel(r.Context())
	var err error
	said := ""
	tone := "ok"
	switch op {
	case "read":
		if !keysWithin(body, "op", "sid") {
			refuseFields(w)
			return
		}
	case "check":
		host, okHost := accessString(body, "host", maxEgressHost)
		port, okPort := accessPort(body)
		cred, okCred := accessString(body, "credential", maxCredentialRef)
		reason, okReason := accessString(body, "reason", maxAccessReason)
		if !keysWithin(body, "op", "sid", "host", "port", "credential", "reason") || !okHost || !okPort || !okCred || !okReason || (host == "") == (cred == "") || cred != "" && port != 0 {
			refuseFields(w)
			return
		}
		card, err := s.cfg.Access.CheckAccess(sid, session.AccessAsk{Host: host, Port: port, Credential: cred, Reason: reason})
		if err != nil {
			code, p := status(err)
			send(w, code, p)
			return
		}
		send(w, http.StatusOK, map[string]any{"card": accessCardOf(card)})
		return
	case "allow":
		q := session.AccessChoice{}
		var okScope, okExp bool
		q.Scope, okScope = body["scope"].(string)
		q.Expires, okExp = body["expires"].(string)
		okCard := true
		if _, given := body["card"]; given {
			q.Card, okCard = accessCardID(body, "card")
		}
		host, okHost := accessString(body, "host", maxEgressHost)
		q.Host = host
		var okPort bool
		q.Port, okPort = accessPort(body)
		cred, okCred := accessString(body, "credential", maxCredentialRef)
		q.Credential = cred
		reason, okReason := accessString(body, "reason", maxAccessReason)
		q.Reason = reason
		okConfirmed := true
		if v, given := body["confirmed"]; given {
			q.Confirmed, okConfirmed = v.(bool)
		}
		named := 0
		for _, v := range []string{q.Card, q.Host, q.Credential} {
			if v != "" {
				named++
			}
		}
		if !keysWithin(body, "op", "sid", "card", "host", "port", "credential", "reason", "scope", "expires", "confirmed") || !okScope || !okExp || len(q.Scope) > 16 || len(q.Expires) > 16 ||
			!okCard || !okHost || !okPort || !okCred || !okReason || !okConfirmed || named != 1 || q.Port != 0 && q.Host == "" {
			refuseFields(w)
			return
		}
		said, err = s.cfg.Access.AllowAccess(ctx, sid, q)
	case "refuse":
		card, okCard := accessCardID(body, "card")
		if !keysWithin(body, "op", "sid", "card") || !okCard {
			refuseFields(w)
			return
		}
		said, err = s.cfg.Access.RefuseAccess(ctx, sid, card)
	case "apply":
		preset, okPreset := accessCardID(body, "preset")
		if !keysWithin(body, "op", "sid", "preset") || !okPreset {
			refuseFields(w)
			return
		}
		said, err = s.cfg.Access.ApplyPreset(ctx, sid, preset)
	case "revoke":
		grant, okGrant := accessCardID(body, "grant")
		if !keysWithin(body, "op", "sid", "grant") || !okGrant {
			refuseFields(w)
			return
		}
		said, err = s.cfg.Access.TakeBack(ctx, sid, grant)
	case "revoke-all":
		if !keysWithin(body, "op", "sid") {
			refuseFields(w)
			return
		}
		var changed bool
		changed, said, err = s.cfg.Access.TakeBackAll(ctx, sid)
		if !changed {
			tone = "warn"
		}
	case "action-run", "action-stop":
		var ok bool
		if said, ok = s.accessActions(ctx, w, op, sid, body); !ok {
			return
		}
	default:
		refuse(w, http.StatusBadRequest, "op", saidAccessOp)
		return
	}
	if op != "read" {
		shared.For(r.Context(), s.logger).Info("access", "op", op, "ok", err == nil)
	}
	if err != nil {
		code, p := status(err)
		send(w, code, p)
		return
	}
	v, err := s.cfg.Access.AccessView(sid)
	if err != nil {
		code, p := status(err)
		send(w, code, p)
		return
	}
	answer := accessViewJSON(v)
	if said != "" {
		answer["message"], answer["tone"] = said, tone
	}
	send(w, http.StatusOK, answer)
}

func (s *Server) accessRequest(w http.ResponseWriter, r *http.Request) {
	body, bad := readObject(w, r, MaxAgentBody)
	if bad != 0 {
		agentRefuse(w, bad, "body")
		return
	}
	host, okHost := accessString(body, "host", maxEgressHost)
	port, okPort := accessPort(body)
	cred, okCred := accessString(body, "credential", maxCredentialRef)
	reason, okReason := body["reason"].(string)
	if !keysWithin(body, "host", "port", "credential", "reason") || !okHost || !okPort || !okCred || !okReason || (host == "") == (cred == "") || cred != "" && port != 0 || len(reason) > maxAccessReason {
		agentRefuse(w, http.StatusBadRequest, "input")
		return
	}
	sid, ok := s.forgeSession(w, r)
	if !ok {
		return
	}
	if s.cfg.Access == nil {
		send(w, http.StatusOK, map[string]string{"result": session.AccessRefused, "reason": "this engine has no control channel to egress, so it gives no access; tell the person what you need and why"})
		return
	}
	answer := s.cfg.Access.Request(context.WithoutCancel(r.Context()), sid, session.AccessAsk{Host: host, Port: port, Credential: cred, Reason: reason})
	shared.For(r.Context(), s.logger).Info("access request", "side", "agent", "result", answer.Result)
	send(w, http.StatusOK, map[string]string{"result": answer.Result, "reason": answer.Reason})
}

type accessNoteJSON struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

func accessNotesOf(list []session.AccessNote) []accessNoteJSON {
	if len(list) == 0 {
		return nil
	}
	out := make([]accessNoteJSON, 0, len(list))
	for _, n := range list {
		out = append(out, accessNoteJSON{ID: n.ID, Text: n.Text})
	}
	return out
}
