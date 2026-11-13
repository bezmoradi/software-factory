package gate

import (
	"regexp"
	"strconv"
	"strings"
)

const shellDepth = 8

type heredoc struct {
	tag    string
	strip  bool
	expand bool
}

type parsed struct {
	cmds   [][]string
	bodies []string
	fed    *bool
	said   *bool
	code   *bool
}

var (
	braceForbidden = regexp.MustCompile("[$`'\"\\\\(]")
	tagForbidden   = regexp.MustCompile("[\\\\$`\n]")
	bareTag        = regexp.MustCompile(`^[A-Za-z0-9_.-]+`)
	afterBareTag   = regexp.MustCompile("^['\"\\\\$`]")
	runsInBody     = regexp.MustCompile("\\$\\(|`")
	digitsOnly     = regexp.MustCompile(`^\d+$`)
	redirection    = regexp.MustCompile(`^(<<<|<>|<&|<|>>|>&|>\||>|&>>|&>)`)
	leadingTabs    = regexp.MustCompile(`^\t+`)
	printEscape    = regexp.MustCompile(`\\(x[0-9a-fA-F]{1,2}|0?[0-7]{1,3}|[ntrfvab\\])`)
)

func at(s string, i int) byte {
	if i < 0 || i >= len(s) {
		return 0
	}
	return s[i]
}

func shellParse(s string, start, depth int, out *parsed) (int, bool) {
	if depth > shellDepth {
		return 0, false
	}
	var words []string
	var word strings.Builder
	has, target, feed := false, false, false
	group := 0
	var docs []heredoc
	i := start
	endWord := func() {
		if has {
			switch {
			case feed:
				out.bodies = append(out.bodies, word.String())
			case !target:
				words = append(words, word.String())
			}
			target, feed = false, false
		}
		word.Reset()
		has = false
	}
	endCmd := func() bool {
		endWord()
		if target || feed {
			return false
		}
		if len(words) > 0 {
			out.cmds = append(out.cmds, words)
		}
		words = nil
		return true
	}
	expansion := func(j int) (int, bool) {
		if at(s, j+1) == '(' {
			return shellParse(s, j+2, depth+1, out)
		}
		end := strings.IndexByte(s[j:], '}')
		if end < 0 || braceForbidden.MatchString(s[j+2:j+end]) {
			return 0, false
		}
		return j + end + 1, true
	}
	for i < len(s) {
		c := s[i]
		next := at(s, i+1)
		switch {
		case c == '\\':
			if i+1 < len(s) && next != '\n' {
				word.WriteByte(next)
				has = true
			}
			i += 2
		case c == '\'':
			end := strings.IndexByte(s[i+1:], '\'')
			if end < 0 {
				return 0, false
			}
			word.WriteString(s[i+1 : i+1+end])
			has = true
			i += end + 2
		case c == '"':
			has = true
			i++
			for {
				if i >= len(s) || s[i] == '`' {
					return 0, false
				}
				d := s[i]
				if d == '"' {
					i++
					break
				}
				after := at(s, i+1)
				switch {
				case d == '\\' && i+1 < len(s) && strings.IndexByte("$`\"\\\n", after) >= 0:
					word.WriteByte(after)
					i += 2
				case d == '$' && (after == '(' || after == '{'):
					end, ok := expansion(i)
					if !ok {
						return 0, false
					}
					word.WriteByte(0)
					i = end
				default:
					word.WriteByte(d)
					i++
				}
			}
		case c == '$' && (next == '(' || next == '{'):
			end, ok := expansion(i)
			if !ok {
				return 0, false
			}
			word.WriteByte(0)
			has = true
			i = end
		case c == '$' && next == '\'':
			return 0, false
		case c == ' ' || c == '\t':
			endWord()
			i++
		case c == '\n':
			if !endCmd() {
				return 0, false
			}
			i++
			for _, doc := range docs {
				var body []string
				for {
					if i > len(s) {
						return 0, false
					}
					nl := strings.IndexByte(s[i:], '\n')
					stop := len(s)
					if nl >= 0 {
						stop = i + nl
					}
					line := s[i:stop]
					i = stop + 1
					cmp := line
					if doc.strip {
						cmp = leadingTabs.ReplaceAllString(line, "")
					}
					if cmp == doc.tag {
						break
					}
					if doc.expand && runsInBody.MatchString(line) {
						return 0, false
					}
					body = append(body, line)
					if nl < 0 {
						return 0, false
					}
				}
				out.bodies = append(out.bodies, strings.Join(body, "\n"))
			}
			docs = nil
		case c == '<' && next == '<' && at(s, i+2) != '<':
			endWord()
			i += 2
			strip := at(s, i) == '-'
			if strip {
				i++
			}
			for at(s, i) == ' ' || at(s, i) == '\t' {
				i++
			}
			quote := at(s, i)
			if quote == '\'' || quote == '"' {
				end := strings.IndexByte(s[i+1:], quote)
				tag := ""
				if end >= 0 {
					tag = s[i+1 : i+1+end]
				}
				if tag == "" || tagForbidden.MatchString(tag) {
					return 0, false
				}
				docs = append(docs, heredoc{tag: tag, strip: strip})
				i += end + 2
			} else {
				tag := bareTag.FindString(s[i:])
				if tag == "" || afterBareTag.MatchString(s[i+len(tag):]) {
					return 0, false
				}
				docs = append(docs, heredoc{tag: tag, strip: strip, expand: true})
				i += len(tag)
			}
		case c == '<' || c == '>' || (c == '&' && next == '>'):
			if next == '(' {
				return 0, false
			}
			if digitsOnly.MatchString(word.String()) {
				word.Reset()
				has = false
			}
			endWord()
			op := redirection.FindString(s[i:min(len(s), i+3)])
			if op == "" {
				op = string(c)
			}
			switch {
			case op == "<<<":
				feed = true
			case !strings.HasPrefix(op, "<"):
				target = true
			}
			i += len(op)
		case c == ';' || c == '&' || c == '|':
			if !endCmd() {
				return 0, false
			}
			if (c == '&' || c == '|') && (next == c || (c == '|' && next == '&')) {
				i += 2
			} else {
				i++
			}
		case c == '(':
			if has || !endCmd() {
				return 0, false
			}
			group++
			i++
		case c == ')':
			if len(docs) > 0 || !endCmd() {
				return 0, false
			}
			switch {
			case group > 0:
				group--
				i++
			case depth > 0:
				return i + 1, true
			default:
				i++
			}
		case c == '#' && !has:
			nl := strings.IndexByte(s[i:], '\n')
			if nl < 0 {
				i = len(s)
			} else {
				i += nl
			}
		case c == '`':
			return 0, false
		default:
			word.WriteByte(c)
			has = true
			i++
		}
	}
	if depth > 0 || group > 0 || len(docs) > 0 || !endCmd() {
		return 0, false
	}
	return i, true
}

func printed(words []string) string {
	return printEscape.ReplaceAllStringFunc(strings.Join(words, " "), func(m string) string {
		e := m[1:]
		switch {
		case e[0] == 'x':
			n, _ := strconv.ParseUint(e[1:], 16, 8)
			return string(rune(n))
		case e[0] >= '0' && e[0] <= '7':
			n, _ := strconv.ParseUint(e, 8, 16)
			return string(rune(n))
		case e == "n":
			return "\n"
		case e == "t":
			return "\t"
		case e == "\\":
			return "\\"
		}
		return " "
	})
}

func nameOf(word string) string {
	return strings.ToLower(word[strings.LastIndexByte(word, '/')+1:])
}
