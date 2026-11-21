package page

import (
	"context"
	"net/http"
	"regexp"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	pairRoute = "/api/pair"
	pairFor   = 60 * time.Second
)

var pairParam = regexp.MustCompile(`^[0-9a-f]{16}$`)

type mergedPullJSON struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body,omitempty"`
	Author string `json:"author"`
	URL    string `json:"url"`
	Base   string `json:"base"`
	Head   string `json:"head"`
	Merge  string `json:"merge"`
	Merged int64  `json:"merged"`
}

type pairMemberJSON struct {
	Agent  string `json:"agent"`
	Model  string `json:"model"`
	Effort string `json:"effort"`
	SID    string `json:"sid"`
}

type benchJSON struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	Base   string `json:"base"`
	Head   string `json:"head"`
	Merge  string `json:"merge"`
}

type pairJSON struct {
	ID      string           `json:"id"`
	Repo    string           `json:"repo"`
	Commit  string           `json:"commit"`
	Origin  string           `json:"origin"`
	Name    string           `json:"name"`
	Type    string           `json:"type"`
	Message string           `json:"message"`
	Members []pairMemberJSON `json:"members"`
	Kept    string           `json:"kept"`
	Bench   *benchJSON       `json:"benchmark"`
	At      int64            `json:"at"`
	History bool             `json:"historyOnly"`
}

func mergedPullOf(m session.MergedPull) mergedPullJSON {
	return mergedPullJSON{Number: m.Number, Title: m.Title, Body: m.Body, Author: m.Author, URL: m.URL, Base: m.Base, Head: m.Head, Merge: m.Merge, Merged: millis(m.Merged)}
}

func pairOf(p session.Pair) pairJSON {
	out := pairJSON{ID: p.ID, Repo: p.Repo, Commit: p.Commit, Origin: p.Origin, Name: p.Name, Type: p.Type, Message: p.Message, Members: make([]pairMemberJSON, 0, len(p.Members)), Kept: p.Kept, At: millis(p.At), History: p.History}
	for _, m := range p.Members {
		out.Members = append(out.Members, pairMemberJSON{Agent: m.Agent, Model: m.Model, Effort: m.Effort, SID: m.SID})
	}
	if b := p.Bench; b != nil {
		out.Bench = &benchJSON{Number: b.Number, Title: b.Title, URL: b.URL, Base: b.Base, Head: b.Head, Merge: b.Merge}
	}
	return out
}

func (s *Server) UsePairs(pairs session.PairsPage) {
	s.mu.Lock()
	s.pairs = pairs
	s.mu.Unlock()
}

func pairAgentOf(v any) (session.PairAgent, bool) {
	list, ok := v.([]any)
	if !ok || len(list) != 1 {
		return session.PairAgent{}, false
	}
	m, isObject := list[0].(map[string]any)
	agent, okAgent := m["agent"].(string)
	if !isObject || !keysWithin(m, "agent", "model", "effort") || !okAgent || !agentID.MatchString(agent) {
		return session.PairAgent{}, false
	}
	a := session.PairAgent{Agent: agent}
	for k, dst := range map[string]*string{"model": &a.Model, "effort": &a.Effort} {
		if raw, has := m[k]; has {
			t, isText := raw.(string)
			if !isText || len(t) > 40 {
				return session.PairAgent{}, false
			}
			*dst = t
		}
	}
	return a, true
}

func benchOf(v any, present bool) (int, string, string, bool) {
	if !present {
		return 0, "", "", true
	}
	m, ok := v.(map[string]any)
	n, okNumber := wholeNumber(m["number"], 1, session.MaxIssueNumber)
	hash, okHash := m["hash"].(string)
	base, okBase := m["base"].(string)
	if !ok || !keysWithin(m, "number", "hash", "base") || !okNumber || !okHash || len(hash) > 64 || !okBase || len(base) > 40 {
		return 0, "", "", false
	}
	return n, hash, base, true
}

func pairPath(body map[string]any) (string, bool) {
	path, ok := body["path"].(string)
	return path, ok && path != "" && len(path) <= session.MaxCwd
}

func pairID(body map[string]any) (string, bool) {
	id, ok := body["pair"].(string)
	return id, ok && pairParam.MatchString(id)
}

