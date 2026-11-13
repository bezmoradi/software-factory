package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type finding struct {
	file string
	line int
	text string
}

type checker struct {
	file     string
	findings []finding
}

func (c *checker) report(line int, text string) {
	text = strings.TrimSpace(text)
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i] + " …"
	}
	if len(text) > 100 {
		text = text[:100] + " …"
	}
	c.findings = append(c.findings, finding{c.file, line, text})
}

var (
	pinnedRef       = regexp.MustCompile(`(\b[0-9a-f]{40}\b|@sha256:[0-9a-f]{64}\b)`)
	versionComment  = regexp.MustCompile(`^#[ \t]*v[0-9][^ \t]*[ \t]*$`)
	hashDirective   = regexp.MustCompile(`^#[ \t]*(zizmor:[ \t]*ignore\[|shellcheck[ \t]+(disable|enable|source|shell)=)`)
	goDirective     = regexp.MustCompile(`^//(go:[a-z]|nolint\b|line )|^// Code generated .* DO NOT EDIT\.$`)
	tsDirective     = regexp.MustCompile(`^(/// <reference |// ?@ts-(expect-error|ignore|nocheck|check)\b)`)
	dockerDirective = regexp.MustCompile(`^#[ \t]*(syntax|escape|check)[ \t]*=`)
)

func hashAllowed(line int, before, comment string) bool {
	if line == 1 && strings.HasPrefix(comment, "#!") && strings.TrimSpace(before) == "" {
		return true
	}
	if hashDirective.MatchString(comment) {
		return true
	}
	return versionComment.MatchString(strings.TrimSpace(comment)) && pinnedRef.MatchString(before)
}

func kindOf(name string) string {
	base := path.Base(name)
	switch {
	case base == "Dockerfile" || strings.HasPrefix(base, "Dockerfile."):
		return "Dockerfile"
	case base == "Makefile" || base == "GNUmakefile" || strings.HasSuffix(base, ".mk"):
		return "Makefile"
	}
	switch path.Ext(base) {
	case ".go":
		return "go"
	case ".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs":
		return "ts/js"
	case ".css":
		return "css"
	case ".html", ".htm":
		return "html"
	case ".sh":
		return "sh"
	case ".yml", ".yaml":
		return "yaml"
	case ".toml":
		return "toml"
	}
	return ""
}

func (c *checker) check(kind string, src []byte) {
	switch kind {
	case "go":
		c.goFile(src)
	case "ts/js":
		c.js(string(src), 1)
	case "css":
		c.css(string(src), 1)
	case "html":
		c.html(string(src))
	case "sh":
		c.shell(string(src), 1)
	case "yaml":
		c.yaml(string(src))
	case "Dockerfile":
		c.dockerfile(string(src))
	case "Makefile":
		c.makefile(string(src))
	case "toml":
		c.toml(string(src))
	}
}

func (c *checker) goFile(src []byte) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, c.file, src, parser.ParseComments)
	if err != nil {
		c.report(1, "can't parse: "+err.Error())
		return
	}
	for _, group := range f.Comments {
		for _, cm := range group.List {
			if goDirective.MatchString(cm.Text) {
				continue
			}
			c.report(fset.Position(cm.Slash).Line, cm.Text)
		}
	}
}

func lineAt(s string, i int) int { return strings.Count(s[:i], "\n") }

