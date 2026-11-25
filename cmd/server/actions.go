package server

import (
	"context"
	"errors"
	"net/netip"
	"slices"

	"github.com/bezmoradi/software-factory/internal/adapters/sandbox/boxes"
	"github.com/bezmoradi/software-factory/internal/core/session"
)

type actionRoute struct {
	subnet netip.Prefix
	list   session.EgressList
}

var _ session.ActionNet = (*boxRoutes)(nil)

func (b *boxRoutes) actionSubnetsLocked() ([]netip.Prefix, map[netip.Prefix]session.EgressList) {
	out := make([]netip.Prefix, 0, len(b.actions))
	lists := map[netip.Prefix]session.EgressList{}
	for _, a := range b.actions {
		out = append(out, a.subnet)
		lists[a.subnet] = a.list
	}
	slices.SortFunc(out, func(x, y netip.Prefix) int { return x.Addr().Compare(y.Addr()) })
	return out, lists
}

func (b *boxRoutes) OpenAction(ctx context.Context, run string, subnet netip.Prefix, list session.EgressList) error {
	if !boxes.InPool(b.pool, subnet) {
		return errors.New("the box helper put the action container on " + subnet.String() + ", outside SF_BOX_SUBNETS")
	}
	b.mu.Lock()
	for _, e := range b.hosts {
		if e.info.Subnet.Overlaps(subnet) {
			b.mu.Unlock()
			return errors.New("the action container's network overlaps a session container's")
		}
	}
	for _, p := range b.reviews {
		if p.Overlaps(subnet) {
			b.mu.Unlock()
			return errors.New("the action container's network overlaps a reviewer container's")
		}
	}
	b.actions[run] = actionRoute{subnet: subnet, list: list}
	b.mu.Unlock()
	if err := b.sendSubnets(ctx); err != nil {
		b.mu.Lock()
		delete(b.actions, run)
		b.mu.Unlock()
		b.logger.Warn("actions", "event", "egress-not-set", "run", run)
		return errors.New("egress didn't take the action container's network")
	}
	return nil
}

func (b *boxRoutes) CloseAction(ctx context.Context, run string) {
	b.mu.Lock()
	_, had := b.actions[run]
	delete(b.actions, run)
	b.mu.Unlock()
	if !had {
		return
	}
	if err := b.sendSubnets(context.WithoutCancel(ctx)); err != nil {
		b.logger.Warn("actions", "event", "egress-not-set", "run", run)
	}
}
