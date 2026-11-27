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

func workApprovalOf(a *SavedApproval, kept KeptText) *workApproval {
	if a == nil || kept.Hash != a.Hash || kept.Text == "" || textHash(kept.Text) != a.Hash {
		return nil
	}
	return &workApproval{Hash: a.Hash, Commit: a.Commit, At: a.At, Text: kept.Text}
}

func (w *Work) saved(sid string) *SavedWork {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.sessions[sid]
	if s == nil {
		return nil
	}
	v := SavedWork{Repo: s.repo, Type: s.typeID, Chosen: s.chosen, Requirements: savedApproval(s.req), Plan: savedApproval(s.plan), Lock: s.lock, Ran: slices.Clone(s.ran)}
	if len(s.allowed) > 0 {
		v.Allowed = map[string][]string{}
		for p, set := range s.allowed {
			v.Allowed[p] = slices.Sorted(maps.Keys(set))
		}
	}
	return &v
}

func (w *Work) persist(sid string) {
	w.saveTexts(sid)
	err := w.change(func(st *WorkState) error {
		if v := w.saved(sid); v != nil {
			st.Sessions[sid] = *v
		} else {
			delete(st.Sessions, sid)
		}
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
	texts := map[string]ApprovedTexts{}
	for sid, v := range st.Sessions {
		if keep[sid] && (v.Requirements != nil || v.Plan != nil) {
			texts[sid] = w.loadTexts(sid)
		}
	}
	w.mu.Lock()
	for sid, v := range st.Sessions {
		if !keep[sid] || w.sessions[sid] != nil {
			continue
		}
		t := texts[sid]
		s := &workSession{repo: v.Repo, typeID: v.Type, chosen: v.Chosen, req: workApprovalOf(v.Requirements, t.Requirements), plan: workApprovalOf(v.Plan, t.Plan), lock: testLockOf(v.Lock), ran: firstStepsRan(v.Ran)}
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
		if key := (ApprovedTexts{Requirements: keptOf(s.req), Plan: keptOf(s.plan)}).key(); key != "" {
			w.textMu.Lock()
			w.textKeys[sid] = key
			w.textMu.Unlock()
		}
	}
	w.mu.Unlock()
	w.keepTexts(sids)
	err := w.change(func(st *WorkState) error {
		maps.DeleteFunc(st.Sessions, func(sid string, _ SavedWork) bool { return !keep[sid] })
		return nil
	})
	if err != nil {
		w.cfg.Logger.Warn("work", "event", "sessions-not-pruned")
	}
}

func testLockOf(l *TestLock) *TestLock {
	if l == nil || l.Commit == "" || len(l.Files) == 0 || len(l.Files) > MaxLockedTests || l.BranchLeft < 0 || l.BranchLeft > 0 && !l.Cut {
		return nil
	}
	return &TestLock{Commit: l.Commit, At: l.At, Files: maps.Clone(l.Files), Cut: l.Cut, BranchLeft: l.BranchLeft}
}
