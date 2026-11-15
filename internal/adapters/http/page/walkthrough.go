package page

import (
	"context"
	"math"
	"net/http"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const walkthroughRoute = "/api/walkthrough-check"

type walkStepJSON struct {
	Do     string `json:"do"`
	Expect string `json:"expect"`
	Input  string `json:"input"`
	Done   bool   `json:"done"`
}

func stepsOf(list []session.WalkStep) []walkStepJSON {
	if len(list) == 0 {
		return nil
	}
	out := make([]walkStepJSON, 0, len(list))
	for _, s := range list {
		out = append(out, walkStepJSON{Do: s.Do, Expect: s.Expect, Input: s.Input, Done: s.Done})
	}
	return out
}

type progressJSON struct {
	ElapsedMs int64 `json:"elapsedMs"`
	ToolCalls int   `json:"toolCalls"`
	Done      bool  `json:"done"`
}

func progressOf(p *session.ReviewProgress) *progressJSON {
	if p == nil {
		return nil
	}
	return &progressJSON{ElapsedMs: p.Elapsed.Milliseconds(), ToolCalls: p.ToolCalls, Done: p.Done}
}

type givenJSON struct {
	Prompt string   `json:"prompt"`
	Flags  []string `json:"flags"`
	Files  []string `json:"files"`
}

func givenOf(g *session.RunGiven) *givenJSON {
	if g == nil {
		return nil
	}
	out := &givenJSON{Prompt: g.Prompt, Flags: g.Flags, Files: g.Files}
	if out.Flags == nil {
		out.Flags = []string{}
	}
	if out.Files == nil {
		out.Files = []string{}
	}
	return out
}

func (s *Server) walkthroughCheck(w http.ResponseWriter, r *http.Request, body map[string]any) {
	checker, ok := s.cfg.Reviews.(session.WalkthroughChecker)
	if s.cfg.Reviews == nil || !ok {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine has no reviewer container, so it keeps no walkthroughs."})
		return
	}
	sid, okSid := body["sid"].(string)
	commit, okCommit := body["commit"].(string)
	step, okStep := body["step"].(float64)
	done, okDone := body["done"].(bool)
	if !keysWithin(body, "sid", "commit", "step", "done") || !okSid || sid == "" || len(sid) > 80 || !okCommit || !commitHex.MatchString(commit) ||
		!okStep || step != math.Trunc(step) || step < 1 || step > session.MaxWalkSteps || !okDone {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(manageFor + writeFor))
	ctx, cancel := context.WithTimeout(r.Context(), manageFor)
	defer cancel()
	err := checker.CheckStep(ctx, sid, commit, int(step), done)
	shared.For(r.Context(), s.logger).Info("walkthrough", "ok", err == nil)
	reply(w, err, http.StatusOK)
}
