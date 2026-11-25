package server

import (
	"context"
	"maps"
	"net"
	"net/netip"
	"slices"
	"time"

	"github.com/bezmoradi/software-factory/internal/adapters/access/actions"
	"github.com/bezmoradi/software-factory/internal/adapters/access/sshsign"
	"github.com/bezmoradi/software-factory/internal/adapters/access/sts"
	"github.com/bezmoradi/software-factory/internal/adapters/egress"
	"github.com/bezmoradi/software-factory/internal/adapters/git/state"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	refusalsEvery    = 5 * time.Second
	actionSweepEvery = 5 * time.Minute
	refusalsFor      = 5 * time.Second
)

func useGrants(base context.Context, cfg PageConfig, allow *session.Allowlist, d *session.Dispatcher, reg *session.Registry, repos *session.Repos, routes *boxRoutes, helper session.Boxes, logger shared.Logger) (*session.Grants, error) {
	if allow == nil || routes == nil || routes.control == nil {
		return nil, nil
	}
	acts := session.ActionsConfig{Base: base}
	if host, ok := helper.(session.ActionHost); ok && cfg.Actions != "" {
		acts = session.ActionsConfig{Store: state.NewActions(cfg.StateDir), Host: host, Files: actions.Files{Dir: cfg.Actions}, Net: routes, Base: base}
	} else {
		logger.Info("actions", "event", "off")
	}
	g, err := session.NewGrants(session.GrantsConfig{
		Store: state.NewGrants(cfg.StateDir), Creds: state.NewCredentials(cfg.StateDir), Presets: state.NewPresets(cfg.StateDir), SSH: sshsign.Keys{}, AWS: sts.New(), Allow: allow,
		Session: d.GrantTarget, Targets: d.GrantTargets, InBox: d.BoxSession, Host: routes.handoverHost, Projects: projectNames(repos), Stack: routes.ownNetworks, GitAddress: routes.known.GitHost, Apply: routes.syncGrants,
		Show: reg.SetAccess, Note: reg.NotesFor("access"), Actions: acts, Audit: state.NewAudit(cfg.StateDir), Logger: logger,
	})
	if err != nil {
		return nil, err
	}
	repos.OnRemoved(func(id session.ProjectID) { g.ForgetProject(base, id) })
	go routes.known.Watch(base, net.DefaultResolver, allow.GitNames)
	routes.mu.Lock()
	routes.grants = g
	routes.mu.Unlock()
	return g, nil
}

func (b *boxRoutes) grantsOf() *session.Grants {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.grants
}

func (b *boxRoutes) grantLists() map[netip.Prefix]session.EgressList {
	out := map[netip.Prefix]session.EgressList{}
	g := b.grantsOf()
	if g == nil {
		return out
	}
	b.mu.Lock()
	entries := make([]session.BoxInfo, 0, len(b.hosts))
	for _, e := range b.hosts {
		entries = append(entries, e.info)
	}
	b.mu.Unlock()
	for _, info := range entries {
		if l := g.ForBox(info.Box); len(l.Allow) > 0 || len(l.Ports) > 0 {
			out[info.Subnet] = l
		}
	}
	return out
}

func sameGrants(a, b map[netip.Prefix]session.EgressList) bool {
	return maps.EqualFunc(a, b, session.SameList)
}

func (b *boxRoutes) syncGrants(ctx context.Context) error {
	want := b.grantLists()
	b.mu.Lock()
	same := b.sent != nil && sameGrants(want, b.sent)
	b.mu.Unlock()
	if same {
		return nil
	}
	return b.sendSubnets(ctx)
}

func (b *boxRoutes) GrantsAt(subnet netip.Prefix) session.EgressList {
	out := session.EgressList{Allow: []string{}, Ports: []string{}, Expires: map[string]time.Time{}}
	g := b.grantsOf()
	if g == nil {
		return out
	}
	b.mu.Lock()
	box := ""
	for _, e := range b.hosts {
		if e.info.Subnet == subnet {
			box = e.info.Box
		}
	}
	b.mu.Unlock()
	if box == "" {
		return out
	}
	return g.ForBox(box)
}

func (b *boxRoutes) ResendGrants(ctx context.Context) error {
	return b.sendSubnets(ctx)
}

func (b *boxRoutes) watchRefusals(ctx context.Context) {
	t := time.NewTicker(refusalsEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.readRefusals(ctx)
		}
	}
}

func (b *boxRoutes) readRefusals(ctx context.Context) {
	g := b.grantsOf()
	b.mu.Lock()
	up := map[netip.Prefix]boxEntry{}
	for _, e := range b.hosts {
		up[e.info.Subnet] = e
	}
	b.mu.Unlock()
	if g == nil || b.control == nil || len(up) == 0 {
		return
	}
	rctx, cancel := context.WithTimeout(ctx, refusalsFor)
	list, err := b.control.SubnetRefused(rctx)
	cancel()
	b.mu.Lock()
	was := b.refusing
	b.refusing = err != nil
	b.mu.Unlock()
	if err != nil {
		if !was {
			b.logger.Warn("grants", "event", "refusals-not-read")
		}
		return
	}
	for _, r := range list {
		e, ok := up[r.Subnet]
		if !ok || r.Last.Before(e.up.Truncate(time.Second)) {
			continue
		}
		g.Tried(e.info.Box, r.Host, r.Last)
	}
}

func (b *boxRoutes) handoverHost(box string) (session.HandoverHost, bool) {
	h, ok := b.Host(box)
	if !ok {
		return nil, false
	}
	hh, ok := h.(session.HandoverHost)
	return hh, ok
}

func (b *boxRoutes) gitNames() []string {
	if b.allow == nil {
		return nil
	}
	var out []string
	for _, n := range b.allow.GitNames() {
		if _, err := netip.ParseAddr(n); err != nil && len(out) < session.MaxEgressEntries {
			out = append(out, n)
		}
	}
	return out
}

func (b *boxRoutes) ownNetworks() []netip.Prefix {
	out := slices.Clone(b.pool)
	for _, a := range b.known.DockerHosts() {
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return out
	}
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip, ok := netip.AddrFromSlice(n.IP)
		ones, bits := n.Mask.Size()
		if ip.Is4In6() && bits == 128 {
			ones -= 96
		}
		p := netip.PrefixFrom(ip.Unmap(), ones).Masked()
		if !ok || !p.IsValid() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || slices.ContainsFunc(out, func(q netip.Prefix) bool { return q.Contains(p.Addr()) && q.Bits() <= p.Bits() }) {
			continue
		}
		out = append(out, p)
	}
	if len(out) > egress.MaxStack {
		out = out[:egress.MaxStack]
	}
	return out
}

func projectNames(repos *session.Repos) func() []string {
	return func() []string {
		list, err := repos.List()
		if err != nil {
			return nil
		}
		names := make([]string, 0, len(list))
		for _, r := range list {
			names = append(names, r.Name)
		}
		return names
	}
}
