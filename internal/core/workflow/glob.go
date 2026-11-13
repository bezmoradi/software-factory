package workflow

import (
	"fmt"
	"regexp"
	"strings"
)

type runeRange struct{ lo, hi rune }

const lineChar = `[^\n\r\x{2028}\x{2029}]`

var posixClasses = map[string][]runeRange{
	"alpha":  {{'a', 'z'}, {'A', 'Z'}},
	"digit":  {{'0', '9'}},
	"alnum":  {{'a', 'z'}, {'A', 'Z'}, {'0', '9'}},
	"upper":  {{'A', 'Z'}},
	"lower":  {{'a', 'z'}},
	"space":  {{' ', ' '}, {'\t', '\r'}},
	"blank":  {{' ', ' '}, {'\t', '\t'}},
	"punct":  {{'!', '/'}, {':', '@'}, {'[', '`'}, {'{', '~'}},
	"xdigit": {{'0', '9'}, {'A', 'F'}, {'a', 'f'}},
	"cntrl":  {{0, 0x1f}, {0x7f, 0x7f}},
	"graph":  {{'!', '~'}},
	"print":  {{' ', '~'}},
}

var artifactRefused = regexp.MustCompile(`(?i)(^|/)\.\.(/|$)|^/|^\.git(/|$)|^\.claude(/|$)|(^|/)\.gitignore$|^software-factory\.json$`)

func classOf(ranges []runeRange) string {
	var b strings.Builder
	for _, r := range ranges {
		fmt.Fprintf(&b, `\x{%x}-\x{%x}`, r.lo, r.hi)
	}
	return b.String()
}

func withoutSlash(ranges []runeRange) []runeRange {
	var out []runeRange
	for _, r := range ranges {
		if r.lo > '/' || r.hi < '/' {
			out = append(out, r)
			continue
		}
		if r.lo < '/' {
			out = append(out, runeRange{r.lo, '/' - 1})
		}
		if r.hi > '/' {
			out = append(out, runeRange{'/' + 1, r.hi})
		}
	}
	return out
}

func globClass(p []rune, at int) (string, int, bool) {
	j := at + 1
	negate := j < len(p) && (p[j] == '!' || p[j] == '^')
	if negate {
		j++
	}
	var ranges []runeRange
	one := func() (rune, bool) {
		if j < len(p) && p[j] == '\\' {
			j++
		}
		if j >= len(p) {
			return 0, false
		}
		j++
		return p[j-1], true
	}
	first := true
	for j < len(p) && (p[j] != ']' || first) {
		first = false
		if p[j] == '[' && j+1 < len(p) && p[j+1] == ':' {
			end := strings.Index(string(p[j+2:]), ":]")
			if end < 0 {
				return "", 0, false
			}
			name := string(p[j+2:])[:end]
			cls, ok := posixClasses[name]
			if !ok {
				return "", 0, false
			}
			ranges = append(ranges, cls...)
			j += 2 + len([]rune(name)) + 2
			continue
		}
		lo, ok := one()
		if !ok {
			return "", 0, false
		}
		hi := lo
		if j+1 < len(p) && p[j] == '-' && p[j+1] != ']' {
			j++
			if hi, ok = one(); !ok || hi < lo {
				return "", 0, false
			}
		}
		ranges = append(ranges, runeRange{lo, hi})
	}
	if j >= len(p) || p[j] != ']' {
		return "", 0, false
	}
	if negate {
		return `[^/` + classOf(ranges) + `]`, j + 1, true
	}
	kept := withoutSlash(ranges)
	if len(kept) == 0 {
		return `[^\x00-\x{10ffff}]`, j + 1, true
	}
	return `[` + classOf(kept) + `]`, j + 1, true
}

func globRegexp(pattern string, fold bool) (*regexp.Regexp, bool) {
	p := []rune(pattern)
	var out strings.Builder
	if fold {
		out.WriteString("(?i)")
	}
	out.WriteString("^")
	for i := 0; i < len(p); {
		c := p[i]
		switch c {
		case '*':
			j := i
			for j < len(p) && p[j] == '*' {
				j++
			}
			if j-i >= 2 && (i == 0 || p[i-1] == '/') && (j == len(p) || p[j] == '/') {
				if j == len(p) {
					out.WriteString(lineChar + "*")
					i = j
				} else {
					out.WriteString(`(?:` + lineChar + `*/)?`)
					i = j + 1
				}
				continue
			}
			out.WriteString(`[^/]*`)
			i = j
		case '?':
			out.WriteString(`[^/]`)
			i++
		case '[':
			cls, end, ok := globClass(p, i)
			if !ok {
				return nil, false
			}
			out.WriteString(cls)
			i = end
		case '\\':
			if i+1 >= len(p) {
				return nil, false
			}
			out.WriteString(regexp.QuoteMeta(string(p[i+1])))
			i += 2
		default:
			out.WriteString(regexp.QuoteMeta(string(c)))
			i++
		}
	}
	out.WriteString("$")
	re, err := regexp.Compile(out.String())
	return re, err == nil
}

func artifactPath(path string, patterns []string) bool {
	if path == "" || artifactRefused.MatchString(path) {
		return false
	}
	for _, p := range patterns {
		if artifactRefused.MatchString(p) || len([]rune(p)) > 200 || strings.Count(p, "*") > 4 {
			continue
		}
		if re, ok := globRegexp(p, false); ok && re.MatchString(path) {
			return true
		}
	}
	return false
}
