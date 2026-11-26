package page

import (
	"context"
	"net/http"
	"strings"
	"unicode"

	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	credentialsRoute    = "/api/credentials"
	maxCredentialRef    = 64
	maxCredentialField  = 600
	saidCredentialsOp   = "The engine reads, saves and deletes credentials, presets and actions, and checks a preset's risk, nothing else."
	saidCredentialsNone = "This engine has no control channel to egress, so it keeps no credentials for access grants."
)

func (s *Server) UseCredentials(c session.CredentialsPage) {
	s.cfg.Credentials = c
}

func presetsJSON(list []session.PresetView) []map[string]any {
	out := make([]map[string]any, 0, len(list))
	for _, p := range list {
		kind := "host"
		if p.Credential != "" {
			kind = "credential"
		}
		if p.Port > 0 {
			kind = "port"
		}
		out = append(out, map[string]any{
			"id": p.ID, "name": p.Name, "kind": kind, "what": p.What, "scope": p.Scope, "scopeLabel": p.ScopeLabel, "expires": p.Expires, "expiresLabel": p.ExpiresLabel,
			"project": p.Project, "auto": p.Auto, "level": p.Level, "label": p.Label,
		})
	}
	return out
}

func credentialsViewJSON(v session.CredentialsView) map[string]any {
	list := make([]map[string]any, 0, len(v.Credentials))
	for _, c := range v.Credentials {
		grants := make([]map[string]any, 0, len(c.Grants))
		for _, g := range c.Grants {
			entry := map[string]any{"id": g.ID, "sid": g.Session, "project": g.Project, "scope": g.Scope, "scopeLabel": g.ScopeLabel, "until": g.Until, "expires": unixOr(g.Expires), "who": g.Who, "lead": g.Lead}
			if g.Copy != "" {
				entry["copy"] = g.Copy
			}
			grants = append(grants, entry)
		}
		list = append(list, map[string]any{"id": c.ID, "name": c.Name, "type": c.Type, "typeLabel": c.Label, "bound": c.Bound, "tags": c.Tags, "saved": unixOr(c.Saved), "grants": grants})
	}
	return map[string]any{
		"credentials": list, "presets": presetsJSON(v.Presets), "note": v.Note, "projects": nonNil(v.Projects),
		"types": choicesJSON(session.CredentialTypes), "tags": choicesJSON(session.CredentialTags), "modes": choicesJSON(session.CredentialModes),
		"scopes": choicesJSON(session.GrantScopes), "expiries": choicesJSON(session.GrantExpiries),
		"actionsOn": v.ActionsOn, "actions": actionViewsJSON(v.Actions),
		"limits": map[string]int{"credentials": session.MaxCredentials, "bound": session.MaxCredentialBound, "vars": session.MaxCredentialVars, "file": session.MaxCredentialFile, "presets": session.MaxPresets,
			"actions": session.MaxActions, "minutes": session.ActionMinutes, "mostMinutes": session.MaxActionMinutes},
	}
}

func boundOf(v any) ([]string, bool) {
	t, ok := v.(string)
	if !ok || len(t) > 4*session.MaxCredentialBound*maxEgressHost {
		return stringList(v, 4*session.MaxCredentialBound, maxEgressHost)
	}
	return strings.FieldsFunc(t, func(r rune) bool { return r == ',' || unicode.IsSpace(r) }), true
}

