package review

import (
	"regexp"
	"strconv"
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

type Counts struct {
	Critical int
	High     int
	Medium   int
	Low      int
	Blocking int
}

func (c Counts) Of(severity string) int {
	switch strings.ToLower(severity) {
	case "critical":
		return c.Critical
	case "high":
		return c.High
	case "medium":
		return c.Medium
	case "low":
		return c.Low
	}
	return 0
}

func CountsLine(reply string) (Counts, bool) {
	var out Counts
	found := false
	for l := range strings.SplitSeq(reply, "\n") {
		m := findingsCounts.FindStringSubmatch(plainLine(l))
		if m == nil {
			continue
		}
		var n [5]int
		ok := true
		for i := range n {
			v, err := strconv.Atoi(m[i+1])
			if err != nil || v > 1000000 {
				ok = false
				break
			}
			n[i] = v
		}
		if ok {
			out, found = Counts{Critical: n[0], High: n[1], Medium: n[2], Low: n[3], Blocking: n[4]}, true
		}
	}
	return out, found
}
