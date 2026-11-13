package state

import "github.com/bezmoradi/software-factory/internal/core/session"

type holdJSON struct {
	AfterBlock bool `json:"afterBlock,omitempty"`
	MaxFiles   int  `json:"maxFiles,omitempty"`
	MaxLines   int  `json:"maxLines,omitempty"`
}

func holdOf(h *holdJSON) session.HoldSettings {
	if h == nil {
		return session.HoldSettings{}
	}
	out := session.HoldSettings{AfterBlock: h.AfterBlock, MaxFiles: h.MaxFiles, MaxLines: h.MaxLines}
	if !out.Valid() {
		return session.HoldSettings{AfterBlock: h.AfterBlock}
	}
	return out
}

func holdJSONOf(h session.HoldSettings) *holdJSON {
	if !h.On() {
		return nil
	}
	return &holdJSON{AfterBlock: h.AfterBlock, MaxFiles: h.MaxFiles, MaxLines: h.MaxLines}
}
