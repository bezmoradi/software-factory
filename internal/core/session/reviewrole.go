package session

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/review"
)

func (r *Reviews) review(ctx context.Context, t ReviewTarget, e ReviewEntry, dir, plugin, prompt string) (ReviewEntry, string) {
	argv := ReviewArgv(r.cfg.Command, plugin, e.Key.Agent, prompt)
	rctx, cancel := context.WithTimeout(ctx, r.cfg.Timeout+reviewSlack)
	defer cancel()
	res, err := r.cfg.Host.Run(rctx, Command{Argv: argv, Dir: dir, Pass: []string{"DISABLE_AUTOUPDATER"}, Timeout: r.cfg.Timeout})
	switch {
	case err != nil && ctx.Err() != nil:
		return r.failed(e, stopped), "engine-stopped"
	case err != nil && rctx.Err() != nil:
		return r.failed(e, fmt.Sprintf("it didn't finish within %d minutes", int(r.cfg.Timeout/time.Minute))), "timeout"
	case err != nil:
		why, kind := reviewHostProblem(err)
		return r.failed(e, why), kind
	case res.TimedOut:
		return r.failed(e, fmt.Sprintf("it didn't finish within %d minutes", int(r.cfg.Timeout/time.Minute))), "timeout"
	}
	out, ok := claudeResult(res.Stdout)
	if !ok {
		why := fmt.Sprintf("its output wasn't Claude Code's JSON result (exit code %d)", res.Code)
		if l := firstLine(ansi.ReplaceAllString(res.Stderr, "")); l != "" && !flagText.MatchString(l) {
			why += ": " + l
		}
		return r.failed(e, why), "not-claude-json"
	}
	e.CostUSD = out.cost
	e.Turns, e.Denied, e.Models = max(out.turns, 0), out.denied, out.models
	if flagText.MatchString(out.text) {
		e.Record.State, e.Record.Verdict, e.Record.Channel = review.Unknown, "", review.Withheld
		e.Reason = "the reply contained a token-shaped string, so the engine didn't keep it"
		e.EndedAt = r.cfg.Now()
		return e, "token-in-reply"
	}
	text := strings.TrimSpace(out.text)
	e.Message = clip(text, maxReviewText)
	e.Clipped = len(text) > maxReviewText
	if out.failed || res.Code != 0 {
		why := "Claude Code reported an error"
		if out.subtype != "" && out.subtype != "success" {
			why += " (" + clip(out.subtype, 40) + ")"
		}
		if l := firstLine(out.text); l != "" {
			why += ": " + l
		}
		return r.failed(e, why), "claude-error"
	}
	rule := review.RuleFor(e.Key.Agent)
	v := review.ReplyVerdict(rule, out.text)
	e.Record.State = v.State
	e.Record.Verdict = v.Verdict
	e.Record.Channel = v.Channel
	kind := ""
	if v.State == review.Unknown {
		e.Reason = "its reply had no verdict the engine recognizes"
		kind = "no-verdict"
		if v.Channel == review.Conflict {
			e.Reason = "its reply gave both pass and block"
			kind = "two-verdicts"
		}
	}
	switch {
	case len(out.outside) > 0:
		e.Record.State, e.Record.Channel = review.Unknown, review.DeniedRead
		e.Reason = "it tried to read outside the review folder (" + strings.Join(out.outside, ", ") + "), so the code may have steered it; counts as no verdict"
		kind = "denied-read"
	case v.State != review.Pass:
	case out.turns == 1:
		e.Record.State, e.Record.Channel = review.Unknown, review.Shallow
		e.Reason = "it passed in one turn without reading a file, so it counts as no verdict"
		kind = "shallow"
	case rule != nil && review.SevereFinding(out.text):
		e.Record.State, e.Record.Channel = review.Unknown, review.SeverePass
		e.Reason = "it passed, but its report lists a Critical or High finding, so it counts as no verdict"
		kind = "severe-pass"
	case out.turns < 0:
		kind = "turns-unknown"
	}
	e.EndedAt = r.cfg.Now()
	return e, kind
}
