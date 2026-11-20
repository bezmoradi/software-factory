package boxes

import (
	"context"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

func (h *Helper) Sweep(ctx context.Context) error {
	var list []containerListJSON
	if err := h.docker.get(ctx, "/containers/json", url.Values{"all": {"true"}, "filters": h.stackFilter()["filters"]}, &list); err != nil {
		return dockerFailed("list the stack's boxes", err)
	}
	running := map[string]bool{}
	for _, c := range list {
		name := ""
		if len(c.Names) > 0 {
			name = c.Names[0]
		}
		box, ok := h.ownsContainer(ctx, name, c.ImageID, c.Labels)
		if !ok {
			if twin, renamed := renamedBox(h.rec.project, c); renamed {
				h.sweepRenamed(ctx, twin, name, c)
				continue
			}
			if _, reviewer := reviewerLabelled(h.rec.project, c.Labels); reviewer {
				continue
			}
			if _, verifier := verifierLabelled(h.rec.project, c.Labels); verifier {
				continue
			}
			h.logger.Warn("box sweep", "event", "left_alone", "kind", "container", "name", name, "reason", "it carries the stack's label without every one of the helper's labels and names")
			continue
		}
		if c.State == stateRunning {
			running[box] = true
			continue
		}
		if h.cfg.SweepDry {
			h.logger.Info("box sweep", "event", "would_remove", "kind", "container", "name", name, "box", box, "state", c.State)
			continue
		}
		unlock := h.locks.lock(box)
		err := h.dropContainer(ctx, c.ID)
		unlock()
		h.logResult(ctx, "sweep_container", box, c.Labels[LabelRecipe], err)
	}
	var nets []networkInspectJSON
	if err := h.docker.get(ctx, "/networks", h.stackFilter(), &nets); err != nil {
		return dockerFailed("list the stack's networks", err)
	}
	for _, n := range nets {
		box, ok := ownedName(h.rec.project, boxPrefix, n.Name, n.Labels)
		if !ok {
			if _, reviewer := ownedReviewer(h.rec.project, n.Name, n.Labels); reviewer {
				continue
			}
			h.logger.Warn("box sweep", "event", "left_alone", "kind", "network", "name", n.Name, "reason", "it carries the stack's label without every one of the helper's labels and names")
			continue
		}
		if running[box] {
			continue
		}
		if h.cfg.SweepDry {
			h.logger.Info("box sweep", "event", "would_remove", "kind", "network", "name", n.Name, "box", box)
			continue
		}
		unlock := h.locks.lock(box)
		fresh, err := h.boxNetwork(ctx, box)
		if err == nil && fresh != nil {
			err = h.dropNetwork(ctx, fresh)
		}
		unlock()
		h.logResult(ctx, "sweep_network", box, "", err)
	}
	if v, err := h.stackVolume(ctx, volumeWork); err == nil {
		if found, err := h.strays(ctx, v); err == nil && len(found) > 0 {
			h.logger.Warn("box sweep", "event", "workspace_shared", "containers", len(found), "names", strings.Join(found, ","), "fix", "remove them, such as an earlier version's agent container (docker compose up -d --remove-orphans); no session starts until they are gone")
		}
	}
	h.SweepReviewers(ctx, h.cfg.SweepDry)
	h.SweepVerifiers(ctx, h.cfg.SweepDry)
	h.Reconnect(ctx)
	return nil
}

func (h *Helper) sweepRenamed(ctx context.Context, box, name string, c containerListJSON) {
	if h.cfg.SweepDry {
		h.logger.Info("box sweep", "event", "would_remove", "kind", "renamed_container", "name", name, "box", box, "state", c.State)
		return
	}
	unlock := h.locks.lock(box)
	err := h.dropContainer(ctx, c.ID)
	unlock()
	h.logResult(ctx, "sweep_renamed", box, "", err)
}

func (h *Helper) runningBoxes(ctx context.Context) []string {
	var list []containerListJSON
	if err := h.docker.get(ctx, "/containers/json", h.stackFilter(), &list); err != nil {
		h.logger.Warn("box helper", "event", "list_failed", "error", oneLine(err.Error()))
		return nil
	}
	var out []string
	for _, c := range list {
		if len(c.Names) == 0 || c.State != stateRunning {
			continue
		}
		if box, ok := h.ownsContainer(ctx, c.Names[0], c.ImageID, c.Labels); ok {
			out = append(out, box)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func (h *Helper) Reconnect(ctx context.Context) {
	m := h.membersOf(ctx)
	if m.engine == "" && m.egress == "" {
		return
	}
	h.reconnectReviewers(ctx, m)
	for _, box := range h.runningBoxes(ctx) {
		unlock := h.locks.lock(box)
		n, err := h.boxNetwork(ctx, box)
		if err == nil && n != nil {
			if subnet, ok := boxSubnet(n); ok {
				if m.engine != "" {
					if err := h.ensure(ctx, n, m.engine, engineOf(subnet)); err != nil {
						h.logResult(ctx, "reconnect_engine", box, "", err)
					}
				}
				if m.egress != "" {
					if err := h.ensure(ctx, n, m.egress, egressOf(subnet)); err != nil {
						h.logResult(ctx, "reconnect_egress", box, "", err)
					}
				}
			}
		}
		unlock()
	}
}

func (h *Helper) nudge() {
	select {
	case h.kick <- struct{}{}:
	default:
	}
}

func (h *Helper) Watch(ctx context.Context) {
	go h.watchVerifiers(ctx)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-h.kick:
				h.Reconnect(ctx)
			}
		}
	}()
	q := filters(map[string][]string{"type": {"container"}, "event": {"start"}, "label": {labelProject + "=" + h.rec.project}})
	for {
		err := h.docker.Events(ctx, q, func(ev eventJSON) {
			a := ev.Actor.Attributes
			if ev.Type != "container" || ev.Action != "start" || a[labelProject] != h.rec.project || a[labelOneOff] == oneOffTrue {
				return
			}
			if s := a[labelService]; s == engineService || s == egressService {
				h.nudge()
			}
		})
		if ctx.Err() != nil {
			return
		}
		h.logger.Warn("box helper", "event", "events_ended", "error", oneLine(err.Error()))
		t := time.NewTimer(2 * time.Second)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		h.nudge()
	}
}

func (h *Helper) StopAll(ctx context.Context) {
	boxes := map[string]bool{}
	var list []containerListJSON
	if err := h.docker.get(ctx, "/containers/json", url.Values{"all": {"true"}, "filters": h.stackFilter()["filters"]}, &list); err == nil {
		for _, c := range list {
			if len(c.Names) > 0 {
				if box, ok := h.ownsContainer(ctx, c.Names[0], c.ImageID, c.Labels); ok {
					boxes[box] = true
				}
			}
			if box, renamed := renamedBox(h.rec.project, c); renamed {
				boxes[box] = true
			}
		}
	} else {
		h.logger.Warn("box helper", "event", "list_failed", "error", oneLine(err.Error()))
	}
	var nets []networkInspectJSON
	if err := h.docker.get(ctx, "/networks", h.stackFilter(), &nets); err == nil {
		for _, n := range nets {
			if box, ok := ownedName(h.rec.project, boxPrefix, n.Name, n.Labels); ok {
				boxes[box] = true
			}
		}
	}
	var wg sync.WaitGroup
	for box := range boxes {
		wg.Go(func() {
			if err := h.Stop(ctx, box); err != nil {
				h.logger.Warn("box helper", "event", "stop_on_exit_failed", "box", box, "error", oneLine(err.Error()))
			}
		})
	}
	wg.Go(func() { h.SweepReviewers(ctx, false) })
	wg.Go(func() { h.SweepVerifiers(ctx, false) })
	wg.Wait()
	h.logger.Info("box helper", "event", "stopped_every_box", "boxes", len(boxes))
}
