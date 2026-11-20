package session

import (
	"cmp"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	maxReviewModels    = 4
	maxReviewDenials   = 20
	maxBraceExpansions = 8
)

var (
	reviewToolName    = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)
	reviewModelName   = regexp.MustCompile(`^[A-Za-z0-9._:@/\[\]-]{1,100}$`)
	instructionTarget = regexp.MustCompile(`^(~|/home/[A-Za-z0-9._-]{1,64}|/root)/\.claude/(CLAUDE\.md|rules(/[^\x00]*)?)$`)
	readTools         = []string{"Read", "Grep", "Glob"}
)

func modelsOf(v any) []string {
	m, _ := v.(map[string]any)
	var out []string
	spent := map[string]float64{}
	for name, u := range m {
		if reviewModelName.MatchString(name) {
			out = append(out, name)
			fields, _ := u.(map[string]any)
			spent[name], _ = fields["costUSD"].(float64)
		}
	}
	slices.SortFunc(out, func(a, b string) int {
		if spent[a] != spent[b] {
			return cmp.Compare(spent[b], spent[a])
		}
		return strings.Compare(a, b)
	})
	return out[:min(len(out), maxReviewModels)]
}

func denialsOf(v any) ([]string, []string) {
	list, _ := v.([]any)
	var names, outside []string
	for _, item := range list {
		d, _ := item.(map[string]any)
		name, _ := d["tool_name"].(string)
		if !reviewToolName.MatchString(name) {
			continue
		}
		if len(names) < maxReviewDenials && !slices.Contains(names, name) {
			names = append(names, name)
		}
		if slices.Contains(readTools, name) && !instructionRead(name, d["tool_input"]) && !slices.Contains(outside, name) {
			outside = append(outside, name)
		}
	}
	return names, outside
}

func instructionRead(tool string, v any) bool {
	in, _ := v.(map[string]any)
	target := ""
	for _, key := range []string{"file_path", "path", "pattern"} {
		if s, _ := in[key].(string); s != "" {
			target = s
			break
		}
	}
	pattern, _ := in["pattern"].(string)
	if tool != "Glob" || pattern == "" {
		return instructionPath(target)
	}
	base := ""
	if target != pattern {
		base = strings.TrimSuffix(target, "/") + "/"
	}
	alternatives, ok := braceExpansions(pattern)
	if !ok {
		return false
	}
	for _, a := range alternatives {
		t := a
		if !strings.HasPrefix(a, "/") && !strings.HasPrefix(a, "~") {
			t = base + a
		}
		if !instructionPath(t) {
			return false
		}
	}
	return true
}

func instructionPath(target string) bool {
	return target != "" && !strings.Contains(target, "..") && instructionTarget.MatchString(target)
}

func braceExpansions(s string) ([]string, bool) {
	if strings.ContainsRune(s, '\\') {
		return nil, false
	}
	out := []string{""}
	for {
		start, end := strings.IndexByte(s, '{'), strings.IndexByte(s, '}')
		if start < 0 {
			if end >= 0 {
				return nil, false
			}
			for i := range out {
				out[i] += s
			}
			return out, true
		}
		if end < start {
			return nil, false
		}
		alts := strings.Split(s[start+1:end], ",")
		if len(alts) < 2 || len(out)*len(alts) > maxBraceExpansions || strings.ContainsRune(s[start+1:end], '{') {
			return nil, false
		}
		next := make([]string, 0, len(out)*len(alts))
		for _, prefix := range out {
			for _, alt := range alts {
				if alt == "" {
					return nil, false
				}
				next = append(next, prefix+s[:start]+alt)
			}
		}
		out, s = next, s[end+1:]
	}
}

func (r *Reviews) audited(t ReviewTarget, e ReviewEntry) {
	if r.cfg.Audit == nil {
		return
	}
	by := ByPage
	if t.Auto {
		by = ByAgent
	}
	reason := e.Reason
	if e.Key.Number() > 1 {
		reason = strings.TrimSuffix("run "+strconv.Itoa(e.Key.Number())+" of this role on the commit; "+reason, "; ")
	}
	if e.Record.Advisory {
		reason = strings.TrimSuffix("an advisory role, which never counts for the push; "+reason, "; ")
	}
	ae := AuditEntry{Time: r.cfg.Now(), Session: t.SID, Repository: t.Name, Branch: t.Branch, Commit: e.Key.Commit, By: by, Kind: AuditReview,
		Result: string(e.Record.State), Reason: reason, Reviewer: e.Key.Agent, Verdict: e.Record.Verdict,
		PluginCommit: e.PluginCommit, Hash: e.PackDigest, ClaudeCode: e.ClaudeCode, Models: slices.Clone(e.Models), Req: t.Req}
	if err := r.cfg.Audit.Append(ae); err != nil {
		shared.Tagged(r.cfg.Logger, t.Req).Warn("review", "event", "audit-not-written")
	}
}
