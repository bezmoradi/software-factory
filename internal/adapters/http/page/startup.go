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
}

func signInOf(s *session.SignIn) *signInJSON {
	if s == nil || s.Method == "" {
		return nil
	}
	return &signInJSON{Method: string(s.Method), Words: s.Words(), Endpoint: s.Endpoint, Costs: s.Costs(), CheckedAt: millis(s.CheckedAt)}
}

type shownFileJSON struct {
	Path        string `json:"path"`
	Place       string `json:"place"`
	Link        bool   `json:"link"`
	Target      string `json:"target,omitempty"`
	Size        int    `json:"size"`
	Hash        string `json:"hash"`
	Content     string `json:"content"`
	Clipped     bool   `json:"clipped"`
	Uncommitted bool   `json:"uncommitted"`
}

type approvalJSON struct {
	Agent  string          `json:"agent"`
	Repo   string          `json:"repo"`
	Commit string          `json:"commit"`
	Hash   string          `json:"hash"`
	Files  []shownFileJSON `json:"files"`
}

type needsJSON struct {
	Approval *approvalJSON `json:"approval,omitempty"`
	SignIn   *signInJSON   `json:"signIn,omitempty"`
	Expected string        `json:"expected,omitempty"`
	Step     string        `json:"step,omitempty"`
	Cost     bool          `json:"cost,omitempty"`
	Trust    string        `json:"trust,omitempty"`
}

func needsOf(n *session.Needs) *needsJSON {
	if n == nil {
		return nil
	}
	out := &needsJSON{SignIn: signInOf(n.SignIn), Step: n.Step, Cost: n.Cost, Trust: n.Trust}
	if n.Expected != "" {
		out.Expected = string(n.Expected)
	}
	if a := n.Approval; a != nil {
		v := &approvalJSON{Agent: a.Agent, Repo: a.Repo, Commit: a.Commit, Hash: a.Hash, Files: make([]shownFileJSON, 0, len(a.Files))}
		for _, f := range a.Files {
			v.Files = append(v.Files, shownFileJSON{Path: f.Path, Place: string(f.Place), Link: f.Link, Target: f.Target, Size: f.Size, Hash: f.Hash,
				Content: f.Content, Clipped: f.Clipped, Uncommitted: f.Uncommitted})
		}
		out.Approval = v
	}
	return out
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
	return f
}
