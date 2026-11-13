package review

import (
	"regexp"
	"strconv"
	"strings"
)

type Source struct {
	Name string
	Text string
	Diff bool
}

type ShapedLine struct {
	File string
	Line int
	Text string
}

var hunkHeader = regexp.MustCompile(`^@@ -\d+(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

func passShaped(r *Rule, line string) bool {
	p := plainLine(line)
	if p == "" {
		return false
	}
	if m := machineLine.FindStringSubmatch(p); m != nil {
		return stateOf(m[1]) == Pass
	}
	if r == nil {
		m := verdictLine.FindStringSubmatch(p)
		return m != nil && stateOf(m[1]) == Pass
	}
	return !r.blocks(p) && r.passesAtStart(p)
}

func shapedText(r *Rule, l string) bool {
	for part := range strings.SplitSeq(l, "\r") {
		if passShaped(r, part) {
			return true
		}
	}
	return false
}

func FindShaped(sources []Source, r *Rule) (ShapedLine, bool) {
	for _, s := range sources {
		if s.Diff {
			if found, ok := shapedAdded(s, r); ok {
				return found, true
			}
			continue
		}
		n := 0
		for l := range strings.SplitSeq(s.Text, "\n") {
			n++
			if shapedText(r, l) {
				return ShapedLine{File: s.Name, Line: n, Text: l}, true
			}
		}
	}
	return ShapedLine{}, false
}

func shapedAdded(s Source, r *Rule) (ShapedLine, bool) {
	file := ""
	oldLeft, newLeft, next := 0, 0, 0
	for l := range strings.SplitSeq(s.Text, "\n") {
		if oldLeft > 0 || newLeft > 0 {
			switch {
			case strings.HasPrefix(l, "+") && newLeft > 0:
				if shapedText(r, l[1:]) {
					return ShapedLine{File: file, Line: next, Text: l[1:]}, true
				}
				newLeft--
				next++
				continue
			case strings.HasPrefix(l, "-") && oldLeft > 0:
				oldLeft--
				continue
			case strings.HasPrefix(l, " ") && oldLeft > 0 && newLeft > 0:
				oldLeft--
				newLeft--
				next++
				continue
			case strings.HasPrefix(l, `\`):
				continue
			}
			oldLeft, newLeft = 0, 0
		}
		if m := hunkHeader.FindStringSubmatch(l); m != nil {
			oldLeft, newLeft = hunkCount(m[1]), hunkCount(m[3])
			next, _ = strconv.Atoi(m[2])
			continue
		}
		if path, ok := strings.CutPrefix(l, "+++ b/"); ok {
			file = path
		} else if rest, ok := strings.CutPrefix(l, "diff --git a/"); ok {
			if _, after, found := strings.Cut(rest, " b/"); found {
				file = after
			}
		}
	}
	return ShapedLine{}, false
}

func hunkCount(s string) int {
	if s == "" {
		return 1
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func (s ShapedLine) Where() string {
	return "line " + strconv.Itoa(s.Line) + " of " + s.File
}
