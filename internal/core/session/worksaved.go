package session

import (
	"maps"
	"slices"
)

func savedApproval(a *workApproval) *SavedApproval {
	if a == nil {
		return nil
	}
	return &SavedApproval{Hash: a.Hash, Commit: a.Commit, At: a.At, Text: a.Text}
}

func workApprovalOf(a *SavedApproval) *workApproval {
	if a == nil {
		return nil
	}
	v := &workApproval{Hash: a.Hash, Commit: a.Commit, At: a.At}
	if a.Text != "" && textHash(a.Text) == a.Hash {
		v.Text = a.Text
	}
	return v
}

func (w *Work) saved(sid string) *SavedWork {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.sessions[sid]
	if s == nil {
		return nil
	}
	v := SavedWork{Repo: s.repo, Claude: s.claude, Type: s.typeID, Chosen: s.chosen, Requirements: savedApproval(s.req), Plan: savedApproval(s.plan)}
	if len(s.allowed) > 0 {
		v.Allowed = map[string][]string{}
		for p, set := range s.allowed {
			v.Allowed[p] = slices.Sorted(maps.Keys(set))
		}
	}
	return &v
}

func (w *Work) persist(sid string) {
	err := w.change(func(st *WorkState) error {
		if v := w.saved(sid); v != nil {
			st.Sessions[sid] = *v
		} else {
			delete(st.Sessions, sid)
		}
		keepApprovedTexts(st.Sessions, sid)
		return nil
	})
	if err != nil {
		w.cfg.Logger.Warn("work", "event", "session-not-saved")
	}
}

func (w *Work) Restore(sids []string) {
	keep := map[string]bool{}
	for _, sid := range sids {
		keep[sid] = true
	}
	st := w.load()
	w.mu.Lock()
	for sid, v := range st.Sessions {
		if !keep[sid] || w.sessions[sid] != nil {
			continue
		}
		s := &workSession{repo: v.Repo, claude: v.Claude, typeID: v.Type, chosen: v.Chosen, req: workApprovalOf(v.Requirements), plan: workApprovalOf(v.Plan)}
		if len(v.Allowed) > 0 {
			s.allowed = map[string]map[string]bool{}
			for p, blobs := range v.Allowed {
				set := map[string]bool{}
				for _, b := range blobs {
					set[b] = true
				}
				s.allowed[p] = set
			}
		}
		w.sessions[sid] = s
	}
	w.mu.Unlock()
	err := w.change(func(st *WorkState) error {
		maps.DeleteFunc(st.Sessions, func(sid string, _ SavedWork) bool { return !keep[sid] })
		return nil
	})
	if err != nil {
		w.cfg.Logger.Warn("work", "event", "sessions-not-pruned")
	}
}
