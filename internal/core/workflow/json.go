package workflow

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

type object struct {
	keys   []string
	values map[string]any
}

type jsonError struct{ why string }

type parser struct {
	text  string
	i     int
	depth int
}

var jsonNumber = regexp.MustCompile(`^-?(0|[1-9]\d{0,15})(\.\d{1,15})?([eE][+-]?\d{1,3})?`)

func hiddenRune(c rune) bool {
	switch {
	case c <= 0x08, c == 0x0b, c == 0x0c, c >= 0x0e && c <= 0x1f, c >= 0x7f && c <= 0x9f:
		return true
	case c == 0xad, c == 0x61c, c == 0x180e, c >= 0x200b && c <= 0x200f, c >= 0x2028 && c <= 0x202e:
		return true
	case c >= 0x2060 && c <= 0x2069, c >= 0xfe00 && c <= 0xfe0f, c == 0xfeff, c >= 0xe0000 && c <= 0xe007f:
		return true
	}
	return false
}

func strictJSON(text string) (v any, problem string) {
	p := &parser{text: text}
	defer func() {
		if r := recover(); r != nil {
			e, ok := r.(jsonError)
			if !ok {
				panic(r)
			}
			v, problem = nil, e.why
		}
	}()
	if !utf8.ValidString(text) {
		p.fail("a byte that isn't UTF-8")
	}
	v = p.value()
	p.space()
	if p.i != len(text) {
		p.fail("text after the end")
	}
	return v, ""
}

func (p *parser) fail(why string) {
	panic(jsonError{why + " at character " + strconv.Itoa(p.i)})
}

func (p *parser) space() {
	for p.i < len(p.text) && strings.IndexByte(" \t\n\r", p.text[p.i]) >= 0 {
		p.i++
	}
}

func (p *parser) next() byte {
	if p.i >= len(p.text) {
		return 0
	}
	c := p.text[p.i]
	p.i++
	return c
}

func (p *parser) str() string {
	if p.i >= len(p.text) || p.text[p.i] != '"' {
		p.fail("expected a string")
	}
	p.i++
	var out strings.Builder
	for p.i < len(p.text) {
		c := p.text[p.i]
		p.i++
		switch {
		case c == '"':
			s := out.String()
			for _, r := range s {
				if hiddenRune(r) {
					p.fail("an invisible or direction-changing character in a string")
				}
			}
			return s
		case c == '\\':
			p.escape(&out)
		case c < ' ':
			p.fail("a control character in a string")
		default:
			out.WriteByte(c)
		}
	}
	p.fail("an unterminated string")
	return ""
}

func (p *parser) escape(out *strings.Builder) {
	e := p.next()
	switch e {
	case '"', '\\', '/':
		out.WriteByte(e)
	case 'b':
		out.WriteByte('\b')
	case 'f':
		out.WriteByte('\f')
	case 'n':
		out.WriteByte('\n')
	case 'r':
		out.WriteByte('\r')
	case 't':
		out.WriteByte('\t')
	case 'u':
		r := p.hex4()
		if utf16.IsSurrogate(r) {
			if !strings.HasPrefix(p.text[p.i:], `\u`) {
				p.fail("a bad \\u escape")
			}
			p.i += 2
			r = utf16.DecodeRune(r, p.hex4())
			if r == utf8.RuneError {
				p.fail("a bad \\u escape")
			}
		}
		out.WriteRune(r)
	default:
		p.fail("a bad escape")
	}
}

func (p *parser) hex4() rune {
	if p.i+4 > len(p.text) {
		p.fail("a bad \\u escape")
	}
	n, err := strconv.ParseUint(p.text[p.i:p.i+4], 16, 32)
	if err != nil {
		p.fail("a bad \\u escape")
	}
	p.i += 4
	return rune(n)
}

func (p *parser) value() any {
	p.space()
	p.depth++
	if p.depth > 16 {
		p.fail("nesting deeper than 16")
	}
	var out any
	rest := p.text[p.i:]
	switch {
	case strings.HasPrefix(rest, "{"):
		out = p.object()
	case strings.HasPrefix(rest, "["):
		out = p.array()
	case strings.HasPrefix(rest, `"`):
		out = p.str()
	case strings.HasPrefix(rest, "true"):
		p.i += 4
		out = true
	case strings.HasPrefix(rest, "false"):
		p.i += 5
		out = false
	case strings.HasPrefix(rest, "null"):
		p.i += 4
		out = nil
	default:
		m := jsonNumber.FindString(rest[:min(len(rest), 40)])
		if m == "" {
			p.fail("an unexpected character")
		}
		n, err := strconv.ParseFloat(m, 64)
		if err != nil {
			p.fail("a number out of range")
		}
		p.i += len(m)
		out = n
	}
	p.depth--
	return out
}

func (p *parser) object() *object {
	p.i++
	o := &object{values: map[string]any{}}
	p.space()
	if p.i < len(p.text) && p.text[p.i] == '}' {
		p.i++
		return o
	}
	for {
		p.space()
		k := p.str()
		if _, dup := o.values[k]; dup {
			p.fail("the key \"" + clip(k, 40) + "\" twice")
		}
		p.space()
		if p.next() != ':' {
			p.fail("expected a colon")
		}
		o.keys = append(o.keys, k)
		o.values[k] = p.value()
		p.space()
		switch p.next() {
		case '}':
			return o
		case ',':
		default:
			p.fail("expected a comma or }")
		}
	}
}

func (p *parser) array() []any {
	p.i++
	out := []any{}
	p.space()
	if p.i < len(p.text) && p.text[p.i] == ']' {
		p.i++
		return out
	}
	for {
		out = append(out, p.value())
		p.space()
		switch p.next() {
		case ']':
			return out
		case ',':
		default:
			p.fail("expected a comma or ]")
		}
	}
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
