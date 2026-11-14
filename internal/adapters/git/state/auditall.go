package state

import (
	"encoding/json"
	"path/filepath"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

var _ session.AuditAll = Audit{}

func (a Audit) ReadAll(limit int) ([]session.AuditEntry, error) {
	if a.mu != nil {
		a.mu.Lock()
		defer a.mu.Unlock()
	}
	if !ownDir(a.Dir) {
		return nil, errDir
	}
	var out []session.AuditEntry
	for _, name := range []string{auditFile, "audit.1.jsonl"} {
		lines, err := tail(filepath.Join(a.Dir, name))
		if err != nil {
			return nil, err
		}
		for i := len(lines) - 1; i >= 0 && len(out) < limit; i-- {
			var j auditJSON
			if json.Unmarshal(lines[i], &j) != nil {
				continue
			}
			kind := j.Kind
			if kind == "" {
				kind = session.AuditPush
			}
			out = append(out, entryOf(j, kind))
		}
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}
