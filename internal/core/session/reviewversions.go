package session

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/review"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	versionFor   = 30 * time.Second
	versionRetry = 2 * time.Second
	maxChain     = 50
)

var reviewVersionText = regexp.MustCompile(`^[0-9A-Za-z.+-]{1,40}$`)

func Rerunnable(e ReviewEntry) bool {
	return e.Record.State == review.Unknown && (e.Record.Channel == review.BelowMinimum || e.Record.Channel == review.Unchecked || e.Record.Channel == review.ApprovalChanged)
}

func versionsOf(e ReviewEntry) review.Versions {
	plugin := e.PluginCommit
	if e.Record.Provider == UploadedProvider {
		plugin = e.Key.Agent
	}
	return review.Versions{ClaudeCode: e.ClaudeCode, Plugin: plugin}
}

func (r *Reviews) Current() (string, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.claude, r.cfg.PluginCommit
}

func (r *Reviews) reviewerVersion(ctx context.Context) string {
	v := r.readVersion(ctx)
	if v == "" {
		select {
		case <-time.After(versionRetry):
			v = r.readVersion(ctx)
		case <-ctx.Done():
		}
	}
	switch {
	case v == "" && cancelled(ctx):
		shared.For(ctx, r.cfg.Logger).Info("review", "event", "stopped", "why", "version")
	case v == "":
		shared.For(ctx, r.cfg.Logger).Warn("review", "event", "version-unread")
	}
	r.mu.Lock()
	if v != r.claude {
		r.signStale = true
		r.signGen++
	}
	r.claude = v
	r.mu.Unlock()
	return v
}

func (r *Reviews) readVersion(ctx context.Context) string {
	vctx, cancel := context.WithTimeout(ctx, versionFor)
	defer cancel()
	res, err := r.cfg.Host.Run(vctx, Command{Argv: []string{r.cfg.Command, "--version"}, Dir: r.cfg.Dir, Pass: []string{"DISABLE_AUTOUPDATER"}, Timeout: versionFor})
	if err != nil || res.Code != 0 || res.TimedOut {
		return ""
	}
	if f := strings.Fields(res.Stdout); len(f) > 0 && reviewVersionText.MatchString(f[0]) {
		return f[0]
	}
	return ""
}

func (r *Reviews) passHeld(e ReviewEntry) (review.Channel, string) {
	if why := r.cfg.Minimum.Below(versionsOf(e), e.Record.Provider != UploadedProvider); why != "" {
		return review.BelowMinimum, "the reviewer that made it is older than the minimum this engine accepts (" + why + "), so the pass doesn't count; it runs again when the agent next asks to push or the person presses Review"
	}
	return "", ""
}

func (r *Reviews) counted(list []ReviewEntry) []ReviewEntry {
	index := make(map[review.Key]int, len(list))
	for i, e := range list {
		index[e.Key] = i
	}
	type held struct {
		channel review.Channel
		why     string
	}
	memo := map[int]held{}
	var hold func(i, depth int) held
	hold = func(i, depth int) held {
		if h, ok := memo[i]; ok {
			return h
		}
		e := list[i]
		var h held
		if e.Record.State == review.Pass {
			h.channel, h.why = r.passHeld(e)
			if h.why == "" && e.Since != "" && depth < maxChain {
				if j, ok := index[review.Key{Commit: e.Since, Branch: e.Key.Branch, Agent: e.Key.Agent, Run: e.Key.Run}]; ok && j != i && hold(j, depth+1).why != "" {
					h = held{review.Unchecked, "it reviewed only what changed since commit " + shortSHA(e.Since) + ", whose pass doesn't count, so this one doesn't either; it runs again in full when the agent next asks to push or the person presses Review"}
				}
			}
		}
		memo[i] = h
		return h
	}
	out := slices.Clone(list)
	for i := range out {
		h := hold(i, 0)
		if h.why == "" {
			continue
		}
		out[i].Record.State, out[i].Record.Channel, out[i].Record.Held = review.Unknown, h.channel, h.why
		out[i].Reason = h.why
	}
	return out
}
