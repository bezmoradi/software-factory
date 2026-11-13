package session

import (
	"regexp"
	"slices"
	"strings"
)

const (
	maxReviewModels  = 4
	maxReviewDenials = 20
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
	for name := range m {
		if reviewModelName.MatchString(name) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
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
	if pattern, _ := in["pattern"].(string); tool == "Glob" && pattern != "" && pattern != target && !strings.HasPrefix(pattern, "/") && !strings.HasPrefix(pattern, "~") {
		target = strings.TrimSuffix(target, "/") + "/" + pattern
	}
	return target != "" && !strings.Contains(target, "..") && instructionTarget.MatchString(target)
}

func (r *Reviews) audited(t ReviewTarget, e ReviewEntry) {
	if r.cfg.Audit == nil {
		return
	}
	by := ByPage
	if t.Auto {
		by = ByAgent
	}
	ae := AuditEntry{Time: r.cfg.Now(), Session: t.SID, Repository: t.Name, Branch: t.Branch, Commit: e.Key.Commit, By: by, Kind: AuditReview,
		Result: string(e.Record.State), Reason: e.Reason, Reviewer: e.Key.Agent, Verdict: e.Record.Verdict,
		PluginCommit: e.PluginCommit, ClaudeCode: e.ClaudeCode, Models: slices.Clone(e.Models)}
	if err := r.cfg.Audit.Append(ae); err != nil {
		r.cfg.Logger.Warn("review", "event", "audit-not-written")
	}
}
