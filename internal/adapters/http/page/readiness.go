package page

import "github.com/bezmoradi/software-factory/internal/core/session"

type readinessJSON struct {
	Kind  string   `json:"kind"`
	State string   `json:"state"`
	Found []string `json:"found"`
	Value string   `json:"value,omitempty"`
	Note  string   `json:"note,omitempty"`
	FixIn string   `json:"fixIn,omitempty"`

	Commit string `json:"commit,omitempty"`
	At     int64  `json:"at,omitempty"`
	Result string `json:"result,omitempty"`
	Code   int    `json:"code,omitempty"`
	Tail   string `json:"tail,omitempty"`

	Label string `json:"label"`
	Word  string `json:"word"`
	Tone  string `json:"tone"`
}

type protectionRowJSON struct {
	Kind   string   `json:"kind"`
	Label  string   `json:"label"`
	State  string   `json:"state"`
	Value  string   `json:"value"`
	Detail string   `json:"detail,omitempty"`
	Names  []string `json:"names"`
	Tone   string   `json:"tone"`
}

func readinessOf(list []session.ReadinessLine) []readinessJSON {
	out := make([]readinessJSON, 0, len(list))
	for _, l := range list {
		said := session.ReadinessSaid(l.Kind, l.State)
		out = append(out, readinessJSON{Kind: l.Kind, State: l.State, Found: nonNil(l.Found), Value: l.Value, Note: l.Note, FixIn: l.FixIn,
			Commit: l.Commit, At: millis(l.At), Result: l.Result, Code: l.Code, Tail: l.Tail, Label: said.Label, Word: session.ReadinessWord(l.State), Tone: said.Tone})
	}
	return out
}

func protectionRowsOf(p session.Protection) []protectionRowJSON {
	rows := session.ProtectionRows(p)
	out := make([]protectionRowJSON, 0, len(rows))
	for _, r := range rows {
		out = append(out, protectionRowJSON{Kind: r.Kind, Label: r.Label, State: r.State, Value: r.Value, Detail: r.Detail, Names: nonNil(r.Names), Tone: session.ReadinessSaid("", r.State).Tone})
	}
	return out
}
