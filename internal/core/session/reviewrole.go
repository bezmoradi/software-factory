package session

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/review"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

func (r *Reviews) reviewWith(ctx context.Context, t ReviewTarget, e ReviewEntry, dir, plugin, prompt string, progress func(RunProgress)) (ReviewEntry, string) {
	return r.reviewAs(ctx, e, dir, plugin, prompt, r.roleModelFor(t.Repo, e.Role), progress)
}

func (r *Reviews) reviewAs(ctx context.Context, e ReviewEntry, dir, plugin, prompt string, m ModelChoice, progress func(RunProgress)) (ReviewEntry, string) {
	e.RunModel, e.RunEffort = m.Model, m.Effort
	argv := ReviewArgvWith(r.cfg.Command, plugin, e.Key.Agent, prompt, m)
	rctx, cancel := context.WithTimeout(ctx, r.cfg.Timeout+reviewSlack)
	defer cancel()
	res, err := r.reviewerRun(rctx, Command{Argv: argv, Dir: dir, Pass: []string{"DISABLE_AUTOUPDATER"}, Timeout: r.cfg.Timeout}, progress)
	switch {
	case err != nil && ctx.Err() != nil:
		return r.failed(e, stopped), "engine-stopped"
	case err != nil && rctx.Err() != nil:
		return r.failed(e, timedOut(r.cfg.Timeout)), "timeout"
	case err != nil:
		why, kind := reviewHostProblem(err)
		return r.failed(e, why), kind
	case res.TimedOut:
		return r.failed(e, timedOut(r.cfg.Timeout)), "timeout"
	}
	out, ok := claudeResult(res.Stdout)
	if !ok {
		why := fmt.Sprintf("its output wasn't Claude Code's JSON result (exit code %d)", res.Code)
		if l := firstLine(ansi.ReplaceAllString(res.Stderr, "")); l != "" && !secretText(l) {
			why += ": " + l
		}
		return r.failed(e, why), "not-claude-json"
	}
	e.CostUSD = out.cost
	e.Turns, e.Denied, e.Models = max(out.turns, 0), out.denied, out.models
	if secretText(out.text) {
		e.Record.State, e.Record.Verdict, e.Record.Channel = review.Unknown, "", review.Withheld
		e.Reason = "the reply contained a token-shaped string, so the engine didn't keep it"
		e.EndedAt = r.cfg.Now()
		return e, "token-in-reply"
	}
	text := strings.TrimSpace(out.text)
	e.Message, e.Clipped = keptReply(text, maxReviewText)
	e.Reply, e.ReplyClipped = keptWhole(text, MaxFullReply)
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
		if strings.HasSuffix(e.Record.Provider, review.PackProviderSuffix) && v.Channel == "" {
			e.Reason = PackNoVerdict
		}
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
	case e.Record.Shaped != "":
		e.Record.State, e.Record.Channel = review.Unknown, review.ShapedPass
		e.Reason = e.Record.Shaped + ", so its pass counts as no verdict"
		kind = "verdict-shaped"
	case out.turns == 1:
		e.Record.State, e.Record.Channel = review.Unknown, review.Shallow
		e.Reason = "it passed in one turn without reading a file, so it counts as no verdict"
		kind = "shallow"
	case rule != nil && review.SevereFinding(out.text):
		e.Record.State, e.Record.Channel = review.Unknown, review.SeverePass
		e.Reason = "it passed, but its report lists a Critical or High finding, so it counts as no verdict"
		kind = "severe-pass"
	case review.SevereCount(out.text):
		e.Record.State, e.Record.Channel = review.Unknown, review.SeverePass
		e.Reason = "it passed, but its findings line counts a Critical, High or blocking finding, so it counts as no verdict"
		kind = "severe-pass"
	case out.turns < 0:
		kind = "turns-unknown"
	}
	e.EndedAt = r.cfg.Now()
	return e, kind
}

const PackNoVerdict = "its reply ended without a Verdict: pass or Verdict: block line, which every pack's reviewer must write, so it counts as no verdict"

func (r *Reviews) placeAgents(ctx context.Context, plans []plannedRole) string {
	if r.cfg.AgentFiles == nil {
		return ""
	}
	var files []AgentFileOut
	for _, p := range plans {
		if u, ok := r.cfg.AgentFiles.Uploaded(p.q.Agent); ok && !slices.ContainsFunc(files, func(f AgentFileOut) bool { return f.Name == AgentFileOf(u.ID) }) {
			files = append(files, u.Out())
		}
	}
	host, ok := r.cfg.Host.(AgentFileHost)
	if !ok {
		if len(files) > 0 {
			return "this engine can't write agent files into the reviewers' agents folder"
		}
		return ""
	}
	pctx, cancel := context.WithTimeout(ctx, reviewCheck)
	defer cancel()
	if err := host.PutAgents(pctx, files, r.cfg.AgentFiles.Files()); err != nil {
		shared.For(ctx, r.cfg.Logger).Warn("review", "event", "agent-files-not-written", "uploaded", len(files))
		if len(files) > 0 {
			return clip("the engine couldn't write the uploaded agent file into the reviewers' agents folder: "+hostMessage(err), 400)
		}
	}
	return ""
}

func keptReply(text string, limit int) (string, bool) {
	if len(text) <= limit {
		return text, false
	}
	lines := strings.Split(text, "\n")
	var ending []string
	closing, size := 0, 0
	for _, line := range slices.Backward(lines) {
		l := strings.TrimSpace(line)
		if l == "" {
			continue
		}
		bare := strings.ToLower(strings.TrimLeft(l, "*_`#> "))
		if strings.HasPrefix(l, FindingLinePrefix) && len(l) <= MaxFindingLine && len(ending)-closing < MaxFindingLines && size+len(l)+1 <= limit/2 {
			ending = append([]string{l}, ending...)
			size += len(l) + 1
			continue
		}
		if closing >= 3 || len(ending) > closing || (!strings.HasPrefix(bare, "knackpack-findings/") && !strings.HasPrefix(bare, "knackpack-verdict/")) {
			break
		}
		kept := clip(l, 200)
		ending = append([]string{kept}, ending...)
		closing++
		size += len(kept) + 1
	}
	if len(ending) == 0 {
		return clip(text, limit), true
	}
	tail := "\n" + clippedMiddle + "\n" + strings.Join(ending, "\n")
	return strings.TrimSpace(clip(text, limit-len(tail))) + tail, true
}

const WholeTail = 16 << 10

func keptWhole(text string, limit int) (string, bool) {
	if len(text) <= limit {
		return text, false
	}
	end, _ := keptReply(text, limit)
	want := WholeTail
	if i := strings.LastIndex(end, clippedMiddle); i >= 0 {
		want = max(want, len(end)-i-len(clippedMiddle))
	}
	want = min(want, limit/2)
	from := len(text) - want
	if i := strings.IndexByte(text[from:], '\n'); i >= 0 && text[from-1] != '\n' {
		from += i + 1
	}
	for from < len(text) && !utf8.RuneStart(text[from]) {
		from++
	}
	tail := text[from:]
	marker := "\n" + clippedMiddle + "\n"
	return strings.TrimSpace(byteHead(text, limit-len(tail)-len(marker))) + marker + tail, true
}

func byteHead(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func timedOut(limit time.Duration) string {
	n := int(limit / time.Minute)
	return fmt.Sprintf("it didn't finish within %d %s", n, plural(n, "minute", "minutes"))
}
