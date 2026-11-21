package page

import (
	"net/http"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const decisionLogRoute = "/api/decision-log"

const (
	saidDecisionSaved     = "Saved. It stays through every rebuild of the log."
	saidDecisionUnchanged = "No change: the entry already says this."
)

type decisionEntryJSON struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	At       int64  `json:"at"`
	Line     string `json:"line"`
	Reason   string `json:"reason,omitempty"`
	Withheld bool   `json:"withheld,omitempty"`
	Name     string `json:"name,omitempty"`
	Text     string `json:"text,omitempty"`
	Hidden   bool   `json:"hidden,omitempty"`
	Added    bool   `json:"added,omitempty"`
	Edited   bool   `json:"edited,omitempty"`
	Label    string `json:"label"`
}

type decisionLogJSON struct {
	savedSaid
	Repo       string              `json:"repo"`
	Entries    []decisionEntryJSON `json:"entries"`
	More       int                 `json:"more"`
	Note       string              `json:"note"`
	Left       int                 `json:"left"`
	AuditSince int64               `json:"auditSince"`
	Orphans    int                 `json:"orphans"`
	Problems   []string            `json:"problems"`
}

func decisionLogOf(v session.DecisionLogView) decisionLogJSON {
	out := decisionLogJSON{Repo: v.Repo, Entries: make([]decisionEntryJSON, 0, len(v.Entries)), More: v.More, Note: v.Note, Left: v.Left, AuditSince: millis(v.AuditSince), Orphans: v.Orphans, Problems: nonNil(v.Problems)}
	for _, e := range v.Entries {
		out.Entries = append(out.Entries, decisionEntryJSON{ID: e.ID, Kind: e.Kind, At: millis(e.Time), Line: e.Line, Reason: e.Reason, Withheld: e.Withheld, Name: e.Name, Text: e.Text, Hidden: e.Hidden, Added: e.Added, Edited: e.Edited, Label: session.DecisionLabel(e.Kind)})
	}
	return out
}

func (s *Server) UseDecisionLog(l session.DecisionLogPage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.decisions = l
}

func (s *Server) decisionLog(w http.ResponseWriter, body map[string]any) {
	s.mu.Lock()
	log := s.decisions
	s.mu.Unlock()
	if log == nil {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine has no repositories, so it keeps no decision log."})
		return
	}
	op, okOp := body["op"].(string)
	repo, okRepo := body["repo"].(string)
	if !okOp || !okRepo || len(repo) > maxRulesRepo || !rulesRepoName.MatchString(repo) {
		refuseFields(w)
		return
	}
	id, _ := body["id"].(string)
	okID := session.ValidDecisionID(id)
	name, okName := body["name"].(string)
	text, okText := body["text"].(string)
	var v session.DecisionLogView
	var err error
	var said savedSaid
	switch op {
	case "read":
		if !keysWithin(body, "op", "repo") {
			refuseFields(w)
			return
		}
		v, err = log.DecisionLog(repo)
	case "set":
		hidden, okHidden := body["hidden"].(bool)
		if !keysWithin(body, "op", "repo", "id", "name", "text", "hidden") || !okID || !okName || !okText || !okHidden {
			refuseFields(w)
			return
		}
		var changed bool
		v, changed, err = log.SetDecision(repo, id, name, text, hidden)
		s.logger.Info("decision log", "op", "set", "ok", err == nil, "changed", changed)
		said = saveSaid(changed, saidDecisionSaved, saidDecisionUnchanged)
	case "add":
		if !keysWithin(body, "op", "repo", "text") || !okText {
			refuseFields(w)
			return
		}
		v, err = log.AddDecision(repo, text)
		s.logger.Info("decision log", "op", "add", "ok", err == nil)
	case "remove":
		if !keysWithin(body, "op", "repo", "id") || !okID {
			refuseFields(w)
			return
		}
		v, err = log.RemoveDecision(repo, id)
		s.logger.Info("decision log", "op", "remove", "ok", err == nil)
	default:
		send(w, http.StatusBadRequest, problem{Error: "op", Message: "The engine reads a project's decision log, and names, edits, hides, adds or removes your entries, nothing else."})
		return
	}
	if err != nil {
		code, p := status(err)
		send(w, code, p)
		return
	}
	answer := decisionLogOf(v)
	answer.savedSaid = said
	send(w, http.StatusOK, answer)
}
