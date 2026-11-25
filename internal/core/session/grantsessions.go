package session

func (d *Dispatcher) GrantTarget(sid string) (GrantTarget, bool) {
	d.mu.Lock()
	r := d.runs[sid]
	if r == nil || r.box == "" || r.history {
		d.mu.Unlock()
		return GrantTarget{}, false
	}
	t := GrantTarget{SID: sid, Project: r.repo, Branch: r.branch, Box: r.box, Running: r.running}
	if r.cwd != "" && r.cwd == BoxWorktree(d.cfg.Workspace, r.box, r.name) {
		t.Worktree = r.name
	}
	d.mu.Unlock()
	t.Live = d.liveBox(t.Box)
	return t, true
}

func (d *Dispatcher) BoxSession(box string) (string, bool) {
	if box == "" {
		return "", false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	found := ""
	for sid, r := range d.runs {
		if r.box != box || r.history {
			continue
		}
		if r.running {
			return sid, true
		}
		found = sid
	}
	return found, found != ""
}

func (d *Dispatcher) GrantTargets() []GrantTarget {
	d.mu.Lock()
	var out []GrantTarget
	for sid, r := range d.runs {
		if r.box == "" || r.history {
			continue
		}
		out = append(out, GrantTarget{SID: sid, Project: r.repo, Branch: r.branch, Box: r.box, Running: r.running})
	}
	d.mu.Unlock()
	for i := range out {
		out[i].Live = d.liveBox(out[i].Box)
	}
	return out
}
