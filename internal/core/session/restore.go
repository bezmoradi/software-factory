package session

import (
	"cmp"
	"slices"
	"time"
)

type SavedRun struct {
	SID       string
	ID        string
	Agent     string
	Repo      string
	RepoPath  string
	Base      string
	Name      string
	Branch    string
	Cwd       string
	Box       string
	Display   string
	StartedAt time.Time
	Sandboxed bool

	InterruptedAt  time.Time
	InTurnSince    time.Time
	AgentVersion   string
	EngineVersion  string
	WorkflowCommit string
	ReviewerClaude string
	ReviewerPlugin string

	CodexThread string

	Origin     string
	Earlier    []Stint
	HandedTo   string
	HandedHead string

	Group       string
	GroupKind   string
	HistoryOnly bool
	RmDone      bool
	Aside       string
}

type RunStore interface {
	Load() ([]SavedRun, error)
	Save([]SavedRun) error
}

const runsUnreadWhy = "The engine couldn't read its record of sessions (sessions.json) when it started, so it starts no new session: a new one would replace that record and leave every earlier session without it. Fix the file in the engine's state folder and restart the engine."

func (d *Dispatcher) OnRemoved(fn func(sid string)) {
	d.mu.Lock()
	d.removed = append(d.removed, fn)
	d.mu.Unlock()
}

func (d *Dispatcher) Known(sid string) bool { return d.hasRun(sid) }

func (d *Dispatcher) RunsUnread() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.runsUnread
}

func (d *Dispatcher) runsReadable() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.runsUnread {
		return explained(conflict("sessions unread"), runsUnreadWhy)
	}
	return nil
}

func (d *Dispatcher) saveRuns() {
	if d.cfg.Runs == nil {
		return
	}
	d.saveMu.Lock()
	defer d.saveMu.Unlock()
	d.mu.Lock()
	if d.runsUnread {
		d.mu.Unlock()
		d.cfg.Logger.Warn("sessions", "event", "not-saved", "why", "unread")
		return
	}
	out := make([]SavedRun, 0, len(d.runs))
	for sid, r := range d.runs {
		out = append(out, SavedRun{SID: sid, ID: r.id, Agent: r.spec.ID, Repo: r.repo, RepoPath: r.repoPath, Base: r.base, Name: r.name,
			Branch: r.branch, Cwd: r.cwd, Display: r.display, StartedAt: r.at, InterruptedAt: r.stamp.interruptedAt,
			AgentVersion: r.stamp.agent, EngineVersion: r.stamp.engine, WorkflowCommit: r.stamp.workflow,
			ReviewerClaude: r.stamp.reviewer, ReviewerPlugin: r.stamp.reviewerPlugin})
		out[len(out)-1].CodexThread = savedThread(r.thread)
		out[len(out)-1].Origin, out[len(out)-1].Earlier = r.origin, slices.Clone(r.earlier)
		out[len(out)-1].HandedTo, out[len(out)-1].HandedHead = r.handedTo, r.handedHead
		out[len(out)-1].Group, out[len(out)-1].GroupKind = r.group, r.groupKind
		out[len(out)-1].HistoryOnly = r.history
		out[len(out)-1].RmDone = r.rmDone
		out[len(out)-1].Aside = r.aside
		out[len(out)-1].Box = r.box
	}
	d.mu.Unlock()
	for i := range out {
		lv, ok := d.reg.Level(out[i].SID)
		out[i].Sandboxed = ok && lv.Sandboxed
		out[i].InTurnSince = d.reg.TurnSince(out[i].SID)
	}
	slices.SortFunc(out, func(a, b SavedRun) int { return cmp.Compare(a.SID, b.SID) })
	if err := d.cfg.Runs.Save(out); err != nil {
		d.cfg.Logger.Warn("sessions", "event", "not-saved")
	}
}

