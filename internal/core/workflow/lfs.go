package workflow

import (
	"path"
	"slices"
	"strings"
)

const maxAttributeLines = 5000

type attrLine struct {
	dir      string
	pattern  string
	anchored bool
	filter   string
}

func attributeLines(dir string, text []byte) []attrLine {
	var out []attrLine
	macros := map[string]string{}
	for i, raw := range strings.Split(string(text), "\n") {
		if i >= maxAttributeLines {
			break
		}
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var pattern, rest string
		if strings.HasPrefix(line, "\"") {
			if end := strings.Index(line[1:], "\""); end >= 0 {
				pattern, rest = line[1:end+1], line[end+2:]
			} else {
				pattern = line[1:]
			}
		} else if i := strings.IndexAny(line, " \t"); i >= 0 {
			pattern, rest = line[:i], line[i+1:]
		} else {
			continue
		}
		filter := ""
		for word := range strings.FieldsSeq(rest) {
			switch {
			case word == "-filter" || word == "!filter":
				filter = "unset"
			case strings.HasPrefix(word, "filter="):
				filter = strings.TrimPrefix(word, "filter=")
			case macros[word] != "":
				filter = macros[word]
			}
		}
		if name, ok := strings.CutPrefix(pattern, "[attr]"); ok {
			if filter != "" {
				macros[name] = filter
			}
			continue
		}
		if filter == "" || strings.HasSuffix(pattern, "/") {
			continue
		}
		l := attrLine{dir: dir, pattern: pattern, filter: filter}
		if p, ok := strings.CutPrefix(pattern, "/"); ok {
			l.pattern, l.anchored = p, true
		} else if strings.Contains(pattern, "/") {
			l.anchored = true
		}
		out = append(out, l)
	}
	return out
}

func (l attrLine) matches(file string) bool {
	rel := file
	if l.dir != "" {
		var ok bool
		if rel, ok = strings.CutPrefix(file, l.dir+"/"); !ok {
			return false
		}
	}
	subject := rel
	if !l.anchored {
		subject = path.Base(rel)
	}
	re, ok := globRegexp(l.pattern, false)
	if !ok {
		return true
	}
	return re.MatchString(subject)
}

func LFSPaths(attributes map[string][]byte, files []string) []string {
	dirs := make([]string, 0, len(attributes))
	for d := range attributes {
		dirs = append(dirs, d)
	}
	slices.SortFunc(dirs, func(a, b string) int {
		return strings.Count(a, "/") - strings.Count(b, "/") + boolInt(a != "") - boolInt(b != "")
	})
	var lines []attrLine
	for _, d := range dirs {
		lines = append(lines, attributeLines(d, attributes[d])...)
	}
	var out []string
	for _, f := range files {
		filter := ""
		for _, l := range lines {
			if l.matches(f) {
				filter = l.filter
			}
		}
		if filter == "lfs" && !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	slices.Sort(out)
	return out
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
