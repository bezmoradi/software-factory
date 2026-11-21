package session

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

const (
	CoverageLinePrefix = "knackpack-coverage/1 "
	MaxCoverageLines   = 100
	MaxCoverageLine    = 1024
	MaxCoverageNote    = 200
	MaxRequirementIDs  = 100
)

const (
	CoverageVerified    = "verified"
	CoverageViolated    = "violated"
	CoverageNotVerified = "not-verified"
)

var (
	requirementID   = regexp.MustCompile(`^[A-Z][A-Z0-9]*-[0-9]{1,4}$`)
	requirementWord = regexp.MustCompile(`\b[A-Z][A-Z0-9]*-[0-9]{1,4}\b`)
	coverageSpot    = regexp.MustCompile(`^(.+?):([1-9][0-9]{0,7})(?:-([1-9][0-9]{0,7}))?$`)
)

type CoverageLine struct {
	ID       string
	Status   string
	Evidence string
	Path     string
	Line     int
	EndLine  int
	Note     string
}

type Coverage struct {
	Lines   []CoverageLine
	Dropped int
	Cut     bool
}

func RequirementID(id string) bool {
	return requirementID.MatchString(id)
}

func RequirementIDs(text string) []string {
	var out []string
	for _, at := range requirementWord.FindAllStringIndex(text, -1) {
		if len(out) >= MaxRequirementIDs {
			break
		}
		if partOfLongerID(text, at[0], at[1]) {
			continue
		}
		if id := text[at[0]:at[1]]; !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

func partOfLongerID(text string, start, end int) bool {
	return (start > 0 && text[start-1] == '-') || (end < len(text) && text[end] == '-')
}

func coverageRun(reply string) ([]string, bool) {
	lines := strings.Split(strings.ReplaceAll(reply, "\r\n", "\n"), "\n")
	var run []string
	inRun := false
	for _, line := range slices.Backward(lines) {
		t := strings.TrimSpace(line)
		switch {
		case t == "":
			continue
		case strings.HasPrefix(t, CoverageLinePrefix):
			inRun = true
			run = append(run, t)
			continue
		case !inRun && (strings.HasPrefix(t, FindingLinePrefix) || strings.HasPrefix(t, "knackpack-findings/") || strings.HasPrefix(t, "knackpack-verdict/")):
			continue
		}
		slices.Reverse(run)
		return run, t == clippedMiddle
	}
	slices.Reverse(run)
	return run, false
}

func CoverageLines(reply string, given []string) Coverage {
	run, cut := coverageRun(reply)
	out := Coverage{Cut: cut}
	for _, t := range run {
		if len(out.Lines) >= MaxCoverageLines || len(t) > MaxCoverageLine {
			out.Dropped++
			continue
		}
		c, ok := coverageLine(strings.TrimPrefix(t, CoverageLinePrefix))
		if !ok || !slices.Contains(given, c.ID) || slices.ContainsFunc(out.Lines, func(x CoverageLine) bool { return x.ID == c.ID }) {
			out.Dropped++
			continue
		}
		out.Lines = append(out.Lines, c)
	}
	return out
}

func coverageLine(text string) (CoverageLine, bool) {
	v, problem := workflow.JSONObject(text)
	if problem != "" {
		return CoverageLine{}, false
	}
	id, _ := v["id"].(string)
	status, _ := v["status"].(string)
	if !requirementID.MatchString(id) || (status != CoverageVerified && status != CoverageViolated && status != CoverageNotVerified) {
		return CoverageLine{}, false
	}
	c := CoverageLine{ID: id, Status: status}
	if raw, ok := v["evidence"]; ok {
		s, isStr := raw.(string)
		if !isStr || !c.at(s) {
			return CoverageLine{}, false
		}
	} else if status != CoverageNotVerified {
		return CoverageLine{}, false
	}
	if raw, ok := v["note"]; ok {
		s, isStr := raw.(string)
		if !isStr {
			return CoverageLine{}, false
		}
		c.Note = CleanLine(strings.Join(strings.Fields(s), " "), MaxCoverageNote)
	}
	return c, true
}

func (c *CoverageLine) at(evidence string) bool {
	m := coverageSpot.FindStringSubmatch(evidence)
	if m == nil {
		return false
	}
	p := m[1]
	if rest, ok := strings.CutPrefix(p, "head/"); ok {
		p = rest
	}
	if !RepoPath(p) {
		return false
	}
	line, err := strconv.Atoi(m[2])
	if err != nil {
		return false
	}
	end := 0
	if m[3] != "" {
		if end, err = strconv.Atoi(m[3]); err != nil || end < line {
			return false
		}
	}
	c.Path, c.Line, c.EndLine = p, line, end
	c.Evidence = p + ":" + m[2]
	if m[3] != "" {
		c.Evidence += "-" + m[3]
	}
	return true
}