func (c *checker) js(s string, first int) {
	type frame struct{ template bool }
	var stack []frame
	prevSignificant := byte(0)
	prevWord := ""
	regexAllowed := func() bool {
		switch prevSignificant {
		case 0, '(', ',', '=', ':', '[', '!', '&', '|', '?', '{', '}', ';', '+', '-', '*', '%', '<', '>', '~', '^':
			return true
		case ')', ']':
			return false
		}
		if isIdent(prevSignificant) {
			switch prevWord {
			case "return", "typeof", "instanceof", "in", "of", "new", "delete", "void", "throw", "case", "do", "else", "yield", "await":
				return true
			}
			return false
		}
		return true
	}
	i := 0
	n := len(s)
	inTemplate := false
	for i < n {
		ch := s[i]
		if inTemplate {
			switch {
			case ch == '\\':
				i += 2
			case ch == '`':
				inTemplate = false
				prevSignificant, prevWord = '`', ""
				i++
			case ch == '$' && i+1 < n && s[i+1] == '{':
				stack = append(stack, frame{template: true})
				inTemplate = false
				prevSignificant, prevWord = '{', ""
				i += 2
			default:
				i++
			}
			continue
		}
		switch {
		case ch == '/' && i+1 < n && s[i+1] == '/':
			end := strings.IndexByte(s[i:], '\n')
			if end < 0 {
				end = n - i
			}
			text := s[i : i+end]
			if !tsDirective.MatchString(text) {
				c.report(first+lineAt(s, i), text)
			}
			i += end
		case ch == '/' && i+1 < n && s[i+1] == '*':
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				end = n - i - 2
			}
			c.report(first+lineAt(s, i), s[i:min(n, i+2+end+2)])
			i += 2 + end + 2
		case ch == '\'' || ch == '"':
			i = skipQuoted(s, i, ch)
			prevSignificant, prevWord = '"', ""
		case ch == '`':
			inTemplate = true
			i++
		case ch == '/' && regexAllowed():
			i = skipRegex(s, i)
			prevSignificant, prevWord = ')', ""
		case ch == '{':
			stack = append(stack, frame{})
			prevSignificant, prevWord = '{', ""
			i++
		case ch == '}':
			if len(stack) > 0 {
				top := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				if top.template {
					inTemplate = true
					i++
					continue
				}
			}
			prevSignificant, prevWord = '}', ""
			i++
		case ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r':
			i++
		case isIdent(ch):
			j := i
			for j < n && isIdent(s[j]) {
				j++
			}
			prevWord = s[i:j]
			prevSignificant = s[j-1]
			i = j
		default:
			prevSignificant, prevWord = ch, ""
			i++
		}
	}
}

func isIdent(b byte) bool {
	return b == '_' || b == '$' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= 0x80
}

func skipQuoted(s string, i int, q byte) int {
	i++
	for i < len(s) {
		switch s[i] {
		case '\\':
			i += 2
			continue
		case q:
			return i + 1
		case '\n':
			if q != '`' {
				return i
			}
		}
		i++
	}
	return i
}

func skipRegex(s string, i int) int {
	i++
	inClass := false
	for i < len(s) {
		switch s[i] {
		case '\\':
			i += 2
			continue
		case '[':
			inClass = true
		case ']':
			inClass = false
		case '/':
			if !inClass {
				i++
				for i < len(s) && isIdent(s[i]) {
					i++
				}
				return i
			}
		case '\n':
			return i
		}
		i++
	}
	return i
}

func (c *checker) css(s string, first int) {
	for i := 0; i < len(s); {
		switch {
		case s[i] == '"' || s[i] == '\'':
			i = skipQuoted(s, i, s[i])
		case s[i] == '/' && i+1 < len(s) && s[i+1] == '*':
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				end = len(s) - i - 2
			}
			c.report(first+lineAt(s, i), s[i:min(len(s), i+2+end+2)])
			i += 2 + end + 2
		default:
			i++
		}
	}
}

var htmlBlock = regexp.MustCompile(`(?is)<(script|style)\b[^>]*>(.*?)</(script|style)\s*>`)

func (c *checker) html(s string) {
	for i := 0; ; {
		j := strings.Index(s[i:], "<!--")
		if j < 0 {
			break
		}
		start := i + j
		end := strings.Index(s[start:], "-->")
		if end < 0 {
			end = len(s) - start
		}
		c.report(1+lineAt(s, start), s[start:min(len(s), start+end+3)])
		i = start + end
	}
	for _, m := range htmlBlock.FindAllStringSubmatchIndex(s, -1) {
		body := s[m[4]:m[5]]
		line := 1 + lineAt(s, m[4])
		if strings.EqualFold(s[m[2]:m[3]], "script") {
			c.js(body, line)
		} else {
			c.css(body, line)
		}
	}
}

var heredocStart = regexp.MustCompile(`^<<(-?)[ \t]*(['"]?)([A-Za-z_][A-Za-z0-9_]*)(['"]?)`)

