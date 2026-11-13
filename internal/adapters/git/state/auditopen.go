package state

import (
	"encoding/json"
	"path/filepath"

	"github.com/bezmoradi/software-factory/internal/core/gate"
	"github.com/bezmoradi/software-factory/internal/core/session"
)

var _ session.AuditOpen = Audit{}

func openResult(kind, result string) bool {
	switch kind {
	case session.AuditPush:
		return result == string(gate.Allowed) || result == session.ResultUnclear
	case string(session.ForgeIssue), string(session.ForgePull):
		return result == session.ForgeHeld || result == session.ResultSending
	}
	return false
}

func (a Audit) Open() ([]session.AuditEntry, error) {
	if a.mu != nil {
		a.mu.Lock()
		defer a.mu.Unlock()
	}
	if !ownDir(a.Dir) {
		return nil, errDir
	}
	last := map[string]session.AuditEntry{}
	var order []string
	for _, name := range []string{"audit.1.jsonl", auditFile} {
		lines, err := tail(filepath.Join(a.Dir, name))
		if err != nil {
			return nil, err
		}
		for _, line := range lines {
			var j auditJSON
			if json.Unmarshal(line, &j) != nil {
				continue
			}
			request := matching(j.Request, requestID)
			if request == "" || !sessionID.MatchString(j.Session) {
				continue
			}
			kind := j.Kind
			if kind == "" {
				kind = session.AuditPush
			}
			if _, seen := last[request]; !seen {
				order = append(order, request)
			}
			last[request] = entryOf(j, kind)
		}
	}
	var out []session.AuditEntry
	for _, request := range order {
		if e := last[request]; openResult(e.Kind, e.Result) {
			out = append(out, e)
		}
	}
	return out, nil
}
