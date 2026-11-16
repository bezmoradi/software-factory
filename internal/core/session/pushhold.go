package session

import (
	"context"
	"strconv"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/review"
	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

const (
	MaxHoldFiles = 100000
	MaxHoldLines = 10000000
)

type HoldSettings struct {
	AfterBlock bool
	MaxFiles   int
	MaxLines   int
}

func (h HoldSettings) On() bool {
	return h.AfterBlock || h.MaxFiles > 0 || h.MaxLines > 0
}

func (h HoldSettings) Valid() bool {
	return h.MaxFiles >= 0 && h.MaxFiles <= MaxHoldFiles && h.MaxLines >= 0 && h.MaxLines <= MaxHoldLines
}

func (p *Pushes) holdItems(ctx context.Context, t PushTarget, h HoldSettings, entries map[review.Key]ReviewEntry, required []workflow.Requirement, head, main string) []string {
	if !h.On() {
		return nil
	}
	var out []string
	if h.AfterBlock {
		for _, q := range required {
			if RoundOf(entries, t.Branch, q.Agent, false).Blocked > 0 {
				out = append(out, "the person's Push: this project holds the agent's own push after a blocked review round, and "+q.Agent+" blocked on this branch")
				break
			}
		}
	}
	if h.MaxFiles <= 0 && h.MaxLines <= 0 {
		return out
	}
	files, lines, clipped, ok := p.changeSize(ctx, t, head, main)
	limit := sizeLimit(h)
	switch {
	case !ok:
		out = append(out, "the person's Push: this project holds the agent's own push above "+limit+", and the engine couldn't count this change")
	case (h.MaxFiles > 0 && files > h.MaxFiles) || (h.MaxLines > 0 && (lines > h.MaxLines || clipped)):
		size := strconv.Itoa(files) + " " + plural(files, "file", "files") + " and " + strconv.Itoa(lines) + " changed " + plural(lines, "line", "lines")
		if clipped {
			size = strconv.Itoa(files) + " " + plural(files, "file", "files") + " and more changed lines than the engine counts"
		}
		out = append(out, "the person's Push: this project holds the agent's own push above "+limit+", and this change has "+size)
	}
	return out
}

func sizeLimit(h HoldSettings) string {
	var parts []string
	if h.MaxFiles > 0 {
		parts = append(parts, strconv.Itoa(h.MaxFiles)+" changed "+plural(h.MaxFiles, "file", "files"))
	}
	if h.MaxLines > 0 {
		parts = append(parts, strconv.Itoa(h.MaxLines)+" changed "+plural(h.MaxLines, "line", "lines"))
	}
	return strings.Join(parts, " or ")
}

func (p *Pushes) changeSize(ctx context.Context, t PushTarget, head, main string) (int, int, bool, bool) {
	base := ""
	if main != "" {
		var err error
		if base, err = p.cfg.History.MergeBase(ctx, t.Repo, head, main); err != nil {
			return 0, 0, false, false
		}
	}
	cs, err := p.cfg.Changes.Changes(ctx, t.Repo, base, head)
	if err != nil {
		return 0, 0, false, false
	}
	return len(cs.Files), changedLines(cs.Diff) + int(min(cs.UndiffedLines, maxCountedLines)), cs.Clipped || cs.Uncounted, true
}

const maxCountedLines = 1 << 40

func changedLines(diff string) int {
	n := 0
	inHunk := false
	for line := range strings.SplitSeq(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			inHunk = false
		case strings.HasPrefix(line, "@@ "):
			inHunk = true
		case inHunk && (strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-")):
			n++
		}
	}
	return n
}