func (c *checker) shell(s string, first int) {
	n := len(s)
	var heredocs []struct {
		word  string
		strip bool
	}
	atWordStart := true
	for i := 0; i < n; {
		ch := s[i]
		switch {
		case ch == '\n':
			i++
			atWordStart = true
			for len(heredocs) > 0 {
				h := heredocs[0]
				heredocs = heredocs[1:]
				for i < n {
					end := strings.IndexByte(s[i:], '\n')
					lineEnd := n
					if end >= 0 {
						lineEnd = i + end
					}
					l := s[i:lineEnd]
					if h.strip {
						l = strings.TrimLeft(l, "\t")
					}
					i = min(n, lineEnd+1)
					if l == h.word {
						break
					}
				}
			}
		case ch == '\\':
			i += 2
			atWordStart = false
		case ch == '\'':
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				i = n
			} else {
				i += j + 2
			}
			atWordStart = false
		case ch == '"' || ch == '`':
			i = skipShellDouble(s, i, ch)
			atWordStart = false
		case ch == '#' && atWordStart:
			end := strings.IndexByte(s[i:], '\n')
			if end < 0 {
				end = n - i
			}
			line := first + lineAt(s, i)
			lineStart := strings.LastIndexByte(s[:i], '\n') + 1
			if !hashAllowed(line, s[lineStart:i], s[i:i+end]) {
				c.report(line, s[i:i+end])
			}
			i += end
		case ch == '<' && strings.HasPrefix(s[i:], "<<") && !strings.HasPrefix(s[i:], "<<<"):
			if m := heredocStart.FindStringSubmatch(s[i:]); m != nil {
				heredocs = append(heredocs, struct {
					word  string
					strip bool
				}{m[3], m[1] == "-"})
				i += len(m[0])
			} else {
				i += 2
			}
			atWordStart = false
		case ch == ' ' || ch == '\t' || ch == ';' || ch == '&' || ch == '|' || ch == '(' || ch == ')':
			i++
			atWordStart = true
		default:
			i++
			atWordStart = false
		}
	}
}

func skipShellDouble(s string, i int, q byte) int {
	i++
	for i < len(s) {
		switch s[i] {
		case '\\':
			i += 2
			continue
		case q:
			return i + 1
		}
		i++
	}
	return i
}

var blockKey = regexp.MustCompile(`^([ \t]*)(?:-[ \t]+)*([A-Za-z0-9_.-]+|"[^"]*"|'[^']*'):[ \t]*[|>][-+0-9]*[ \t]*(#.*)?$`)

func indentOf(l string) int { return len(l) - len(strings.TrimLeft(l, " ")) }

func (c *checker) yaml(s string) {
	lines := strings.Split(s, "\n")
	quote := byte(0)
	for k := 0; k < len(lines); k++ {
		l := lines[k]
		if quote == 0 {
			if m := blockKey.FindStringSubmatchIndex(l); m != nil {
				keyCol := m[4]
				key := strings.Trim(l[m[4]:m[5]], `"'`)
				if m[6] >= 0 {
					c.yamlLine(k+1, l, &quote)
				}
				start := k + 1
				end := start
				for end < len(lines) && (strings.TrimSpace(lines[end]) == "" || indentOf(lines[end]) > keyCol) {
					end++
				}
				if key == "run" && end > start {
					c.shell(strings.Join(lines[start:end], "\n"), start+1)
				}
				k = end - 1
				continue
			}
		}
		c.yamlLine(k+1, l, &quote)
	}
}

func (c *checker) yamlLine(line int, l string, quote *byte) {
	scalarStart := true
	for i := 0; i < len(l); i++ {
		ch := l[i]
		if *quote != 0 {
			switch {
			case *quote == '"' && ch == '\\':
				i++
			case *quote == '\'' && ch == '\'' && i+1 < len(l) && l[i+1] == '\'':
				i++
			case ch == *quote:
				*quote = 0
				scalarStart = false
			}
			continue
		}
		switch {
		case ch == '#' && (i == 0 || l[i-1] == ' ' || l[i-1] == '\t'):
			if !hashAllowed(line, l[:i], l[i:]) {
				c.report(line, l[i:])
			}
			return
		case (ch == '"' || ch == '\'') && scalarStart:
			*quote = ch
		case ch == ' ' || ch == '\t':
		case ch == '-' && (i+1 == len(l) || l[i+1] == ' '):
			scalarStart = true
		case ch == ':' && (i+1 == len(l) || l[i+1] == ' ' || l[i+1] == '\t'):
			scalarStart = true
		case ch == '[' || ch == '{' || ch == ',' || ch == '?':
			scalarStart = true
		default:
			scalarStart = false
		}
	}
}

func (c *checker) dockerfile(s string) {
	lines := strings.Split(s, "\n")
	directives := true
	for k := 0; k < len(lines); k++ {
		t := strings.TrimSpace(lines[k])
		if strings.HasPrefix(t, "#") {
			if directives && dockerDirective.MatchString(t) {
				continue
			}
			directives = false
			c.report(k+1, t)
			continue
		}
		directives = false
		if !strings.HasPrefix(strings.ToUpper(t), "RUN ") {
			continue
		}
		start := k
		var body []string
		for {
			l := lines[k]
			if k == start {
				l = l[strings.Index(strings.ToUpper(l), "RUN ")+4:]
			}
			comment := k > start && strings.HasPrefix(strings.TrimSpace(l), "#")
			if comment {
				c.report(k+1, strings.TrimSpace(l))
				l = ""
			}
			body = append(body, l)
			if k+1 >= len(lines) || !comment && !strings.HasSuffix(strings.TrimRight(lines[k], " \t"), "\\") {
				break
			}
			k++
		}
		c.shell(strings.Join(body, "\n"), start+1)
	}
}

