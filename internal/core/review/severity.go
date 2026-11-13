package review

import (
	"regexp"
	"strings"
)

var (
	severeHeading = regexp.MustCompile(`(?i)^\s{0,3}#{2,4}\s*\[(critical|high)\]`)
	severeRow     = regexp.MustCompile(`(?i)^\|\s*F\d+\s*\|\s*(critical|high)\s*\|`)
)

func SevereFinding(reply string) bool {
	for l := range strings.SplitSeq(reply, "\n") {
		if severeHeading.MatchString(l) || severeRow.MatchString(l) {
			return true
		}
	}
	return false
}
