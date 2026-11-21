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

type savedSaid struct {
	Message string `json:"message,omitempty"`
	Tone    string `json:"tone,omitempty"`
}

func saveSaid(changed bool, saved, unchanged string) savedSaid {
	if changed {
		return savedSaid{Message: saved, Tone: "ok"}
	}
	return savedSaid{Message: unchanged, Tone: "warn"}
}

func (s savedSaid) on(answer map[string]any) map[string]any {
	answer["message"], answer["tone"] = s.Message, s.Tone
	return answer
}

func (s savedSaid) ok() map[string]any {
	return s.on(map[string]any{"ok": true})
}
