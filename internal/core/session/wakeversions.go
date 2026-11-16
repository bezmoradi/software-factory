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
	reviewer := false
	add := func(what, from, to string, ofReviewer bool) {
		if from != "" && to != "" && from != to {
			parts = append(parts, what+" went from "+from+" to "+to)
			reviewer = reviewer || ofReviewer
		}
	}
	add(name, was.agent, now.agent, false)
	add(name+"'s workflow plugin", shortSHA7(was.workflow), shortSHA7(now.workflow), false)
	add("the reviewer's Claude Code", was.reviewer, now.reviewer, true)
	add("the reviewers' plugin", shortSHA7(was.reviewerPlugin), shortSHA7(now.reviewerPlugin), true)
	if len(parts) == 0 {
		return ""
	}
	text := "Since this session last ran, " + strings.Join(parts, ", ") + "."
	if reviewer {
		text += " The engine checks each reviewer after an update before its next pass counts."
	}
	return text
}

func (d *Dispatcher) showVersions(sid, text string) {
	if text != "" {
		d.reg.Notify(Fields{"type": "versions", "sid": sid, "text": text})
	}
}
