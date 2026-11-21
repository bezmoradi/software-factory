package session

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/level"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	BoxStopReason = "the sandbox check of the session's container failed"
	boxListFor    = 15 * time.Second
	boxSeenFor    = 3 * time.Second
	maxCheckParts = 6
)

type BoxProbe struct {
	Box     string
	Host    Host
	Dir     string
	Subnet  netip.Prefix
	Gateway netip.Addr
}

type boxFact struct {
	key, want, why string
}

var boxFacts = []boxFact{
	{"recipe", "verified", "the session's container doesn't match the stack's recipe"},
	{"own-network-only", "true", "the session's container is attached to networks besides its own"},
	{"network-internal", "true", "the session's network isn't internal"},
	{"network-ipv6", "false", "the session's network has IPv6 switched on"},
	{"gateway-mode", "isolated", "the session's network gives its gateway an address"},
	{"readonly-root", "true", "the session's container has a writable root file system"},
	{"privileged", "false", "the session's container is privileged"},
	{"cap-add", "none", "the session's container was given capabilities"},
	{"runtime-ready", "true", "the container runtime SF_BOX_RUNTIME asks for isn't installed in Docker"},
}

func failedCheck(parts []string) level.Observation {
	parts = slices.Compact(parts)
	if len(parts) > maxCheckParts {
		parts = append(parts[:maxCheckParts-1], "and "+strconv.Itoa(len(parts)-maxCheckParts+1)+" more")
	}
	return level.Observation{By: level.Engine, Event: level.CheckFailed, Parts: parts}
}

func factsOf(list []string) map[string]string {
	out := map[string]string{}
	for _, f := range list {
		if k, v, ok := strings.Cut(f, "="); ok {
			out[k] = v
		}
	}
	return out
}

func BoxFactProblems(info BoxInfo) []string {
	var parts []string
	if info.State != "running" {
		parts = append(parts, "the box helper reports the session's container as "+clip(info.State, 40))
	}
	facts := factsOf(info.Facts)
	if _, missing := facts["network"]; missing {
		parts = append(parts, "the session's network is missing")
	}
	for _, f := range boxFacts {
		got, ok := facts[f.key]
		if ok && got == f.want {
			continue
		}
		why := f.why
		if !ok {
			why += " (the box helper didn't say)"
		} else if f.key == "own-network-only" {
			why += ": " + clip(facts["networks"], 200)
		} else {
			why += " (" + f.key + "=" + clip(got, 40) + ")"
		}
		parts = append(parts, why)
	}
	if !info.Subnet.IsValid() || !info.Gateway.IsValid() || !info.Subnet.Contains(info.Gateway) {
		parts = append(parts, "the box helper reported no gateway address on the session's network")
	}
	return parts
}

func (d *Dispatcher) listBoxes(ctx context.Context) (map[string]BoxInfo, error) {
	lctx, cancel := context.WithTimeout(ctx, boxListFor)
	defer cancel()
	list, err := d.cfg.Boxes.List(lctx)
	if err != nil {
		d.forgetSeen()
		return nil, err
	}
	out := make(map[string]BoxInfo, len(list))
	for _, b := range list {
		if ValidBox(b.Box) {
			out[b.Box] = b
		}
	}
	d.boxes.seenMu.Lock()
	d.boxes.seen, d.boxes.seenAt = out, d.reg.now()
	d.boxes.seenMu.Unlock()
	return out, nil
}

func (d *Dispatcher) forgetSeen() {
	d.boxes.seenMu.Lock()
	d.boxes.seen, d.boxes.seenAt = nil, time.Time{}
	d.boxes.seenMu.Unlock()
}

func (d *Dispatcher) seenBoxes(ctx context.Context) (map[string]BoxInfo, error) {
	d.boxes.seenMu.Lock()
	seen, at := d.boxes.seen, d.boxes.seenAt
	d.boxes.seenMu.Unlock()
	if age := d.reg.now().Sub(at); seen != nil && age >= 0 && age < boxSeenFor {
		return seen, nil
	}
	return d.listBoxes(ctx)
}

func routedAt(listed map[string]BoxInfo, known BoxInfo) bool {
	info, ok := listed[known.Box]
	return ok && info.State == "running" && info.Subnet == known.Subnet && info.Agent == known.Agent
}

