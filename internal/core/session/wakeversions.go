package session

import "strings"

func (d *Dispatcher) reviewerStamp() (string, string) {
	if d.cfg.Reviews == nil {
		return "", ""
	}
	return d.cfg.Reviews.Current()
}

func versionsChanged(name string, was, now runStamp) string {
	var parts []string
	add := func(what, from, to string) {
		if from != "" && to != "" && from != to {
			parts = append(parts, what+" went from "+from+" to "+to)
		}
	}
	add(name, was.agent, now.agent)
	add(name+"'s workflow plugin", shortSHA7(was.workflow), shortSHA7(now.workflow))
	add("the reviewer's Claude Code", was.reviewer, now.reviewer)
	add("the reviewers' plugin", shortSHA7(was.reviewerPlugin), shortSHA7(now.reviewerPlugin))
	if len(parts) == 0 {
		return ""
	}
	return "Since this session last ran, " + strings.Join(parts, ", ") + "."
}

func (d *Dispatcher) showVersions(sid, text string) {
	if text != "" {
		d.reg.Notify(Fields{"type": "versions", "sid": sid, "text": text})
	}
}
