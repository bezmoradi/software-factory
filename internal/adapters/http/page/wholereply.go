package page

import (
	"net/http"

	"github.com/bezmoradi/software-factory/internal/core/review"
	"github.com/bezmoradi/software-factory/internal/core/session"
)

const reviewReplyRoute = "/api/review-reply"

type wholeReplyJSON struct {
	Text    string `json:"text"`
	Clipped bool   `json:"clipped"`
}

func (s *Server) UseWholeReplies(w session.WholeReplies) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replies = w
}

func (s *Server) wholeReply(w http.ResponseWriter, body map[string]any) {
	s.mu.Lock()
	replies := s.replies
	s.mu.Unlock()
	if replies == nil {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine runs no reviews, so it keeps no whole reports."})
		return
	}
	sid, okSID := body["sid"].(string)
	agent, okAgent := body["agent"].(string)
	commit, okCommit := body["commit"].(string)
	run, okRun := wholeNumber(body["run"], 1, review.MaxRuns)
	if !keysWithin(body, "sid", "agent", "commit", "run") || !okSID || sid == "" || len(sid) > 80 || !okAgent || len(agent) > maxCheckAgent || !agentName.MatchString(agent) ||
		!okCommit || !commitHex.MatchString(commit) || !okRun {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	got, err := replies.WholeReply(sid, agent, commit, run)
	if err != nil {
		code, p := status(err)
		send(w, code, p)
		return
	}
	if len(got.Text) > session.MaxFullReply {
		refuse(w, http.StatusInternalServerError, "size")
		return
	}
	send(w, http.StatusOK, wholeReplyJSON{Text: got.Text, Clipped: got.Clipped})
}
