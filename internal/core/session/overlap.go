package session

import (
	"slices"
	"strings"
)

const maxOverlapShown = 50

type Overlap struct {
	SID   string
	Name  string
	Paths []string
	More  int
}

func (r *Registry) overlapsLocked(b *worktreeBook, sid string) []Overlap {
	mine, ok := b.changed[sid]
	if !ok || len(mine.paths) == 0 {
		return nil
	}
	var out []Overlap
	for other, theirs := range b.changed {
		if other == sid || theirs.repo != mine.repo || r.admitted[other] == nil {
			continue
		}
		var shared []string
		for p := range mine.paths {
			if theirs.paths[p] {
				shared = append(shared, p)
			}
		}
		if len(shared) == 0 {
			continue
		}
		slices.Sort(shared)
		o := Overlap{SID: other, Name: sessionLabel(r.admitted[other].Admission, other)}
		if len(shared) > maxOverlapShown {
			o.More = len(shared) - maxOverlapShown
			shared = shared[:maxOverlapShown]
		}
		for i, p := range shared {
			shared[i] = shownPath(p)
		}
		o.Paths = shared
		out = append(out, o)
	}
	slices.SortFunc(out, func(a, b Overlap) int { return strings.Compare(a.Name+a.SID, b.Name+b.SID) })
	return out
}

func shownPath(p string) string {
	return maskLines(p, true)
}

func sessionLabel(a Admission, sid string) string {
	switch {
	case strings.TrimSpace(a.Name) != "":
		return clip(strings.TrimSpace(a.Name), 80)
	case a.ID != "":
		return a.ID
	}
	return shortSHA(sid)
}
