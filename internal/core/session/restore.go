package session

import (
	"cmp"
	"slices"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/shared"
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

	Origin string

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

const doubledMessage = "Another session on this engine holds this session's branch, so this one is read-only. Delete it to offer its unpushed commits, or keep working in the other session."

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
		out[len(out)-1].Origin = r.origin
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
	doubled := d.oneHolder(saved)
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
		r.origin = s.Origin
		r.group, r.groupKind = s.Group, s.GroupKind
		r.history = s.HistoryOnly
		r.doubled = doubled[s.SID]
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
		if r.stamp.interruptedAt.IsZero() || !d.SkipDoubled(s.SID, "interrupted-note") {
			d.interrupted(s.SID, r.stamp.interruptedAt)
		}
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
			d.cfg.Work.Begin(sid, r.repo, r.typ)
		}
	}
	if d.cfg.Reviews != nil {
		d.cfg.Reviews.ShowKept(sids, d.keptBranches(sids, d.cfg.Work != nil))
	}
	if len(sids) > 0 {
		d.cfg.Logger.Info("sessions", "event", "restored", "count", len(sids))
	}
	return len(sids)
}

func (d *Dispatcher) keptBranches(sids []string, recheck bool) map[string]KeptBranch {
	out := map[string]KeptBranch{}
	for _, sid := range sids {
		t, ok := d.PushTarget(sid)
		if !ok {
			continue
		}
		k := KeptBranch{Branch: t.Branch, Recheck: recheck}
		if d.cfg.Work != nil {
			k.Rounds = d.cfg.Work.Rounds(sid, t.Name)
		}
		out[sid] = k
	}
	return out
}

func (d *Dispatcher) restorable(s SavedRun) bool {
	if _, ok := findAgent(d.cfg.Agents, s.Agent); !ok || !sidPattern.MatchString(s.SID) || !shortID.MatchString(s.ID) {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.runs[s.SID] == nil
}

func (d *Dispatcher) Doubled(sid string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	r := d.runs[sid]
	return r != nil && r.doubled
}

func (d *Dispatcher) SkipDoubled(sid, what string) bool {
	return d.skips.skip(d.Doubled, d.cfg.Logger, sid, what)
}

func targetOrHeld(targets, held func(sid string) (PushTarget, bool), sid string) (PushTarget, bool) {
	if t, ok := targets(sid); ok {
		return t, true
	}
	if held == nil {
		return PushTarget{}, false
	}
	return held(sid)
}

func isDoubled(doubled func(sid string) bool, sid string) bool {
	return doubled != nil && doubled(sid)
}

func heldBranch(doubled func(sid string) bool, sid string) error {
	if isDoubled(doubled, sid) {
		return explained(conflict("branch held"), doubledMessage)
	}
	return nil
}

type doubledSkips struct {
	mu     sync.Mutex
	logged map[string]bool
}

func (s *doubledSkips) skip(doubled func(sid string) bool, logger shared.Logger, sid, what string) bool {
	if !isDoubled(doubled, sid) {
		return false
	}
	key := what + "\x00" + sid
	s.mu.Lock()
	first := !s.logged[key]
	if first {
		if s.logged == nil {
			s.logged = map[string]bool{}
		}
		s.logged[key] = true
	}
	s.mu.Unlock()
	if first {
		logger.Info("sessions", "event", "doubled-skipped", "what", what, "sid", sid)
	}
	return true
}

func notStartedHere(doubled func(sid string) bool, sid, why string) error {
	if err := heldBranch(doubled, sid); err != nil {
		return err
	}
	return explained(conflict("not started here"), why)
}

func (d *Dispatcher) oneHolder(saved []SavedRun) map[string]bool {
	holder := map[string]int{}
	for i, s := range saved {
		if s.HistoryOnly || !d.restorable(s) {
			continue
		}
		key := s.Repo + "\x00" + s.Branch
		j, ok := holder[key]
		if !ok || s.StartedAt.After(saved[j].StartedAt) || s.StartedAt.Equal(saved[j].StartedAt) && s.SID > saved[j].SID {
			holder[key] = i
		}
	}
	out := map[string]bool{}
	for i, s := range saved {
		if s.HistoryOnly || !d.restorable(s) {
			continue
		}
		j := holder[s.Repo+"\x00"+s.Branch]
		if j == i {
			continue
		}
		out[s.SID] = true
		d.cfg.Logger.Warn("sessions", "event", "branch-held-twice", "kept", saved[j].SID)
	}
	return out
}
