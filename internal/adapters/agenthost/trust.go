package agenthost

import (
	"context"
	"maps"
	"net/http"
	"strings"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	trustWait   = 40 * time.Second
	trustSettle = 3 * time.Second
	keyPause    = 400 * time.Millisecond
	trustAsk    = 12 * time.Second
	trustCols   = 120
	trustRows   = 40
)

type trustScreen int

const (
	screenNone trustScreen = iota
	screenStyle
	screenNotes
	screenQuestion
	screenUnknown
	screenReady
)

func screenOf(screen string) trustScreen {
	t := strings.ToLower(strings.Join(strings.Fields(screen), " "))
	switch {
	case strings.Contains(t, "yes, i trust this folder"):
		return screenQuestion
	case strings.Contains(t, "choose the text style"):
		return screenStyle
	case strings.Contains(t, "press enter to continue"):
		return screenNotes
	case strings.Contains(t, "enter to confirm"):
		return screenUnknown
	case strings.Contains(t, "for shortcuts"), strings.Contains(t, "❯ try \""), strings.Contains(t, "/effort"):
		return screenReady
	}
	return screenNone
}

type trustReply struct {
	Trusted bool `json:"trusted"`
}

func (s *Server) trustStep(ctx context.Context, q commandJSON) (trustReply, *problem) {
	if s.cfg.Review {
		return trustReply{}, fail(http.StatusNotFound, "route", "")
	}
	env := map[string]string{"TERM": "xterm-256color"}
	maps.Copy(env, q.Env)
	q.Env = env
	answered, p := s.answerTrust(ctx, q, true)
	if p != nil {
		return trustReply{}, p
	}
	s.log.Info("agent-host trust", "step", "answer", "answered", answered)
	again, p := s.answerTrust(ctx, q, false)
	if p != nil {
		return trustReply{}, p
	}
	s.log.Info("agent-host trust", "step", "check", "asked again", again)
	if again {
		return trustReply{}, fail(http.StatusUnprocessableEntity, "trust", "Claude Code still asks whether you trust this folder, so the answer wasn't saved. Trust it in a terminal instead.")
	}
	return trustReply{Trusted: true}, nil
}

var _ session.TrustHost = (*Client)(nil)

func (c *Client) Trust(ctx context.Context, cmd session.Command) error {
	var out trustReply
	return c.call(ctx, http.MethodPost, "/v1/trust", wire(cmd), &out)
}