func (s *Server) pairAPI(w http.ResponseWriter, r *http.Request, body map[string]any) {
	s.mu.Lock()
	pairs := s.pairs
	s.mu.Unlock()
	if pairs == nil {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine has no agent-host, so it starts no paired sessions."})
		return
	}
	op, okOp := body["op"].(string)
	if !okOp {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	limit := pairFor
	switch op {
	case "start":
		limit = 2*dispatchFor + manageFor
	case "next":
		limit = dispatchFor + manageFor
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(limit + writeFor))
	ctx, cancel := context.WithTimeout(r.Context(), limit)
	defer cancel()
	switch op {
	case "merged-list", "merged-read":
		s.mergedAPI(ctx, w, pairs, op, body)
	case "start":
		s.pairStart(ctx, w, pairs, body)
	case "next":
		id, okID := pairID(body)
		consent, consented := consentOf(body)
		box, okBox := boxOf(body)
		if !keysWithin(body, "op", "pair", "approve", "costOk", "box") || !okID || !consented || !okBox {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		started, err := pairs.Next(ctx, id, consent, box)
		shared.For(r.Context(), s.logger).Info("pair next", "ok", err == nil)
		if err != nil {
			code, p := status(err)
			send(w, code, p)
			return
		}
		send(w, http.StatusAccepted, startAnswer(started))
	case "view", "merged-diff":
		id, okID := pairID(body)
		if !keysWithin(body, "op", "pair") || !okID {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		if op == "view" {
			p, err := pairs.View(id)
			if err != nil {
				code, pr := status(err)
				send(w, code, pr)
				return
			}
			send(w, http.StatusOK, pairOf(p))
			return
		}
		v, err := pairs.MergedDiff(ctx, id)
		if err != nil {
			code, pr := status(err)
			send(w, code, pr)
			return
		}
		send(w, http.StatusOK, map[string]any{"changes": changesOf(v, nil)})
	case "keep":
		id, okID := pairID(body)
		sid, okSID := body["sid"].(string)
		if !keysWithin(body, "op", "pair", "sid") || !okID || !okSID || len(sid) > 80 {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		err := pairs.Keep(id, sid)
		shared.For(r.Context(), s.logger).Info("pair keep", "ok", err == nil)
		reply(w, err, http.StatusOK)
	default:
		send(w, http.StatusBadRequest, problem{Error: "op", Message: "The engine lists and reads merged pull requests, starts paired sessions, and shows or keeps one of a pair, nothing else."})
	}
}

func (s *Server) mergedAPI(ctx context.Context, w http.ResponseWriter, pairs session.PairsPage, op string, body map[string]any) {
	path, okPath := pairPath(body)
	if op == "merged-list" {
		page, okPage := wholeNumber(body["page"], 1, session.MaxMergedPage)
		if !keysWithin(body, "op", "path", "page") || !okPath || !okPage {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		list, err := pairs.MergedPulls(ctx, path, page)
		if err != nil {
			code, p := status(err)
			send(w, code, p)
			return
		}
		out := make([]mergedPullJSON, 0, len(list.Pulls))
		for _, m := range list.Pulls {
			out = append(out, mergedPullOf(m))
		}
		send(w, http.StatusOK, map[string]any{"repo": list.Repo, "page": list.Page, "more": list.More, "pulls": out})
		return
	}
	number, okNumber := wholeNumber(body["number"], 1, session.MaxIssueNumber)
	if !keysWithin(body, "op", "path", "number") || !okPath || !okNumber {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	v, err := pairs.ReadMerged(ctx, path, number)
	if err != nil {
		code, p := status(err)
		send(w, code, p)
		return
	}
	send(w, http.StatusOK, map[string]any{"repo": v.Repo, "pull": mergedPullOf(v.Pull), "refused": v.Refused, "clipped": v.Clipped, "hash": v.Hash})
}

func (s *Server) pairStart(ctx context.Context, w http.ResponseWriter, pairs session.PairsPage, body map[string]any) {
	path, okPath := pairPath(body)
	agent, okAgent := pairAgentOf(body["agents"])
	rawBench, hasBench := body["benchmark"]
	bench, hash, base, okBench := benchOf(rawBench, hasBench)
	rawFrom, hasFrom := body["from"]
	from, okFrom := startFromOf(rawFrom, hasFrom)
	consent, consented := consentOf(body)
	double, hasDouble := body["double"]
	isDouble, okDouble := double.(bool)
	box, okBox := boxOf(body)
	if !keysWithin(body, "op", "path", "name", "type", "message", "agents", "secondModel", "double", "benchmark", "from", "issue", "issueHash", "caps", "approve", "costOk", "box") ||
		!okPath || !okAgent || !okBench || !okFrom || !consented || hasDouble && !okDouble || !okBox {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	for _, k := range []string{"name", "type", "message", "secondModel"} {
		if v, ok := body[k]; ok {
			if _, isText := v.(string); !isText {
				refuse(w, http.StatusBadRequest, k)
				return
			}
		}
	}
	issue, issueHash, okIssue := startIssueOf(w, body)
	if !okIssue {
		return
	}
	caps, okCaps := startCapsOf(w, body)
	if !okCaps {
		return
	}
	started, err := pairs.Start(ctx, session.PairStart{Path: path, Name: str(body["name"], 400), Type: str(body["type"], 40), Message: str(body["message"], 4*session.MaxText+1),
		Agent: agent, SecondModel: str(body["secondModel"], 41), Double: isDouble, Bench: bench, BenchHash: hash, BenchBase: base, From: from, Issue: issue, IssueHash: issueHash,
		Caps: caps, Consent: consent, Box: box})
	s.logger.Info("pair start", "double", isDouble, "benchmark", bench > 0, "issue", issue > 0, "ok", err == nil, "second", started.Second.SID != "")
	if err != nil {
		code, p := status(err)
		send(w, code, p)
		return
	}
	out := startAnswer(started.Started)
	out["pair"], out["next"] = started.Pair, started.Next
	if started.Second.SID != "" {
		out["second"] = map[string]string{"id": started.Second.ID, "sid": started.Second.SID}
	}
	if started.SecondErr != nil {
		_, p := status(started.SecondErr)
		out["secondRefused"] = p
	}
	if started.CapsRefused == "" && started.Second.CapsRefused != "" {
		out["capsRefused"] = started.Second.CapsRefused
	}
	send(w, http.StatusAccepted, out)
}
