package state

import (
	"regexp"
	"slices"
)

const (
	maxModels  = 4
	maxDenied  = 20
	maxTurns   = 100000
	maxAttempt = 100
)

var (
	versionText = regexp.MustCompile(`^[0-9A-Za-z.+-]{1,40}$`)
	modelName   = regexp.MustCompile(`^[A-Za-z0-9._:@/\[\]-]{1,100}$`)
	toolName    = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)
	failureKind = regexp.MustCompile(`^(no-outcome|timeout|error)$`)
	requestID   = regexp.MustCompile(`^[0-9a-f]{16,64}$`)
)

func matching(s string, re *regexp.Regexp) string {
	if re.MatchString(s) {
		return s
	}
	return ""
}

func matchingAll(list []string, re *regexp.Regexp, limit int) []string {
	var out []string
	for _, s := range list {
		if len(out) >= limit {
			break
		}
		if re.MatchString(s) && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

func inRange(n, low, high int) int {
	if n < low || n > high {
		return 0
	}
	return n
}