func (c *checker) makefile(s string) {
	lines := strings.Split(s, "\n")
	recipe := false
	for k := 0; k < len(lines); k++ {
		l := lines[k]
		if strings.HasPrefix(l, "\t") && recipe {
			start := k
			body := []string{strings.TrimLeft(strings.TrimPrefix(l, "\t"), "@-+")}
			for strings.HasSuffix(lines[k], "\\") && k+1 < len(lines) {
				k++
				body = append(body, strings.TrimPrefix(lines[k], "\t"))
			}
			c.shell(strings.Join(body, "\n"), start+1)
			continue
		}
		start := k
		for strings.HasSuffix(lines[k], "\\") && k+1 < len(lines) {
			k++
		}
		logical := strings.Join(lines[start:k+1], "\n")
		for i := 0; i < len(logical); i++ {
			if logical[i] == '\\' {
				i++
				continue
			}
			if logical[i] == '#' {
				line := start + 1 + lineAt(logical, i)
				ls := strings.LastIndexByte(logical[:i], '\n') + 1
				if !hashAllowed(line, logical[ls:i], logical[i:]) {
					c.report(line, logical[i:])
				}
				logical = logical[:i]
				break
			}
		}
		t := strings.TrimSpace(logical)
		switch {
		case t == "":
		case strings.Contains(t, ":") && !strings.Contains(t, ":=") && !strings.Contains(t, "?=") && !strings.Contains(t, "+=") && !strings.Contains(t, "::="):
			recipe = true
		default:
			recipe = false
		}
	}
}

func (c *checker) toml(s string) {
	for i := 0; i < len(s); {
		switch {
		case strings.HasPrefix(s[i:], `"""`):
			j := strings.Index(s[i+3:], `"""`)
			if j < 0 {
				return
			}
			i += 3 + j + 3
		case strings.HasPrefix(s[i:], `'''`):
			j := strings.Index(s[i+3:], `'''`)
			if j < 0 {
				return
			}
			i += 3 + j + 3
		case s[i] == '"':
			i = skipQuoted(s, i, '"')
		case s[i] == '\'':
			j := strings.IndexAny(s[i+1:], "'\n")
			if j < 0 {
				return
			}
			i += j + 2
		case s[i] == '#':
			end := strings.IndexByte(s[i:], '\n')
			if end < 0 {
				end = len(s) - i
			}
			line := 1 + lineAt(s, i)
			ls := strings.LastIndexByte(s[:i], '\n') + 1
			if !hashAllowed(line, s[ls:i], s[i:i+end]) {
				c.report(line, s[i:i+end])
			}
			i += end
		default:
			i++
		}
	}
}

func main() {
	root := flag.String("root", ".", "the repository's root; file names on stdin are relative to it")
	summary := flag.Bool("summary", false, "print only the number of comments per file type")
	flag.Parse()

	names, err := io.ReadAll(bufio.NewReader(os.Stdin))
	if err != nil {
		fmt.Fprintln(os.Stderr, "no-comments:", err)
		os.Exit(2)
	}
	var all []finding
	counts := map[string]int{}
	files := map[string]int{}
	checked := 0
	for _, name := range bytes.Split(names, []byte{0}) {
		if len(name) == 0 {
			continue
		}
		kind := kindOf(string(name))
		if kind == "" {
			continue
		}
		src, err := os.ReadFile(filepath.Join(*root, string(name)))
		if err != nil {
			fmt.Fprintln(os.Stderr, "no-comments:", err)
			os.Exit(2)
		}
		checked++
		c := &checker{file: string(name)}
		c.check(kind, src)
		if len(c.findings) > 0 {
			counts[kind] += len(c.findings)
			files[kind]++
		}
		all = append(all, c.findings...)
	}
	if checked == 0 {
		fmt.Fprintln(os.Stderr, "no-comments: no files to check")
		os.Exit(2)
	}
	if !*summary {
		for _, f := range all {
			fmt.Printf("%s:%d: %s\n", f.file, f.line, f.text)
		}
	}
	if len(all) == 0 {
		fmt.Printf("No comments in the %d files checked.\n", checked)
		return
	}
	kinds := make([]string, 0, len(counts))
	for k := range counts {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		fmt.Printf("%-10s %5d comments in %d files\n", k, counts[k], files[k])
	}
	fmt.Printf("%d comments in %d files checked: the repository has no comments (CLAUDE.md), only version comments after pinned references and tool directives.\n", len(all), checked)
	os.Exit(1)
}
