package page

import "github.com/bezmoradi/software-factory/internal/core/session"

type readinessJSON struct {
	Kind  string   `json:"kind"`
	State string   `json:"state"`
	Found []string `json:"found"`
	Note  string   `json:"note,omitempty"`

	Commit string `json:"commit,omitempty"`
	At     int64  `json:"at,omitempty"`
	Result string `json:"result,omitempty"`
	Code   int    `json:"code,omitempty"`
	Tail   string `json:"tail,omitempty"`
}

func readinessOf(list []session.ReadinessLine) []readinessJSON {
	out := make([]readinessJSON, 0, len(list))
	for _, l := range list {
		out = append(out, readinessJSON{Kind: l.Kind, State: l.State, Found: nonNil(l.Found), Note: l.Note,
			Commit: l.Commit, At: millis(l.At), Result: l.Result, Code: l.Code, Tail: l.Tail})
	}
	return out
}
