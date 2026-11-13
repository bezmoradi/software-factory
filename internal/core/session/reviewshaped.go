package session

import (
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/review"
)

const (
	maxShapedShown = 200
	shapedTooLarge = "the change is too large to check for lines shaped like a reviewer's verdict"
)

func shapedFor(rule *review.Rule, s Snapshot, given []SnapshotFile) string {
	if s.TooLarge {
		return shapedTooLarge
	}
	sources := []review.Source{{Name: "change", Text: s.Added, Diff: true}, {Name: CommitsFile, Text: messageLines(s.Commits)}}
	for _, f := range given {
		text := f.Text
		if f.Name == ClaimsFile {
			text = messageLines(text)
		}
		sources = append(sources, review.Source{Name: f.Name, Text: text})
	}
	found, ok := review.FindShaped(sources, rule)
	if !ok {
		return ""
	}
	return "the change adds a line shaped like a reviewer's verdict (" + shapedShown(found) + ")"
}

func messageLines(commits string) string {
	lines := strings.Split(commits, "\n")
	for i, l := range lines {
		rest, quoted := strings.CutPrefix(l, messageBar)
		if !quoted {
			rest = ""
		}
		lines[i] = rest
	}
	return strings.Join(lines, "\n")
}

func shapedShown(f review.ShapedLine) string {
	text := []rune(CleanLine(f.Text, maxQuotedLine))
	cut := len(text) > maxShapedShown
	text = text[:min(len(text), maxShapedShown)]
	shown := maskCut(maskLines(CleanLine(f.Where(), maxShapedShown)+": "+string(text), true))
	if cut {
		shown += "…"
	}
	return shown
}
