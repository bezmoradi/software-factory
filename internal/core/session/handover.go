package session

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	AuditHandover  = "handover"
	handedMessage  = "This session's branch continues in another session, which holds it now. Open that session, or start a new one."
	handoverSettle = 10 * time.Second
	noteHandover   = "handover"
)

var sessionBranch = regexp.MustCompile(`^sf/[0-9a-f]{8}$`)

type Continuation struct {
	SID     string
	Branch  string
	Head    string
	Earlier []Stint
}

func (r *Registry) worktreeReaders() (Heads, WorktreeReader) {
	r.mu.Lock()
	b := r.bookLocked()
	r.mu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.watch == nil {
		return nil, nil
	}
	return b.watch.Heads, b.watch.Reader
}

func (r *Registry) handedOver(sid string, l *Lineage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bookLocked().forget(sid)
	if a := r.admitted[sid]; a != nil {
		a.Lineage = l
		r.announce(r.now())
	}
}

func (d *Dispatcher) branchFree(branch string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range d.runs {
		if r.branch == branch && r.handedTo == "" && !r.handing {
			return explained(conflict("branch held"), "Another session works on the branch "+branch+", and two sessions never hold one branch.")
		}
	}
	return nil
}

func (d *Dispatcher) waitStopped(r *run) {
	until := d.reg.now().Add(handoverSettle)
	for {
		d.mu.Lock()
		running := r.running
		d.mu.Unlock()
		if !running || !d.reg.now().Before(until) {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (d *Dispatcher) Handover(ctx context.Context, sid, agent string, consent Consent) (Started, error) {
	if err := d.historyOnly(sid); err != nil {
		return Started{}, err
	}
	r, err := d.claim(sid)
	if err != nil {
		return Started{}, err
	}
	defer d.release(r)
	d.mu.Lock()
	handed, running := r.handedTo != "", r.running
	d.mu.Unlock()
	spec, ok := findAgent(d.cfg.Agents, agent)
	switch {
	case handed:
		return Started{}, explained(conflict("handed over"), handedMessage)
	case !ok:
		return Started{}, explained(invalid("agent"), "Software Factory doesn't know that agent.")
	case spec.ID == r.spec.ID:
		return Started{}, explained(invalid("agent"), "Choose another agent than the one this session runs.")
	case !spec.Enabled:
		return Started{}, explained(conflict("agent off"), spec.Off)
	}
	if spec = d.installed(spec); !spec.Enabled {
		return Started{}, explained(conflict("not installed"), spec.Off)
	}
	if why := d.reg.halt.starts(); why != "" {
		return Started{}, explained(conflict("halted"), why)
	}
	if running && !d.reg.TurnSince(sid).IsZero() {
		return Started{}, explained(conflict("in turn"), "The session is in its turn. Wait for it to end, or stop the turn, then continue with another agent.")
	}
	id := d.cfg.Identity.Current()
	if !id.Set() {
		return Started{}, noIdentity()
	}
	head, err := d.cleanHead(r)
	if err != nil {
		return Started{}, err
	}
	earlier := append(slices.Clone(r.earlier), Stint{Agent: r.spec.ID, SID: sid, Name: clip(r.display, maxName), From: r.base, To: head})
	if len(earlier) > MaxStints {
		earlier = earlier[len(earlier)-MaxStints:]
	}
	d.mu.Lock()
	r.handing = true
	d.mu.Unlock()
	started, err := d.Dispatch(ctx, Dispatch{Path: r.repoPath, Agent: spec.ID, Name: r.display, Consent: consent,
		Continues: &Continuation{SID: sid, Branch: r.branch, Head: head, Earlier: earlier}, asked: "handover:" + sid + ":" + spec.ID})
	if err != nil || started.SID == "" {
		d.endHanding(r, "")
		return started, err
	}
	d.endHanding(r, started.SID)
	d.mu.Lock()
	r.handedHead = head
	d.mu.Unlock()
	d.saveRuns()
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), claudeRun+stopFor)
	d.mu.Lock()
	running = r.running
	d.mu.Unlock()
	if running {
		if err := d.stopRun(sctx, sid, r); err != nil {
			shared.For(ctx, d.cfg.Logger).Warn("handover", "event", "old-not-stopped")
		}
		d.waitStopped(r)
	}
	d.stopBox(sctx, r.box)
	cancel()
	if running {
		d.lifecycle(ctx, AuditSessionStop, r, ByPage, ResultStopped, "handed over to session "+started.SID)
	}
	last, moved := d.headAfterStop(r, head)
	d.mu.Lock()
	r.handedHead = last
	d.mu.Unlock()
	d.saveRuns()
	d.reg.handedOver(sid, lineageOf(r))
	if d.cfg.Work != nil {
		d.cfg.Work.carry(sid, started.SID, spec.Knackpack)
	}
	d.reg.NoteFor(noteHandover)(started.SID, d.handoverNote(r, head, last, moved))
	d.handoverAudit(r, spec, started.SID, head, last, moved)
	shared.For(ctx, d.cfg.Logger).Info("handover", "from", r.spec.ID, "to", spec.ID, "moved", moved)
	return started, nil
}

func (d *Dispatcher) cleanHead(r *run) (string, error) {
	heads, reader := d.reg.worktreeReaders()
	if heads == nil || reader == nil {
		return "", explained(conflict("worktree"), "This engine can't read the session's worktree, so it can't hand the branch over.")
	}
	head, err := heads.Head(r.clone, r.branch)
	if err != nil || !sha1Hex.MatchString(head) {
		return "", explained(conflict("branch"), "The engine couldn't read the session's branch "+r.branch+".")
	}
	u, err := reader.Uncommitted(r.clone, r.cwd, head)
	if err != nil {
		return "", explained(conflict("worktree"), "The engine couldn't read the session's worktree, so it can't tell whether everything is committed.")
	}
	if u.Count > 0 {
		return "", explained(conflict("uncommitted"), "The session's worktree has "+plural(u.Count, "a file", "files")+" changed but not committed, which the other agent wouldn't get. Press Ask the agent to commit them, under the session's uncommitted files, then continue with another agent.")
	}
	return head, nil
}

func (d *Dispatcher) headAfterStop(r *run, before string) (string, bool) {
	heads, _ := d.reg.worktreeReaders()
	if heads == nil {
		return before, false
	}
	now, err := heads.Head(r.clone, r.branch)
	if err != nil || !sha1Hex.MatchString(now) || now == before {
		return before, false
	}
	d.cfg.Logger.Info("handover", "event", "committed-while-stopping")
	return now, true
}

func (d *Dispatcher) endHanding(r *run, to string) {
	d.mu.Lock()
	r.handing = false
	if to != "" {
		r.handedTo = to
	}
	d.mu.Unlock()
}

func (w *Work) carry(from, to string, claude bool) {
	w.mu.Lock()
	src, dst := w.sessions[from], w.sessions[to]
	if src == nil || dst == nil {
		w.mu.Unlock()
		return
	}
	c := *src
	c.claude = claude
	c.allowed = map[string]map[string]bool{}
	for p, set := range src.allowed {
		c.allowed[p] = make(map[string]bool, len(set))
		for b := range set {
			c.allowed[p][b] = true
		}
	}
	if c.req != nil {
		v := *c.req
		c.req = &v
	}
	if c.plan != nil {
		v := *c.plan
		c.plan = &v
	}
	keepType := true
	w.mu.Unlock()
	if _, ok := typeIn(w.setupFor(c.repo, claude).setup.Types, c.typeID); !ok {
		keepType = false
	}
	w.mu.Lock()
	if !keepType {
		c.typeID, c.chosen = dst.typeID, dst.chosen
	}
	w.sessions[to] = &c
	w.mu.Unlock()
	w.persist(to)
}

func (w *Work) approvedWords(sid string) string {
	ws, ok := w.session(sid)
	if !ok {
		return ""
	}
	var parts []string
	if ws.req != nil {
		parts = append(parts, "the requirements (text hash "+ws.req.Hash[:12]+", at commit "+shortSHA(ws.req.Commit)+")")
	}
	if ws.plan != nil {
		parts = append(parts, "the plan (text hash "+ws.plan.Hash[:12]+", at commit "+shortSHA(ws.plan.Commit)+")")
	}
	if n := len(ws.allowed); n > 0 {
		parts = append(parts, plural(n, "a flagged file", "flagged files")+" allowed for the push")
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ", ")
}

func (d *Dispatcher) handoverNote(r *run, head, last string, moved bool) string {
	approved := "nothing yet"
	if d.cfg.Work != nil {
		if words := d.cfg.Work.approvedWords(r.sid); words != "" {
			approved = words
		}
	}
	at := "Its last commit is " + head + "."
	if moved {
		at = "This session starts at its commit " + head + ". " + r.spec.Name + "'s session committed " + last + " while it was being stopped, after this session started; that commit isn't in this clone. It stays in the earlier session's clone, and deleting the earlier session asks the person before it is discarded."
	}
	return "This session continues the branch " + r.branch + ", which " + r.spec.Name + " worked on before in another session. " + at + " The person approved " + approved +
		"; those approvals carry over, bound to the same text and commit. The earlier conversation isn't passed on: read the branch's commits and files to see where the work stands."
}

func (d *Dispatcher) handoverAudit(r *run, to AgentSpec, sid, head, last string, moved bool) {
	if d.cfg.Audit == nil {
		return
	}
	stopped := "'s session and its container were stopped once the new session had started from " + shortSHA(head)
	if moved {
		stopped += "; it committed " + shortSHA(last) + " while it was being stopped, which only its own clone holds"
	}
	e := AuditEntry{Time: d.reg.now(), Session: r.sid, Repository: r.repo, Branch: r.branch, Commit: head, By: ByPage, Kind: AuditHandover, Result: "continued", Agent: to.ID,
		Reason: "the developer continued this branch with " + to.Name + " in session " + sid + "; " + r.spec.Name + stopped, Missing: []string{}}
	if err := d.cfg.Audit.Append(e); err != nil {
		d.cfg.Logger.Warn("handover", "event", "audit-not-written")
	}
}