func (d *Dispatcher) Restore() int {
	if d.cfg.Runs == nil {
		return 0
	}
	saved, err := d.cfg.Runs.Load()
	if err != nil {
		d.mu.Lock()
		d.runsUnread = true
		d.mu.Unlock()
		d.cfg.Logger.Warn("sessions", "event", "not-read")
		return 0
	}
	handed := d.oneHolder(saved)
	var sids []string
	for _, s := range saved {
		spec, ok := findAgent(d.cfg.Agents, s.Agent)
		if !ok || !sidPattern.MatchString(s.SID) || !shortID.MatchString(s.ID) {
			d.cfg.Logger.Warn("sessions", "event", "not-restored")
			continue
		}
		r := &run{spec: spec, repo: s.Repo, repoPath: s.RepoPath, base: s.Base, name: s.Name, branch: s.Branch, cwd: s.Cwd,
			sid: s.SID, id: s.ID, display: s.Display, at: s.StartedAt,
			stamp: runStamp{interruptedAt: s.InterruptedAt, agent: s.AgentVersion, engine: s.EngineVersion, workflow: s.WorkflowCommit,
				reviewer: s.ReviewerClaude, reviewerPlugin: s.ReviewerPlugin}}
		r.stamp.interruptedAt = cutAt(s)
		r.thread = savedThread(s.CodexThread)
		r.origin, r.earlier, r.handedTo, r.handedHead = s.Origin, slices.Clone(s.Earlier), s.HandedTo, s.HandedHead
		r.group, r.groupKind = s.Group, s.GroupKind
		r.history = s.HistoryOnly
		r.rmDone = s.RmDone
		r.aside = s.Aside
		if ValidBox(s.Box) && !r.history {
			r.box, r.clone = s.Box, BoxClone(d.cfg.Workspace, s.Box, s.Repo)
		}
		d.mu.Lock()
		known := d.runs[s.SID] != nil
		if !known {
			d.runs[s.SID] = r
		}
		d.mu.Unlock()
		if known {
			continue
		}
		a := d.admission(r, spec.Method == MethodPlugin)
		if !s.StartedAt.IsZero() {
			a.StartedAt = s.StartedAt
		}
		d.reg.Restore(s.SID, a, s.Sandboxed)
		d.interrupted(s.SID, r.stamp.interruptedAt)
		sids = append(sids, s.SID)
	}
	if d.cfg.Work != nil {
		d.cfg.Work.Restore(sids)
		for _, sid := range sids {
			if d.cfg.Work.Has(sid) {
				continue
			}
			d.mu.Lock()
			r := d.runs[sid]
			d.mu.Unlock()
			d.cfg.Work.Begin(sid, r.repo, r.spec.Knackpack, r.typ)
		}
	}
	if d.cfg.Reviews != nil {
		d.cfg.Reviews.ShowKept(sids)
	}
	if len(sids) > 0 {
		d.cfg.Logger.Info("sessions", "event", "restored", "count", len(sids))
	}
	if handed > 0 {
		d.saveRuns()
	}
	return len(sids)
}

func (d *Dispatcher) restorable(s SavedRun) bool {
	if _, ok := findAgent(d.cfg.Agents, s.Agent); !ok || !sidPattern.MatchString(s.SID) || !shortID.MatchString(s.ID) {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.runs[s.SID] == nil
}

func continues(a, b SavedRun) bool {
	return slices.ContainsFunc(a.Earlier, func(st Stint) bool { return st.SID == b.SID })
}

func (d *Dispatcher) lastHead(s, holder SavedRun) string {
	if heads, _ := d.reg.worktreeReaders(); heads != nil {
		if clone := BoxClone(d.cfg.Workspace, s.Box, s.Repo); clone != "" {
			if h, err := heads.Head(clone, s.Branch); err == nil && sha1Hex.MatchString(h) {
				return h
			}
		}
	}
	for _, st := range holder.Earlier {
		if st.SID == s.SID && sha1Hex.MatchString(st.To) {
			return st.To
		}
	}
	return s.Base
}

func (d *Dispatcher) oneHolder(saved []SavedRun) int {
	holder := map[string]int{}
	for i, s := range saved {
		if s.HandedTo != "" || s.HistoryOnly || !d.restorable(s) {
			continue
		}
		key := s.Repo + "\x00" + s.Branch
		j, ok := holder[key]
		if !ok {
			holder[key] = i
			continue
		}
		h := saved[j]
		switch {
		case continues(s, h):
			holder[key] = i
		case continues(h, s):
		case s.StartedAt.After(h.StartedAt) || s.StartedAt.Equal(h.StartedAt) && s.SID > h.SID:
			holder[key] = i
		}
	}
	n := 0
	for i := range saved {
		s := &saved[i]
		if s.HandedTo != "" || s.HistoryOnly || !d.restorable(*s) {
			continue
		}
		j := holder[s.Repo+"\x00"+s.Branch]
		if j == i {
			continue
		}
		s.HandedTo, s.HandedHead = saved[j].SID, d.lastHead(*s, saved[j])
		n++
		d.cfg.Logger.Warn("sessions", "event", "branch-held-twice", "kept", saved[j].SID)
	}
	return n
}
