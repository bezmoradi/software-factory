package session

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	SandboxStopReason = "the sandbox check failed"
	stopEveryFor      = 3 * time.Minute
)

func (r *Registry) stoppedFor(sid, why string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if a := r.admitted[sid]; a != nil {
		a.stopReason = why
		r.announce(r.now())
	}
}

func StoppedNote(at time.Time, why string) string {
	return "Software Factory stopped your session at " + at.UTC().Format("2006-01-02 15:04 UTC") + " because " + why + ", so a turn that was running was cut off. Tool calls in it may or may not have taken effect. Check git status and git log before you go on."
}

func (d *Dispatcher) sandboxFailed() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), stopEveryFor)
		defer cancel()
		d.StopEvery(ctx, SandboxStopReason)
	}()
}

func (d *Dispatcher) startInFlight() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.starting
}

func (d *Dispatcher) StopEvery(ctx context.Context, why string) {
	inFlight := d.startInFlight()
	d.stopRunning(ctx, why)
	if !inFlight {
		return
	}
	for d.startInFlight() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
	d.stopRunning(ctx, why)
}

func (d *Dispatcher) stopRunning(ctx context.Context, why string) {
	for _, s := range d.reg.Sessions() {
		if s.Live && !s.Verified && s.Controlled {
			_ = d.reg.Interrupt(s.SID)
		}
	}
	d.mu.Lock()
	var sids []string
	for sid, r := range d.runs {
		if r.running || r.busy {
			sids = append(sids, sid)
		}
	}
	d.mu.Unlock()
	var wg sync.WaitGroup
	for _, sid := range sids {
		wg.Go(func() { d.stopOne(ctx, sid, why) })
	}
	wg.Wait()
}

func (d *Dispatcher) stopOne(ctx context.Context, sid, why string) {
	for ctx.Err() == nil {
		d.mu.Lock()
		r := d.runs[sid]
		running, busy := r != nil && r.running, r != nil && r.busy
		d.mu.Unlock()
		if !running && !busy {
			return
		}
		if !busy {
			d.reg.stoppedFor(sid, why)
			_ = d.reg.Interrupt(sid)
			sctx, cancel := context.WithTimeout(ctx, claudeRun+stopFor)
			err := d.stopAs(sctx, sid, ByEngine, why)
			cancel()
			if err == nil {
				shared.For(ctx, d.cfg.Logger).Warn("agent session", "event", "stopped-by-engine", "why", why)
				d.reg.NoteFor("stopped")(sid, StoppedNote(d.reg.now(), why))
				return
			}
			if e, ok := errors.AsType[*Error](err); !ok || e.Reason != "busy" {
				shared.For(ctx, d.cfg.Logger).Warn("agent session", "event", "stop-failed", "why", why)
				return
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
}
