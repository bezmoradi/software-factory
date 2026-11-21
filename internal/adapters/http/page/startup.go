package page

import (
	"net/http"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const expectRoute = "/api/sign-in-expect"

type signInJSON struct {
	Method    string `json:"method"`
	Words     string `json:"words"`
	Endpoint  string `json:"endpoint,omitempty"`
	Costs     bool   `json:"costs"`
	CheckedAt int64  `json:"checkedAt,omitempty"`
	Busy      bool   `json:"busy,omitempty"`
	Stale     bool   `json:"stale,omitempty"`
}

func signInOf(s *session.SignIn) *signInJSON {
	if s == nil || s.Method == "" {
		return nil
	}
	return &signInJSON{Method: string(s.Method), Words: s.Words(), Endpoint: s.Endpoint, Costs: s.Costs(), CheckedAt: millis(s.CheckedAt), Busy: s.Busy, Stale: s.Stale}
}

type shownFileJSON struct {
	Path        string     `json:"path"`
	Place       string     `json:"place"`
	Link        bool       `json:"link"`
	Target      string     `json:"target,omitempty"`
	Size        int        `json:"size"`
	Hash        string     `json:"hash"`
	Content     string     `json:"content"`
	Clipped     bool       `json:"clipped"`
	Uncommitted bool       `json:"uncommitted"`
	Hidden      bool       `json:"hidden,omitempty"`
	Marks       []markJSON `json:"marks,omitempty"`
}

type markJSON struct {
	Line int    `json:"line"`
	What string `json:"what"`
	Text string `json:"text"`
}

type approvalJSON struct {
	Agent        string          `json:"agent"`
	Repo         string          `json:"repo"`
	Commit       string          `json:"commit"`
	Hash         string          `json:"hash"`
	Files        []shownFileJSON `json:"files"`
	Instructions []shownFileJSON `json:"instructions,omitempty"`
	Unchanged    int             `json:"unchanged,omitempty"`
	Walk         []string        `json:"walk,omitempty"`
	Notes        []string        `json:"notes,omitempty"`
	Skipped      []string        `json:"skipped,omitempty"`
}

func shownFilesOf(files []session.ShownFile) []shownFileJSON {
	out := make([]shownFileJSON, 0, len(files))
	for _, f := range files {
		j := shownFileJSON{Path: f.Path, Place: string(f.Place), Link: f.Link, Target: f.Target, Size: f.Size, Hash: f.Hash,
			Content: f.Content, Clipped: f.Clipped, Uncommitted: f.Uncommitted, Hidden: f.Hidden}
		for _, m := range f.Marks {
			j.Marks = append(j.Marks, markJSON{Line: m.Line, What: m.What, Text: m.Text})
		}
		out = append(out, j)
	}
	return out
}

type homeApprovalJSON struct {
	Box     string   `json:"box"`
	Digest  string   `json:"digest"`
	Changed []string `json:"changed"`
	More    int      `json:"more"`
	First   bool     `json:"first"`
	Problem string   `json:"problem,omitempty"`
}

type needsJSON struct {
	Approval *approvalJSON     `json:"approval,omitempty"`
	Home     *homeApprovalJSON `json:"home,omitempty"`
	SignIn   *signInJSON       `json:"signIn,omitempty"`
	Expected string            `json:"expected,omitempty"`
	Step     string            `json:"step,omitempty"`
	Cost     bool              `json:"cost,omitempty"`
	Trust    string            `json:"trust,omitempty"`
	Box      string            `json:"box,omitempty"`
	Agent    string            `json:"agent,omitempty"`
	Code     bool              `json:"code,omitempty"`
	Next     string            `json:"next,omitempty"`
	Tries    int               `json:"tries,omitempty"`
}

func needsOf(n *session.Needs) *needsJSON {
	if n == nil {
		return nil
	}
	out := &needsJSON{SignIn: signInOf(n.SignIn), Step: n.Step, Cost: n.Cost, Trust: n.Trust, Box: n.Box, Agent: n.Agent, Code: n.Code, Next: n.NextStep(), Tries: n.Tries}
	if n.Expected != "" {
		out.Expected = string(n.Expected)
	}
	if h := n.Home; h != nil {
		changed := h.Changed
		if changed == nil {
			changed = []string{}
		}
		out.Home = &homeApprovalJSON{Box: h.Box, Digest: h.Digest, Changed: changed, More: h.More, First: h.First, Problem: h.Problem}
	}
	if a := n.Approval; a != nil {
		out.Approval = &approvalJSON{Agent: a.Agent, Repo: a.Repo, Commit: a.Commit, Hash: a.Hash, Files: shownFilesOf(a.Files),
			Unchanged: a.Unchanged, Walk: a.Walk, Notes: a.Notes, Skipped: a.Skipped}
		if len(a.Instructions) > 0 {
			out.Approval.Instructions = shownFilesOf(a.Instructions)
		}
	}
	return out
}

func boxOf(body map[string]any) (string, bool) {
	v, has := body["box"]
	if !has {
		return "", true
	}
	box, ok := v.(string)
	return box, ok && session.ValidBox(box)
}

func consentOf(body map[string]any) (session.Consent, bool) {
	var c session.Consent
	if v, ok := body["approve"]; ok {
		s, isText := v.(string)
		if !isText || s == "" || !session.ValidApproval(s) {
			return c, false
		}
		c.Approve = s
	}
	if v, ok := body["home"]; ok {
		s, isText := v.(string)
		if !isText || !session.ValidHomeDigest(s) {
			return c, false
		}
		c.Home = s
	}
	if v, ok := body["costOk"]; ok {
		b, isBool := v.(bool)
		if !isBool {
			return c, false
		}
		c.CostOK = b
	}
	return c, true
}

func (s *Server) expect(w http.ResponseWriter, body map[string]any) {
	if s.cfg.Starter == nil {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine has no agent-host, so it starts no agents."})
		return
	}
	agent, okAgent := body["agent"].(string)
	x, okExpect := body["expect"].(string)
	if !keysWithin(body, "agent", "expect") || !okAgent || !okExpect || agent == "" || len(agent) > 40 || len(x) > 20 {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	err := s.cfg.Starter.Expect(agent, session.Expect(x))
	s.logger.Info("sign-in expect", "ok", err == nil)
	reply(w, err, http.StatusOK)
}

type reviewerJSON struct {
	SignIn *signInJSON `json:"signIn,omitempty"`
	Expect string      `json:"expect"`
}

func (s *Server) reviewer(expect map[string]session.Expect) *reviewerJSON {
	if s.cfg.Reviews == nil {
		return nil
	}
	out := &reviewerJSON{Expect: string(session.ExpectAny)}
	if x, ok := expect[session.ReviewerAgent]; ok {
		out.Expect = string(x)
	}
	if s.cfg.ReviewerSignIn != nil {
		if si, ok := s.cfg.ReviewerSignIn(); ok {
			out.SignIn = signInOf(&si)
		}
	}
	return out
}

func ReviewerFrame(si session.SignIn) session.Fields {
	f := session.Fields{"type": "reviewer", "method": string(si.Method), "words": si.Words(), "costs": si.Costs(), "checkedAt": millis(si.CheckedAt)}
	if si.Endpoint != "" {
		f["endpoint"] = si.Endpoint
	}
	if si.Stale {
		f["stale"] = true
	}
	return f
}
