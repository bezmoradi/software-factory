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
	Display   string
	StartedAt time.Time
	Sandboxed bool

	InterruptedAt  time.Time
	AgentVersion   string
	EngineVersion  string
	WorkflowCommit string
}

type RunStore interface {
	Load() ([]SavedRun, error)
	Save([]SavedRun) error
}

func (d *Dispatcher) saveRuns() {
	if d.cfg.Runs == nil {
		return
	}
	d.saveMu.Lock()
	defer d.saveMu.Unlock()
	d.mu.Lock()
	out := make([]SavedRun, 0, len(d.runs))
	for sid, r := range d.runs {
		out = append(out, SavedRun{SID: sid, ID: r.id, Agent: r.spec.ID, Repo: r.repo, RepoPath: r.repoPath, Base: r.base, Name: r.name,
			Branch: r.branch, Cwd: r.cwd, Display: r.display, StartedAt: r.at, InterruptedAt: r.stamp.interruptedAt,
			AgentVersion: r.stamp.agent, EngineVersion: r.stamp.engine, WorkflowCommit: r.stamp.workflow})
	}
	d.mu.Unlock()
	for i := range out {
		lv, ok := d.reg.Level(out[i].SID)
		out[i].Sandboxed = ok && lv.Sandboxed
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
		d.cfg.Logger.Warn("sessions", "event", "not-read")
		return 0
	}
	var sids []string
	for _, s := range saved {
		spec, ok := findAgent(d.cfg.Agents, s.Agent)
		if !ok || !sidPattern.MatchString(s.SID) || !shortID.MatchString(s.ID) {
			d.cfg.Logger.Warn("sessions", "event", "not-restored")
			continue
		}
		r := &run{spec: spec, repo: s.Repo, repoPath: s.RepoPath, base: s.Base, name: s.Name, branch: s.Branch, cwd: s.Cwd,
			sid: s.SID, id: s.ID, display: s.Display, at: s.StartedAt,
			stamp: runStamp{interruptedAt: s.InterruptedAt, agent: s.AgentVersion, engine: s.EngineVersion, workflow: s.WorkflowCommit}}
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
		d.interrupted(s.SID, s.InterruptedAt)
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
			d.cfg.Work.Begin(sid, r.repo, r.spec.Method == MethodPlugin, r.typ)
		}
	}
	if len(sids) > 0 {
		d.cfg.Logger.Info("sessions", "event", "restored", "count", len(sids))
	}
	return len(sids)
}
