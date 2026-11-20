package session

import (
	"slices"
	"strings"
)

func (d *Dispatcher) reviewerStamp() (string, string) {
	if d.cfg.Reviews == nil {
		return "", ""
	}
	return d.cfg.Reviews.Current()
}

func (d *Dispatcher) installedWorkflow() string {
	if d.cfg.Workflow == nil {
		return ""
	}
	_, commit, err := d.cfg.Workflow()
	if err != nil {
		return ""
	}
	return commit
}

func (d *Dispatcher) refusedWorkflow(commit string) bool {
	return commit != "" && d.cfg.Refused != nil && d.cfg.Refused(commit)
}

func versionsChanged(name string, was, now runStamp, installed string, refused bool) string {
	var parts []string
	add := func(what, from, to string) {
		if from != "" && to != "" && from != to {
			parts = append(parts, what+" went from "+from+" to "+to)
		}
	}
	add(name, was.agent, now.agent)
	add("the reviewer's Claude Code", was.reviewer, now.reviewer)
	add("the reviewers' plugin", shortSHA7(was.reviewerPlugin), shortSHA7(now.reviewerPlugin))
	var out []string
	if len(parts) > 0 {
		out = append(out, "Since this session last ran, "+strings.Join(parts, ", ")+".")
	}
	switch {
	case refused:
		out = append(out, "This session runs knackpack "+shortSHA7(now.workflow)+", which this engine refuses; start a new session.")
	case now.workflow != "" && installed != "" && installed != now.workflow:
		out = append(out, "This session still runs knackpack "+shortSHA7(now.workflow)+"; a new session gets "+shortSHA7(installed)+".")
	}
	return strings.Join(out, " ")
}

func (d *Dispatcher) WorkflowCommits() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, r := range d.runs {
		if r.stamp.workflow != "" && !slices.Contains(out, r.stamp.workflow) {
			out = append(out, r.stamp.workflow)
		}
	}
	return out
}

func (d *Dispatcher) showVersions(sid, text string) {
	if text != "" {
		d.reg.Notify(Fields{"type": "versions", "sid": sid, "text": text})
	}
}
