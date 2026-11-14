package page

import "github.com/bezmoradi/software-factory/internal/core/session"

type readinessJSON struct {
	Kind  string   `json:"kind"`
	State string   `json:"state"`
	Found []string `json:"found"`
	Note  string   `json:"note,omitempty"`
}

func readinessOf(list []session.ReadinessLine) []readinessJSON {
	out := make([]readinessJSON, 0, len(list))
	for _, l := range list {
		out = append(out, readinessJSON{Kind: l.Kind, State: l.State, Found: nonNil(l.Found), Note: l.Note})
	}
	return out
}
