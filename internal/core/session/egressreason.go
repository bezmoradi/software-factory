package session

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

const MaxHostReason = 200

var hostReasonControl = regexp.MustCompile(`[\x00-\x1f\x7f\x{2028}\x{2029}]`)

func CleanHostReason(raw string) (string, error) {
	r := strings.TrimSpace(raw)
	switch {
	case r == "":
		return "", nil
	case !utf8.ValidString(r) || utf8.RuneCountInString(r) > MaxHostReason || hostReasonControl.MatchString(r):
		return "", onField("reason", invalid("reason", "Plain text on one line, at most 200 characters."))
	case secretText(r):
		return "", onField("reason", invalid("reason", "Looks like a token or key, so it wasn't saved."))
	}
	return r, nil
}
