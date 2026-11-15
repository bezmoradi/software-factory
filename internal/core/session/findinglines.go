package session

import (
	"math"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/review"
	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

const (
	FindingLinePrefix = "knackpack-finding/1 "
	MaxFindingLines   = 200
	MaxFindingLine    = 4096
	MaxFindingTitle   = 200
	MaxRepoPath       = 300
	MaxFindingRefs    = 40
	maxFindingLineNo  = 10000000
)

var (
	findingID       = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._-]{0,19}$`)
	findingCategory = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	findingSpot     = regexp.MustCompile(`^(.+?):(\d{1,8})(?:-(\d{1,8}))?$`)
	findingSeverity = map[string]string{"critical": "Critical", "high": "High", "medium": "Medium", "low": "Low"}
	findingEvidence = map[string]bool{"confirmed": true, "conditional": true, "unproven": true}
)

type FindingLine struct {
	ID       string
	Severity string
	Blocking bool
	Path     string
	Line     int
	EndLine  int
	Title    string
}

func (f FindingLine) Location() string {
	if f.Path == "" {
		return ""
	}
	if f.Line == 0 {
		return f.Path
	}
	loc := f.Path + ":" + strconv.Itoa(f.Line)
	if f.EndLine > f.Line {
		loc += "-" + strconv.Itoa(f.EndLine)
	}
	return loc
}

func RepoPath(p string) bool {
	if p == "" || len(p) > MaxRepoPath || !utf8.ValidString(p) || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "~") || path.Clean(p) != p {
		return false
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f || r == '\\' || r == ':' || escaped(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	for seg := range strings.SplitSeq(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

func FindingLines(reply string) []FindingLine {
	lines := strings.Split(strings.ReplaceAll(reply, "\r\n", "\n"), "\n")
	end := len(lines)
	for i, line := range slices.Backward(lines) {
		if strings.HasPrefix(strings.TrimSpace(line), "knackpack-verdict/") {
			end = i
			break
		}
	}
	var out []FindingLine
	for _, l := range lines[:end] {
		t := strings.TrimSpace(l)
		rest, ok := strings.CutPrefix(t, FindingLinePrefix)
		if !ok || len(t) > MaxFindingLine {
			continue
		}
		if len(out) >= MaxFindingLines {
			break
		}
		f, ok := findingLine(rest)
		if !ok || slices.ContainsFunc(out, func(x FindingLine) bool { return x.ID == f.ID }) {
			continue
		}
		out = append(out, f)
	}
	return out
}

func findingLine(text string) (FindingLine, bool) {
	v, problem := workflow.JSONObject(text)
	if problem != "" {
		return FindingLine{}, false
	}
	id, _ := v["id"].(string)
	sev, _ := v["severity"].(string)
	blocking, isBool := v["blocking"].(bool)
	title, _ := v["title"].(string)
	title = strings.TrimSpace(title)
	if !findingID.MatchString(id) || findingSeverity[sev] == "" || !isBool || title == "" || utf8.RuneCountInString(title) > MaxFindingTitle || strings.ContainsAny(title, "\n\r") {
		return FindingLine{}, false
	}
	f := FindingLine{ID: id, Severity: findingSeverity[sev], Blocking: blocking, Title: CleanLine(title, MaxFindingTitle)}
	if p, ok := v["path"]; ok {
		s, isStr := p.(string)
		if !isStr || !RepoPath(s) {
			return FindingLine{}, false
		}
		f.Path = s
	}
	for _, k := range []string{"line", "endLine"} {
		raw, ok := v[k]
		if !ok {
			continue
		}
		n, isNum := raw.(float64)
		if !isNum || n != math.Trunc(n) || n < 1 || n > maxFindingLineNo {
			return FindingLine{}, false
		}
		if k == "line" {
			f.Line = int(n)
		} else {
			f.EndLine = int(n)
		}
	}
	if (f.EndLine > 0 && (f.Line == 0 || f.EndLine < f.Line)) || (f.Line > 0 && f.Path == "") {
		return FindingLine{}, false
	}
	if c, ok := v["category"]; ok {
		s, isStr := c.(string)
		if !isStr || len(s) > 40 || !findingCategory.MatchString(s) {
			return FindingLine{}, false
		}
	}
	if e, ok := v["evidence"]; ok {
		s, isStr := e.(string)
		if !isStr || !findingEvidence[s] {
			return FindingLine{}, false
		}
	}
	return f, true
}

func AgreedFindingLines(reply string) []FindingLine {
	lines := FindingLines(reply)
	counts, ok := review.CountsLine(reply)
	if len(lines) == 0 {
		return lines
	}
	if !ok {
		if !review.ClosingPass(reply) {
			return lines
		}
		return slices.DeleteFunc(lines, func(f FindingLine) bool { return f.Blocking || f.Severity == "Critical" || f.Severity == "High" })
	}
	seen := map[string]int{}
	out := lines[:0:0]
	for _, f := range lines {
		if seen[f.Severity] >= counts.Of(f.Severity) {
			continue
		}
		seen[f.Severity]++
		if counts.Blocking == 0 {
			f.Blocking = false
		}
		out = append(out, f)
	}
	return out
}

func namesID(title, id string) bool {
	for w := range strings.FieldsSeq(title) {
		if strings.Trim(w, "[]():.,;`*_#") == id {
			return true
		}
	}
	return false
}

