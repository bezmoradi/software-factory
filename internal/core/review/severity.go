package review

import (
	"regexp"
	"strings"
)

var (
	severeHeading  = regexp.MustCompile(`(?i)^\s{0,3}#{2,4}\s*\[(critical|high)\]`)
	severeRow      = regexp.MustCompile(`(?i)^\|\s*F\d+\s*\|\s*(critical|high)\s*\|`)
	findingsCounts = regexp.MustCompile(`^knackpack-findings/1: critical=(\d+) high=(\d+) medium=(\d+) low=(\d+) blocking=(\d+)$`)
)

const (
	countCritical = iota + 1
	countHigh
	countMedium
	countLow
	countBlocking
)

func SevereFinding(reply string) bool {
	for l := range strings.SplitSeq(reply, "\n") {
		if severeHeading.MatchString(l) || severeRow.MatchString(l) {
			return true
		}
	}
	return false
}

func SevereCount(reply string) bool {
	return counted(reply, countCritical, countHigh, countBlocking)
}

func AnyCount(reply string) bool {
	return counted(reply, countCritical, countHigh, countMedium, countLow, countBlocking)
}

func counted(reply string, groups ...int) bool {
	for l := range strings.SplitSeq(reply, "\n") {
		m := findingsCounts.FindStringSubmatch(plainLine(l))
		if m == nil {
			continue
		}
		for _, g := range groups {
			if strings.TrimLeft(m[g], "0") != "" {
				return true
			}
		}
	}
	return false
}
