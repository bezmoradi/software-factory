package session

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/level"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	CheckEvery = 5 * time.Minute
	checkFor   = 90 * time.Second

	WaitingForFirstCheck = "not checked while the engine was down; waiting for the first check"
)

type Sandbox interface {
	Check(ctx context.Context) level.Observation
	CheckBox(ctx context.Context, b BoxProbe) level.Observation
}

type Installs interface {
	Ready(agent string) (bool, string)
}

type CheckState struct {
	Observation level.Observation
	At          time.Time
	Ran         bool
}

type checks struct {
	mu     sync.Mutex
	run    sync.Mutex
	last   CheckState
	engine context.Context
}

func (d *Dispatcher) engineEnded() bool {
	d.checks.mu.Lock()
	e := d.checks.engine
	d.checks.mu.Unlock()
	return e != nil && e.Err() != nil
}

func (d *Dispatcher) LastCheck() CheckState {
	d.checks.mu.Lock()
	defer d.checks.mu.Unlock()
	s := d.checks.last
	s.Observation.Parts = append([]string(nil), s.Observation.Parts...)
	return s
}

func (d *Dispatcher) check(ctx context.Context) CheckState {
	if d.cfg.Sandbox == nil {
		return CheckState{Observation: notRun(), At: d.reg.now()}
	}
	d.checks.run.Lock()
	defer d.checks.run.Unlock()
	if ctx.Err() != nil {
		return CheckState{Observation: unwaited(), At: d.reg.now()}
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), checkFor)
	defer cancel()
	o := d.cfg.Sandbox.Check(cctx)
	o.By = level.Engine
	if o.Event != level.CheckPassed && o.Event != level.CheckFailed {
		o.Event = level.CheckNotRun
	}
	if o.Event != level.CheckPassed && d.engineEnded() {
		shared.For(ctx, d.cfg.Logger).Info("sandbox check", "event", "not-counted", "why", "engine-stopping")
		return CheckState{Observation: unwaited(), At: d.reg.now()}
	}
	st := CheckState{Observation: o, At: d.reg.now(), Ran: true}
	d.checks.mu.Lock()
	d.checks.last = st
	d.checks.mu.Unlock()
	shared.For(ctx, d.cfg.Logger).Info("sandbox check", "event", string(o.Event))
	if o.Event != level.CheckPassed {
		d.observeAll(o)
	}
	if o.Event == level.CheckFailed {
		d.sandboxFailed()
	}
	if o.Event == level.CheckPassed {
		d.reg.CheckPassed()
	}
	d.saveRuns()
	return st
}

func unwaited() level.Observation {
	return level.Observation{By: level.Engine, Event: level.CheckNotRun, Parts: []string{"the engine stopped waiting for it before it finished"}}
}

func (d *Dispatcher) startCheck(ctx context.Context, box string) (CheckState, error) {
	st := d.check(ctx)
	if d.cfg.Sandbox != nil && st.Observation.Event == level.CheckPassed {
		bctx, cancel := context.WithTimeout(ctx, checkFor)
		o := d.boxCheck(bctx, box, nil, nil)
		cancel()
		if o.Event != level.CheckPassed && ctx.Err() != nil {
			o = unwaited()
		}
		shared.For(ctx, d.cfg.Logger).Info("sandbox check", "box", box, "event", string(o.Event))
		st = CheckState{Observation: o, At: d.reg.now(), Ran: true}
	}
	o := st.Observation
	if d.cfg.Sandbox == nil || o.Event == level.CheckPassed {
		return st, nil
	}
	why := "the sandbox check failed"
	if o.Event == level.CheckNotRun {
		why = "the sandbox check couldn't run"
	}
	if len(o.Parts) > 0 {
		why += ": " + strings.Join(o.Parts, "; ")
	}
	return st, explained(conflict("sandbox"), clip("No session starts while "+why+".", maxOutput))
}

func (d *Dispatcher) observeStart(sid string, st CheckState) {
	d.reg.Observe(sid, st.Observation)
	last := d.LastCheck()
	if last.Ran && last.At.After(st.At) && last.Observation.Event != level.CheckPassed {
		d.reg.Observe(sid, last.Observation)
	}
}

func (d *Dispatcher) observeAll(o level.Observation) {
	for _, s := range d.reg.Sessions() {
		if s.Verified && (s.Running || o.Event == level.CheckFailed) {
			d.reg.Observe(s.SID, o)
		}
	}
}

func (d *Dispatcher) Recheck(ctx context.Context) {
	if d.cfg.Sandbox == nil {
		return
	}
	go d.round(ctx)
}

func (d *Dispatcher) Watch(ctx context.Context, every time.Duration) {
	if d.cfg.Sandbox == nil {
		return
	}
	if every <= 0 {
		every = CheckEvery
	}
	d.checks.mu.Lock()
	d.checks.engine = ctx
	d.checks.mu.Unlock()
	d.round(ctx)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.round(ctx)
		}
	}
}