func withFindingLines(items []FindingItem, lines []FindingLine, severities ...string) []FindingItem {
	var picked []FindingLine
	for _, f := range lines {
		if slices.Contains(severities, f.Severity) {
			picked = append(picked, f)
		}
	}
	if len(picked) == 0 {
		return items
	}
	matched := make([]bool, len(items))
	find := func(same func(FindingItem) bool) int {
		for i, it := range items {
			if !matched[i] && same(it) {
				return i
			}
		}
		return -1
	}
	var left []FindingLine
	for _, f := range picked {
		i := find(func(it FindingItem) bool {
			return it.Severity == f.Severity && normalTitle(it.Title) == normalTitle(f.Title)
		})
		if i < 0 {
			i = find(func(it FindingItem) bool { return it.Severity == f.Severity && namesID(it.Title, f.ID) })
		}
		if i < 0 {
			left = append(left, f)
			continue
		}
		matched[i] = true
		if loc := f.Location(); loc != "" {
			items[i].Location = loc
		}
	}
	var open []int
	for i, m := range matched {
		if !m {
			open = append(open, i)
		}
	}
	switch {
	case len(left) > 0 && len(left) == len(open):
		for k, i := range open {
			if loc := left[k].Location(); loc != "" && items[i].Severity == left[k].Severity {
				items[i].Location = loc
			}
		}
	case len(open) == 0:
		for _, f := range left {
			if len(items) >= maxFindingItems {
				break
			}
			items = append(items, FindingItem{Severity: f.Severity, Title: f.Title, Location: f.Location()})
		}
	}
	return items
}

type FindingRef struct {
	Severity string
	Title    string
	Where    string
	Path     string
	Line     int
}

func FindingRefs(reply string) []FindingRef {
	var out []FindingRef
	for _, it := range append(BlockingFindings(reply), NonBlockingFindings(reply)...) {
		if len(out) >= MaxFindingRefs {
			break
		}
		ref := FindingRef{Severity: it.Severity, Title: it.Title, Where: it.Location}
		ref.Path, ref.Line = spotOf(it.Location)
		out = append(out, ref)
	}
	return out
}

func spotOf(loc string) (string, int) {
	loc = strings.TrimSpace(strings.Trim(loc, "`*_ "))
	if i := strings.IndexAny(loc, ",; "); i >= 0 {
		loc = loc[:i]
	}
	loc = strings.Trim(loc, "`*_()")
	m := findingSpot.FindStringSubmatch(loc)
	if m == nil {
		if RepoPath(loc) && strings.ContainsAny(loc, "./") {
			return loc, 0
		}
		return "", 0
	}
	n, err := strconv.Atoi(m[2])
	if err != nil || n < 1 || n > maxFindingLineNo || !RepoPath(m[1]) {
		return "", 0
	}
	return m[1], n
}
