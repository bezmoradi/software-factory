package page

import (
	"context"
	"net/http"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	egressRoute   = "/api/egress"
	maxEgressHost = 300
	egressFor     = 20 * time.Second
)

func (s *Server) UseEgress(e session.EgressPage) {
	s.cfg.Egress = e
}

func unixOr(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func egressListJSON(l session.EgressList) map[string]any {
	expires := map[string]int64{}
	for h, t := range l.Expires {
		expires[h] = t.Unix()
	}
	allow := l.Allow
	if allow == nil {
		allow = []string{}
	}
	return map[string]any{"allow": allow, "expires": expires}
}

func egressViewJSON(v session.EgressView) map[string]any {
	added := make([]map[string]any, 0, len(v.Added))
	for _, h := range v.Added {
		added = append(added, map[string]any{"host": h.Name, "expires": unixOr(h.Expires), "addedAt": unixOr(h.AddedAt), "uploads": session.UploadHost(h.Name), "reason": h.Reason})
	}
	var gateway any
	if v.Gateway != nil {
		gateway = map[string]any{"url": v.Gateway.URL, "host": v.Gateway.Host, "agents": v.Gateway.Agents, "reviewers": v.Gateway.Reviewers}
	}
	return map[string]any{
		"saved": v.Saved, "pending": v.Pending, "added": added, "gateway": gateway,
		"lists": map[string]any{"agents": egressListJSON(v.Lists.Agents), "reviewers": egressListJSON(v.Lists.Reviewers)},
		"base":  v.Base, "deny": v.Deny, "now": v.Now.Unix(),
		"gatewayKeyNote": session.GatewayKeyNote, "pypiNote": session.PyPIReadHostsNote, "expiries": expiriesJSON(),
	}
}

func expiriesJSON() []map[string]string {
	out := make([]map[string]string, 0, len(session.ExpiryChoices))
	for _, c := range session.ExpiryChoices {
		out = append(out, map[string]string{"value": c.Value, "label": c.Label})
	}
	return out
}

func refusalsJSON(list []session.RefusalView, now time.Time) map[string]any {
	sides := map[string][]map[string]any{session.SideAgents: {}, session.SideReviewers: {}}
	for _, r := range list {
		suggest := r.Suggest
		if suggest == nil {
			suggest = []string{}
		}
		sides[r.Side] = append(sides[r.Side], map[string]any{
			"host": r.Host, "reason": r.Reason, "reasonLabel": session.EgressReasonLabel(r.Reason), "count": r.Count, "last": r.Last.Unix(),
			"addable": r.Addable, "gitHost": r.GitHost, "uploads": r.Uploads, "probe": r.Probe, "suggest": suggest, "note": r.Note(),
		})
	}
	return map[string]any{"refusals": sides, "now": now.Unix()}
}

func (s *Server) egress(w http.ResponseWriter, r *http.Request, body map[string]any) {
	if s.cfg.Egress == nil {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine has no control channel to egress (SF_EGRESS_KEY_FILE in the engine, EGRESS_CONTROL_KEY_FILE in egress), or its sandbox check is off, so the allowlist is set only by SF_ALLOW and SF_EXTRA_ALLOW."})
		return
	}
	op, okOp := body["op"].(string)
	if !okOp {
		refuse(w, http.StatusBadRequest, "op")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), egressFor)
	defer cancel()
	var err error
	switch op {
	case "read":
		if !keysWithin(body, "op") {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
	case "refusals":
		if !keysWithin(body, "op") {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		list, err := s.cfg.Egress.EgressRefusals(ctx)
		if err != nil {
			code, p := status(err)
			send(w, code, p)
			return
		}
		send(w, http.StatusOK, refusalsJSON(list, s.cfg.Now()))
		return
	case "add":
		host, okHost := body["host"].(string)
		expires, okExp := body["expires"].(string)
		uploadsOk := false
		if v, present := body["uploadsOk"]; present {
			b, ok := v.(bool)
			if !ok {
				refuse(w, http.StatusBadRequest, "fields")
				return
			}
			uploadsOk = b
		}
		reason := ""
		if v, present := body["reason"]; present {
			t, ok := v.(string)
			if !ok {
				refuse(w, http.StatusBadRequest, "fields")
				return
			}
			reason = t
		}
		if !keysWithin(body, "op", "host", "expires", "uploadsOk", "reason") || !okHost || !okExp || len(expires) > 8 {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		err = s.cfg.Egress.AddHost(ctx, host, expires, uploadsOk, reason)
	case "remove":
		host, okHost := body["host"].(string)
		if !keysWithin(body, "op", "host") || !okHost || len(host) > maxEgressHost {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		err = s.cfg.Egress.RemoveHost(ctx, host)
	case "gateway":
		u, okURL := body["url"].(string)
		agents, okA := body["agents"].(bool)
		reviewers, okR := body["reviewers"].(bool)
		if !keysWithin(body, "op", "url", "agents", "reviewers") || !okURL || !okA || !okR {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(egressFor + writeFor))
		err = s.cfg.Egress.SetGateway(ctx, u, agents, reviewers)
	default:
		refuse(w, http.StatusBadRequest, "op")
		return
	}
	if op != "read" {
		shared.For(r.Context(), s.logger).Info("egress allowlist", "op", op, "ok", err == nil)
	}
	if err != nil {
		code, p := status(err)
		send(w, code, p)
		return
	}
	v, err := s.cfg.Egress.EgressView()
	if err != nil {
		code, p := status(err)
		send(w, code, p)
		return
	}
	answer := egressViewJSON(v)
	if host, ok := body["host"].(string); ok && op == "add" {
		answer["note"] = session.HostNote(host)
	}
	send(w, http.StatusOK, answer)
}
