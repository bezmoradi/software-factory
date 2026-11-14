package workflow

import (
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxInstruction = 300
	MaxProtect     = 50
)

func plainLine(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == ' ' || r == ' ' {
			return false
		}
	}
	return true
}

func instructionOf(values map[string]any) (string, bool) {
	v, ok := values["instruction"]
	if !ok {
		return "", true
	}
	s, isStr := asString(v)
	s = strings.TrimSpace(s)
	if !isStr || s == "" || utf8.RuneCountInString(s) > MaxInstruction || !plainLine(s) {
		return "", false
	}
	return s, true
}

func parseProtect(root *object, team *Team, out *[]string) {
	v, ok := root.values["protect"]
	if !ok {
		return
	}
	list, isList := v.([]any)
	if !isList {
		*out = append(*out, "\"protect\" must be a list of path patterns")
		return
	}
	if len(list) > MaxProtect {
		*out = append(*out, "\"protect\" may hold at most "+strconv.Itoa(MaxProtect)+" patterns")
		return
	}
	for n, raw := range list {
		at := "protect[" + strconv.Itoa(n) + "]"
		p, isStr := asString(raw)
		why := "a pattern must be 1 to 120 characters"
		if isStr {
			why = PatternProblem(p)
		}
		if why == "" {
			if _, ok := globRegexp(p, true); !ok {
				why = "the pattern isn't one the engine can read"
			}
		}
		if why != "" {
			*out = append(*out, at+": "+why)
			continue
		}
		if !slices.Contains(team.Protect, p) {
			team.Protect = append(team.Protect, p)
		}
	}
}

func PathMatches(pattern, path string, fold bool) bool {
	if PatternProblem(pattern) != "" {
		return false
	}
	re, ok := globRegexp(pattern, fold)
	return ok && re.MatchString(path)
}
