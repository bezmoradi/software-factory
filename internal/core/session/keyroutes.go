package session

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	keyRouteFor     = 20 * time.Second
	maxRunningShown = 8
)

type KeyRoute struct {
	Saved   bool
	Gateway string
	Before  Expect
}

type KeyRoutes interface {
	Load() (map[string]KeyRoute, error)
	Save(map[string]KeyRoute) error
}

type GatewayRoutes interface {
	GatewayHost(side string) (string, bool)
	HoldGateway() func()
}

func (d *Dispatcher) UseGateways(g GatewayRoutes) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.gateways = g
}

func (d *Dispatcher) gatewayOf(side string) (string, bool) {
	d.mu.Lock()
	g := d.gateways
	d.mu.Unlock()
	if g == nil {
		return "", true
	}
	return g.GatewayHost(side)
}

func (d *Dispatcher) holdGateway() func() {
	d.mu.Lock()
	g := d.gateways
	d.mu.Unlock()
	if g == nil {
		return func() {}
	}
	return g.HoldGateway()
}

func targetSide(target string) string {
	if target == LoginReviewers {
		return SideReviewers
	}
	return SideAgents
}

func (d *Dispatcher) expectAgentOf(target string) string {
	if target == LoginReviewers {
		return ReviewerAgent
	}
	if box, ok := TargetBox(target); ok {
		return BoxTarget(box)
	}
	return "claude"
}

func (d *Dispatcher) containerWords(target string) string {
	if box, ok := TargetBox(target); ok {
		return "session's container (" + BoxContainer(box) + ")"
	}
	return "reviewer container"
}

func (d *Dispatcher) runsCheck(target string) string {
	if box, ok := TargetBox(target); ok {
		return "docker ps --filter name=" + BoxContainer(box)
	}
	return Compose() + " ps reviewer"
}

func (d *Dispatcher) keyRoutes() (map[string]KeyRoute, bool) {
	if d.cfg.KeyRoutes == nil {
		return map[string]KeyRoute{}, true
	}
	all, err := d.cfg.KeyRoutes.Load()
	if err != nil {
		return map[string]KeyRoute{}, false
	}
	if all == nil {
		all = map[string]KeyRoute{}
	}
	return all, true
}

func (d *Dispatcher) saveKeyRoute(target string, k KeyRoute) error {
	if d.cfg.KeyRoutes == nil {
		return nil
	}
	all, _ := d.keyRoutes()
	delete(all, SideAgents)
	all[target] = k
	return d.cfg.KeyRoutes.Save(all)
}

func keyMisrouted(k KeyRoute, known bool, host string, established bool) bool {
	switch {
	case !known:
		return !established || host != ""
	case !k.Saved:
		return false
	case !established:
		return k.Gateway != ""
	}
	return k.Gateway != host
}

func (d *Dispatcher) restoreExpect(target string, k KeyRoute, known bool) {
	agent := d.expectAgentOf(target)
	switch {
	case known && k.Saved:
		before := k.Before
		if !ValidExpect(before) {
			before = ExpectAny
		}
		_ = d.cfg.Expected.Set(agent, before)
	case !known && d.cfg.Expected.Of(agent) == ExpectAPIKey:
		_ = d.cfg.Expected.Set(agent, ExpectAny)
	}
}

func (d *Dispatcher) removeKeyLocked(ctx context.Context, target string, restore bool) error {
	all, readable := d.keyRoutes()
	k, present := all[target]
	if err := d.apiKey(ctx, target, "clear", ""); err != nil {
		return err
	}
	if restore {
		d.restoreExpect(target, k, readable && present)
	}
	if err := d.saveKeyRoute(target, KeyRoute{}); err != nil {
		d.cfg.Logger.Warn("claude api key", "target", target, "event", "record-not-saved")
	}
	return nil
}

func (d *Dispatcher) KeyRouted(ctx context.Context, target string) error {
	if !ValidLoginTarget(target) {
		return explained(invalid("target"), "")
	}
	if _, err := d.keyHost(ctx, target); err != nil {
		return nil
	}
	release := d.holdGateway()
	defer release()
	d.keyMu.Lock()
	defer d.keyMu.Unlock()
	all, readable := d.keyRoutes()
	k, present := all[target]
	host, established := d.gatewayOf(targetSide(target))
	if !keyMisrouted(k, readable && present, host, established) {
		return nil
	}
	kctx, cancel := context.WithTimeout(ctx, keyRouteFor)
	defer cancel()
	err := d.removeKeyLocked(kctx, target, true)
	d.cfg.Logger.Info("claude api key", "target", target, "step", "reconcile", "ok", err == nil)
	if err != nil {
		return explained(conflict("api key"), "Claude Code in the "+d.containerWords(target)+" may hold a key pasted for a gateway whose address the engine can't apply now, and the engine couldn't remove it, so Claude Code doesn't start there. Check that the container runs ("+d.runsCheck(target)+"), then try again: the engine tries to remove the key before every start, and a subscription sign-in in the sign-in panel removes it too.")
	}
	return nil
}

func (d *Dispatcher) ReconcileKeys(ctx context.Context) {
	if err := d.KeyRouted(ctx, LoginReviewers); err != nil {
		d.cfg.Logger.Warn("claude api key", "target", LoginReviewers, "event", "not-removed", "fix", Compose()+" ps reviewer")
	}
}

func (d *Dispatcher) RunningOn(side string) []string {
	var names []string
	if side == SideReviewers {
		if d.cfg.Reviews == nil {
			return nil
		}
		for _, b := range d.cfg.Reviews.busy() {
			if sid, ok := strings.CutPrefix(b, testListKey); ok {
				names = append(names, "the manual test list of "+d.shownName(sid))
			} else if agent, ok := strings.CutPrefix(b, reviewerCheckKey); ok {
				names = append(names, "the check of the reviewer "+clipWords(agent))
			} else {
				names = append(names, "the review of "+d.shownName(b))
			}
		}
	} else {
		d.mu.Lock()
		if d.starting {
			names = append(names, "a session that is starting")
		}
		for _, r := range d.runs {
			if r.spec.Method == MethodPlugin && (r.running || r.busy) {
				names = append(names, runName(r))
			}
		}
		d.mu.Unlock()
	}
	slices.Sort(names)
	return names
}

func runName(r *run) string {
	if r.display != "" {
		return clipWords(r.display) + " (" + r.name + ")"
	}
	return r.name
}

func (d *Dispatcher) shownName(sid string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if r := d.runs[sid]; r != nil {
		return runName(r)
	}
	return "a session"
}

func runningWords(names []string) string {
	if len(names) > maxRunningShown {
		more := len(names) - maxRunningShown + 1
		names = append(slices.Clone(names[:maxRunningShown-1]), "and "+strconv.Itoa(more)+" more")
	}
	return strings.Join(names, ", ")
}

const reviewerCheckKey = "check:"

func (r *Reviews) busy() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.running)+len(r.checking))
	for k := range r.running {
		out = append(out, k)
	}
	for agent := range r.checking {
		out = append(out, reviewerCheckKey+agent)
	}
	return out
}

func (r *Reviews) UseKeyGate(gate func(context.Context) error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keyGate = gate
}

func (r *Reviews) keyRouted(ctx context.Context) string {
	r.mu.Lock()
	gate := r.keyGate
	r.mu.Unlock()
	if gate == nil {
		return ""
	}
	if err := gate(ctx); err != nil {
		if e, ok := errors.AsType[*Error](err); ok && e.Message != "" {
			return e.Message
		}
		return "the engine couldn't check where Claude Code would send the saved key"
	}
	return ""
}
