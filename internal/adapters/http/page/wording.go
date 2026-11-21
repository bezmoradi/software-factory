package page

import "github.com/bezmoradi/software-factory/internal/core/session"

type saidJSON struct {
	Label string `json:"label"`
	Tone  string `json:"tone"`
}

func saidJSONOf(s session.Said) saidJSON {
	return saidJSON{Label: s.Label, Tone: s.Tone}
}

func saidPtr(s *session.Said) *saidJSON {
	if s == nil {
		return nil
	}
	out := saidJSONOf(*s)
	return &out
}
