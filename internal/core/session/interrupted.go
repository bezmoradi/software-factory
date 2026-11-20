package session

import (
	"context"
	"regexp"
	"strings"
	"time"
)

const lifeFor = 5 * time.Second

var bootID = regexp.MustCompile(`^[0-9a-f]{16}$`)

type HostLife interface {
	Boot(ctx context.Context) (string, error)
	Exited(ctx context.Context, id string) (bool, error)
}

type runStamp struct {
	interruptedAt  time.Time
	agent          string
	engine         string
	workflow       string
	reviewer       string
	reviewerPlugin string
	boot           string
}

func (d *Dispatcher) stampOf(agent string, interrupted time.Time) runStamp {
	s := runStamp{interruptedAt: interrupted, engine: d.cfg.Version}
	if d.cfg.Versions != nil {
		s.agent = d.cfg.Versions(agent)
	}
	s.reviewer, s.reviewerPlugin = d.reviewerStamp()
	return s
}

func (d *Dispatcher) woke(r *run) {
	now := d.stampOf(r.spec.ID, time.Time{})
	installed := d.installedWorkflow()
	d.mu.Lock()
	if r.spec.Method == MethodPlugin {
		now.workflow = r.stamp.workflow
	}
	changed := versionsChanged(r.spec.Name, r.stamp, now, installed, d.refusedWorkflow(now.workflow))
	r.stamp = now
	sid, repo, pack := r.sid, r.repo, r.pack.Choice
	d.mu.Unlock()
	d.saveRuns()
	if d.cfg.Packs != nil {
		changed = strings.TrimSpace(changed + " " + d.cfg.Packs.WakeNote(repo, pack))
	}
	d.showVersions(sid, changed)
}

func (d *Dispatcher) MarkStopping() int {
	now := d.reg.now()
	type candidate struct {
		sid string
		r   *run
		b   *bridge
	}
	var list []candidate
	d.mu.Lock()
	for sid, r := range d.runs {
		if r.running {
			list = append(list, candidate{sid: sid, r: r, b: r.bridge})
		}
	}
	d.mu.Unlock()
	marked := 0
	for _, c := range list {
		if !d.reg.Busy(c.sid) && (c.b == nil || !c.b.inTurn()) {
			continue
		}
		d.mu.Lock()
		c.r.stamp.interruptedAt = now
		d.mu.Unlock()
		marked++
	}
	if marked > 0 {
		d.saveRuns()
		d.cfg.Logger.Info("sessions", "event", "interrupted", "count", marked)
	}
	return marked
}

func cutAt(s SavedRun) time.Time {
	if !s.InterruptedAt.IsZero() {
		return s.InterruptedAt
	}
	return s.InTurnSince
}

func (d *Dispatcher) life(host Host) HostLife {
	l, _ := host.(HostLife)
	return l
}

func (d *Dispatcher) booted(r *run) {
	l := d.life(d.host(r))
	if l == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), lifeFor)
	boot, err := l.Boot(ctx)
	cancel()
	if err != nil || !bootID.MatchString(boot) {
		boot = ""
	}
	d.mu.Lock()
	r.stamp.boot = boot
	d.mu.Unlock()
}

func (d *Dispatcher) currentBoot(ctx context.Context, host Host) string {
	l := d.life(host)
	if l == nil {
		return ""
	}
	bctx, cancel := context.WithTimeout(ctx, lifeFor)
	defer cancel()
	boot, err := l.Boot(bctx)
	if err != nil || !bootID.MatchString(boot) {
		return ""
	}
	return boot
}

func (d *Dispatcher) exitedItself(r *run, id string) bool {
	l := d.life(d.host(r))
	if l == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), lifeFor)
	defer cancel()
	exited, err := l.Exited(ctx, id)
	return err == nil && exited
}

func (d *Dispatcher) lost(sid string, inTurn bool) {
	d.reg.Ended(sid, true)
	d.cfg.Logger.Info("agent session", "event", "lost", "turn", inTurn)
	if inTurn && d.cfg.Note != nil {
		d.cfg.Note(sid, LostNote(d.reg.now()))
	}
}

func LostNote(at time.Time) string {
	return "Software Factory lost your session's process at " + at.UTC().Format("2006-01-02 15:04 UTC") + " during your last turn: the engine's connection to it dropped, or the session's container restarted, so that turn was cut off. Tool calls in it may or may not have taken effect, and a factory_push, factory_issue or factory_pull_request call in it may not have been answered. Check git status and git log before you go on."
}

func (d *Dispatcher) interrupted(sid string, at time.Time) {
	if at.IsZero() || d.cfg.Note == nil {
		return
	}
	d.cfg.Note(sid, InterruptedNote(at))
}

func InterruptedNote(at time.Time) string {
	return "The engine stopped at " + at.UTC().Format("2006-01-02 15:04 UTC") + " during your last turn, so that turn was cut off. Tool calls in it may or may not have taken effect, and a factory_push, factory_issue or factory_pull_request call in it may not have been answered. Check git status and git log before you go on."
}

func (r *Registry) Busy(sid string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.conns {
		if c.sid == sid && c.busy {
			return true
		}
	}
	return false
}

func (b *bridge) inTurn() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.turn != ""
}

func (d *Dispatcher) restarted(ctx context.Context, host Host, mine map[string]*run, live map[string]bool) {
	d.mu.Lock()
	started := false
	for _, r := range mine {
		started = started || (r.running && r.stamp.boot != "")
	}
	d.mu.Unlock()
	if !started {
		return
	}
	boot := d.currentBoot(ctx, host)
	if boot == "" {
		return
	}
	for sid, r := range mine {
		d.mu.Lock()
		gone := r.running && !r.busy && r.stamp.boot != "" && r.stamp.boot != boot && !live[sid]
		if gone {
			r.running = false
		}
		d.mu.Unlock()
		if gone {
			d.lost(sid, !d.reg.TurnSince(sid).IsZero())
		}
	}
}

func (b *bridge) cutTurn() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cut
}
