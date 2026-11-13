package session

const (
	ResultApproved  = "approved"
	ResultForgotten = "forgotten"
)

func (w *Work) holdOf(name string) HoldSettings {
	return w.load().Projects[name].Hold
}

func (w *Work) SetHold(path string, h HoldSettings) error {
	if !h.Valid() {
		return explained(invalid("hold"), "A size limit is a whole number of files or lines, 0 to switch it off.")
	}
	r, err := w.repoByPath(path)
	if err != nil {
		return err
	}
	return w.change(func(st *WorkState) error {
		p := st.Projects[r.Name]
		p.Hold = h
		st.Projects[r.Name] = p
		return nil
	})
}

func (w *Work) approved(sid string, t PushTarget, head string, plan bool, hash string) {
	kind, what := AuditApproveRequirements, "requirements"
	if plan {
		kind, what = AuditApprovePlan, "plan"
	}
	w.audit(AuditEntry{Session: sid, Repository: t.Name, Branch: t.Branch, Commit: head, Kind: kind, Hash: hash,
		Reason: "the developer approved the " + what + " shown on the page, at commit " + shortSHA(head)})
}

func (w *Work) teamAudit(repo, kind, sha string) {
	reason := "the developer approved the team file " + TeamFile + " on the main branch"
	result := ResultApproved
	if kind == AuditForgetTeam {
		reason, result = "the developer forgot the approval of the team file "+TeamFile, ResultForgotten
	}
	w.audit(AuditEntry{Repository: repo, Kind: kind, Hash: sha, Result: result, Reason: reason})
}

func (w *Work) audit(e AuditEntry) {
	if w.cfg.Audit == nil {
		return
	}
	e.Time, e.By = w.cfg.Now(), ByPage
	if e.Result == "" {
		e.Result = ResultApproved
	}
	if err := w.cfg.Audit.Append(e); err != nil {
		w.cfg.Logger.Warn("approval", "event", "audit-not-written")
	}
}

func (d *Dispatcher) startupAudit(r *run, hash string) {
	if d.cfg.Audit == nil {
		return
	}
	e := AuditEntry{Time: d.reg.now(), Session: r.sid, Repository: r.repo, Branch: r.branch, By: ByPage, Kind: AuditApproveStartup, Result: ResultApproved,
		Agent: r.spec.ID, Hash: hash, Reason: "the developer approved the start-up files " + r.spec.Name + " loads from this repository"}
	if err := d.cfg.Audit.Append(e); err != nil {
		d.cfg.Logger.Warn("start-up files", "event", "audit-not-written")
	}
}
