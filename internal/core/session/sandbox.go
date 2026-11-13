package session

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/level"
)

const (
	CheckEvery = 5 * time.Minute
	checkFor   = 90 * time.Second
)

type Sandbox interface {
	Check(ctx context.Context) level.Observation
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
	mu   sync.Mutex
	run  sync.Mutex
	last CheckState
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
	ctx, cancel := context.WithTimeout(ctx, checkFor)
	defer cancel()
	o := d.cfg.Sandbox.Check(ctx)
	o.By = level.Engine
	if o.Event != level.CheckPassed && o.Event != level.CheckFailed {
		o.Event = level.CheckNotRun
	}
	st := CheckState{Observation: o, At: d.reg.now(), Ran: true}
	d.checks.mu.Lock()
	d.checks.last = st
	d.checks.mu.Unlock()
	d.cfg.Logger.Info("sandbox check", "event", string(o.Event))
	d.observeAll(o)
	d.saveRuns()
	return st
}

func (d *Dispatcher) startCheck(ctx context.Context) (CheckState, error) {
	st := d.check(ctx)
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
	go d.check(ctx)
}

func (d *Dispatcher) Watch(ctx context.Context, every time.Duration) {
	if d.cfg.Sandbox == nil {
		return
	}
	if every <= 0 {
		every = CheckEvery
	}
	d.check(ctx)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.check(ctx)
		}
	}
}
