package session

import (
	"cmp"
	"slices"
)

const maxApprovedTexts = 1 << 20

func keepApprovedTexts(sessions map[string]SavedWork, current string) {
	type kept struct {
		sid  string
		size int
		at   int64
	}
	var list []kept
	total := 0
	for sid, v := range sessions {
		if v.Requirements == nil || v.Requirements.Text == "" {
			continue
		}
		n := len(v.Requirements.Text)
		total += n
		list = append(list, kept{sid: sid, size: n, at: v.Requirements.At.UnixNano()})
	}
	slices.SortFunc(list, func(x, y kept) int {
		return cmp.Or(cmp.Compare(boolRank(x.sid == current), boolRank(y.sid == current)), cmp.Compare(x.at, y.at))
	})
	for _, k := range list {
		if total <= maxApprovedTexts {
			return
		}
		v := sessions[k.sid]
		a := *v.Requirements
		a.Text = ""
		v.Requirements = &a
		sessions[k.sid] = v
		total -= k.size
	}
}

func boolRank(b bool) int {
	if b {
		return 1
	}
	return 0
}