func (d *Dispatcher) ConfirmBox(ctx context.Context, box string) error {
	if !d.boxed() || !ValidBox(box) {
		return conflict("box stopped", "The session's container isn't running.")
	}
	known, ok := d.cfg.Routes.Info(box)
	if !ok {
		return conflict("box stopped", "The session's container isn't running. Wake or start the session again.")
	}
	listed, err := d.seenBoxes(ctx)
	if err != nil {
		return conflict("box helper", "The box helper didn't answer, so the engine can't confirm which container answers at the session's address, and it doesn't reach it.")
	}
	if !routedAt(listed, known) {
		d.cfg.Routes.Down(box)
		shared.For(ctx, d.cfg.Logger).Warn("boxes", "event", "stale-route-dropped", "box", box)
		return conflict("box stopped", "The session's container isn't running at the address the engine had for it, so the engine dropped that route. Wake or start the session again.")
	}
	return nil
}

func (d *Dispatcher) pruneRoutes(listed map[string]BoxInfo) {
	if listed == nil {
		return
	}
	for _, known := range d.cfg.Routes.Routed() {
		if !routedAt(listed, known) {
			d.cfg.Routes.Down(known.Box)
			d.cfg.Logger.Warn("boxes", "event", "stale-route-dropped", "box", known.Box)
		}
	}
}

func (d *Dispatcher) boxCheck(ctx context.Context, box string, listed map[string]BoxInfo, listErr error) level.Observation {
	if !d.boxed() || !ValidBox(box) {
		return failedCheck([]string{"the session has no container of its own to check"})
	}
	if listed == nil && listErr == nil {
		listed, listErr = d.listBoxes(ctx)
	}
	if listErr != nil {
		return failedCheck([]string{"the box helper didn't answer, so the session's container couldn't be checked from outside it"})
	}
	info, ok := listed[box]
	if !ok {
		return failedCheck([]string{"the box helper doesn't list the session's container"})
	}
	parts := BoxFactProblems(info)
	if k, has := d.cfg.Routes.Info(box); !has || k.Subnet != info.Subnet || k.Agent != info.Agent {
		parts = append(parts, "the session's container isn't on the network the engine routes it through")
	}
	host, live := d.cfg.Routes.Host(box)
	if !live || host == nil {
		parts = append(parts, "the engine has no route to the session's container")
	}
	if len(parts) > 0 {
		return failedCheck(parts)
	}
	o := d.cfg.Sandbox.CheckBox(ctx, BoxProbe{Box: box, Host: host, Dir: BoxFolder(d.cfg.Workspace, box), Subnet: info.Subnet, Gateway: info.Gateway})
	o.By = level.Engine
	if o.Event != level.CheckPassed && o.Event != level.CheckFailed {
		o.Event = level.CheckNotRun
	}
	return o
}

func (d *Dispatcher) round(ctx context.Context) {
	d.retryStops(ctx)
	d.sweepLeftovers(ctx)
	d.dropPending(false)
	var listed map[string]BoxInfo
	listErr := errors.New("the engine has no box helper")
	if d.boxed() {
		if listed, listErr = d.listBoxes(ctx); listErr == nil {
			d.pruneRoutes(listed)
		}
	}
	st := d.check(ctx)
	if d.cfg.Sandbox == nil || st.Observation.Event != level.CheckPassed {
		return
	}
	type target struct{ sid, box string }
	var targets []target
	for _, s := range d.reg.Sessions() {
		if !s.Verified || !s.Running {
			continue
		}
		box, _ := d.SessionBox(s.SID)
		targets = append(targets, target{sid: s.SID, box: box})
	}
	if len(targets) == 0 {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, checkFor)
	defer cancel()
	var wg sync.WaitGroup
	for _, t := range targets {
		wg.Go(func() {
			o := d.boxCheck(cctx, t.box, listed, listErr)
			if o.Event != level.CheckPassed && ctx.Err() != nil {
				return
			}
			d.reg.Observe(t.sid, o)
			if o.Event != level.CheckPassed {
				shared.For(ctx, d.cfg.Logger).Warn("sandbox check", "box", t.box, "event", string(o.Event))
				d.boxFailed(t.sid)
			}
		})
	}
	wg.Wait()
	d.saveRuns()
}

func (d *Dispatcher) boxFailed(sid string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), stopEveryFor)
		defer cancel()
		d.stopOne(ctx, sid, BoxStopReason)
	}()
}