func varsOf(v any) ([]session.CredentialVar, bool) {
	t, ok := v.(string)
	if !ok || len(t) > 2*session.MaxCredentialVars*(session.MaxCredentialValue+maxCredentialRef) {
		return nil, false
	}
	var out []session.CredentialVar
	for line := range strings.SplitSeq(strings.ReplaceAll(t, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		name, value, _ := strings.Cut(line, "=")
		out = append(out, session.CredentialVar{Name: name, Value: value})
	}
	return out, true
}

func stringList(v any, most, limit int) ([]string, bool) {
	raw, ok := v.([]any)
	if !ok || len(raw) > most {
		return nil, false
	}
	out := make([]string, 0, len(raw))
	for _, x := range raw {
		t, ok := x.(string)
		if !ok || len(t) > limit {
			return nil, false
		}
		out = append(out, t)
	}
	return out, true
}

var credentialFields = []string{"id", "name", "type", "bound", "tags", "variable", "value", "keyId", "secretKey", "role", "region", "user", "privateKey", "password", "hostKey", "path", "mode", "contents", "vars"}

func credentialOf(raw any) (session.Credential, bool) {
	body, ok := raw.(map[string]any)
	if !ok || !keysWithin(body, credentialFields...) {
		return session.Credential{}, false
	}
	text := func(key string, limit int) string {
		v, good := accessString(body, key, limit)
		ok = ok && good
		return v
	}
	c := session.Credential{ID: text("id", maxCredentialRef), Name: text("name", maxCredentialRef), Type: text("type", 16)}
	c.Secret = session.CredentialSecret{
		Variable: text("variable", maxCredentialRef), Value: text("value", session.MaxCredentialValue+maxCredentialField), KeyID: text("keyId", maxCredentialField),
		SecretKey: text("secretKey", maxCredentialField), Role: text("role", maxCredentialField), Region: text("region", maxCredentialRef), User: text("user", maxCredentialRef),
		PrivateKey: text("privateKey", session.MaxCredentialKey+maxCredentialField), Password: text("password", session.MaxCredentialPass+maxCredentialField),
		HostKey: text("hostKey", 8192), Path: text("path", session.MaxCredentialPath+maxCredentialField), Mode: text("mode", 8),
		Contents: text("contents", session.MaxCredentialFile+maxCredentialField),
	}
	if !ok {
		return session.Credential{}, false
	}
	if c.ID != "" && !pushCard.MatchString(c.ID) {
		return session.Credential{}, false
	}
	if v, given := body["bound"]; given {
		if c.Bound, ok = boundOf(v); !ok {
			return session.Credential{}, false
		}
	}
	if v, given := body["tags"]; given {
		if c.Tags, ok = stringList(v, 8, 16); !ok {
			return session.Credential{}, false
		}
	}
	if v, given := body["vars"]; given {
		if c.Secret.Vars, ok = varsOf(v); !ok {
			return session.Credential{}, false
		}
	}
	return c, true
}

func presetDraftOf(raw any) (session.PresetDraft, bool) {
	body, ok := raw.(map[string]any)
	if !ok || !keysWithin(body, "name", "host", "port", "credential", "scope", "expires", "project", "sid", "auto", "confirmed") {
		return session.PresetDraft{}, false
	}
	text := func(key string, limit int) string {
		v, good := accessString(body, key, limit)
		ok = ok && good
		return v
	}
	d := session.PresetDraft{Name: text("name", 4*session.MaxPresetName), Host: text("host", maxEgressHost), Credential: text("credential", maxCredentialRef),
		Scope: text("scope", 16), Expires: text("expires", 16), Project: text("project", 256), Session: text("sid", 80)}
	port, okPort := accessPort(body)
	d.Port, ok = port, ok && okPort
	for _, key := range []string{"auto", "confirmed"} {
		v, given := body[key]
		if !given {
			continue
		}
		b, isBool := v.(bool)
		ok = ok && isBool
		if key == "auto" {
			d.Auto = b
		} else {
			d.Confirmed = b
		}
	}
	return d, ok
}

func (s *Server) credentials(w http.ResponseWriter, r *http.Request, body map[string]any) {
	if s.cfg.Credentials == nil {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: saidCredentialsNone})
		return
	}
	op, okOp := body["op"].(string)
	if !okOp {
		refuse(w, http.StatusBadRequest, "op", saidCredentialsOp)
		return
	}
	ctx := context.WithoutCancel(r.Context())
	var err error
	said := ""
	switch op {
	case "read":
		if !keysWithin(body, "op") {
			refuseFields(w)
			return
		}
	case "save":
		c, ok := credentialOf(body["credential"])
		if !keysWithin(body, "op", "credential") || !ok {
			refuseFields(w)
			return
		}
		said, err = s.cfg.Credentials.SaveCredential(ctx, c)
	case "delete", "preset-delete", "action-delete":
		id, ok := accessCardID(body, "id")
		if !keysWithin(body, "op", "id") || !ok {
			refuseFields(w)
			return
		}
		switch op {
		case "delete":
			said, err = s.cfg.Credentials.DeleteCredential(ctx, id)
		case "action-delete":
			said, err = s.cfg.Credentials.DeleteAction(ctx, id)
		default:
			said, err = s.cfg.Credentials.DeletePreset(ctx, id)
		}
	case "action-save":
		d, ok := actionDraftOf(body["action"])
		if !keysWithin(body, "op", "action") || !ok {
			refuseFields(w)
			return
		}
		said, err = s.cfg.Credentials.SaveAction(ctx, d)
	case "preset-save", "preset-check":
		d, ok := presetDraftOf(body["preset"])
		if !keysWithin(body, "op", "preset") || !ok {
			refuseFields(w)
			return
		}
		if op == "preset-check" {
			risk, err := s.cfg.Credentials.PresetRisk(d)
			if err != nil {
				code, p := status(err)
				send(w, code, p)
				return
			}
			says := risk.Says
			if says == nil {
				says = []string{}
			}
			send(w, http.StatusOK, map[string]any{"risk": accessRiskJSON{Scope: risk.Scope, Expires: risk.Expires, Level: risk.Level, Label: risk.Label, Says: says, Accept: risk.Accept}})
			return
		}
		said, err = s.cfg.Credentials.SavePreset(ctx, d)
	default:
		refuse(w, http.StatusBadRequest, "op", saidCredentialsOp)
		return
	}
	if op != "read" {
		shared.For(r.Context(), s.logger).Info("credentials", "op", op, "ok", err == nil)
	}
	if err != nil {
		code, p := status(err)
		send(w, code, p)
		return
	}
	v, err := s.cfg.Credentials.CredentialsView()
	if err != nil {
		code, p := status(err)
		send(w, code, p)
		return
	}
	answer := credentialsViewJSON(v)
	if said != "" {
		answer["message"], answer["tone"] = said, "ok"
	}
	send(w, http.StatusOK, answer)
}
