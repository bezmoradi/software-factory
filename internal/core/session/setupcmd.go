package session

func (w *SessionWork) startSetup(sid, project, cwd string) {
	command := w.command(project, CommandSetup)
	if command == "" || w.cfg.Host == nil || sid == "" || cwd == "" {
		return
	}
	w.mu.Lock()
	w.runsOf(sid).setting = true
	w.mu.Unlock()
	w.changed(sid)
	go func() {
		done := w.runCommand(w.cfg.Base, sid, CommandSetup, command, cwd)
		w.mu.Lock()
		w.runsOf(sid).setting = false
		w.mu.Unlock()
		w.audit(AuditEntry{Time: w.cfg.Now(), Session: sid, Repository: project, By: "engine", Kind: AuditSetupRun, Result: done.State})
		w.changed(sid)
		w.pump(sid, "")
	}()
}

func (w *SessionWork) Runs(sid string) (check, setup *CommandRun) {
	w.mu.Lock()
	defer w.mu.Unlock()
	rs := w.runs[sid]
	if rs == nil {
		return nil, nil
	}
	if rs.check != nil {
		c := *rs.check
		check = &c
	}
	if rs.setup != nil {
		s := *rs.setup
		setup = &s
	}
	return check, setup
}
