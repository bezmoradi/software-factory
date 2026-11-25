package server

import (
	"context"
	"errors"
	"maps"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/adapters/agenthost"
	"github.com/bezmoradi/software-factory/internal/adapters/egress"
	"github.com/bezmoradi/software-factory/internal/adapters/git/files"
	"github.com/bezmoradi/software-factory/internal/adapters/http/page"
	"github.com/bezmoradi/software-factory/internal/adapters/sandbox/boxes"
	"github.com/bezmoradi/software-factory/internal/adapters/sandbox/docker"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	subnetsFor   = 10 * time.Second
	subnetsEvery = 30 * time.Second
	helperFor    = 5 * time.Second
	helperEvery  = 10 * time.Second
)

type boxEntry struct {
	info session.BoxInfo
	host agentHost
	up   time.Time
}

type boxRoutes struct {
	mu       sync.Mutex
	sendMu   sync.Mutex
	hosts    map[string]boxEntry
	reviews  map[string]netip.Prefix
	actions  map[string]actionRoute
	listener *page.BoxListener
	control  *docker.Control
	reg      *session.Registry
	allow    *session.Allowlist
	logger   shared.Logger
	pool     []netip.Prefix
	ping     func(context.Context) error
	down     bool
	grants   *session.Grants
	sent     map[netip.Prefix]session.EgressList
	refusing bool
	known    *egress.Known
}

var _ session.BoxRoutes = (*boxRoutes)(nil)

func newBoxRoutes(reg *session.Registry, allow *session.Allowlist, control *docker.Control, pool []netip.Prefix, ping func(context.Context) error, logger shared.Logger) *boxRoutes {
	b := &boxRoutes{hosts: map[string]boxEntry{}, reviews: map[string]netip.Prefix{}, actions: map[string]actionRoute{}, reg: reg, allow: allow, control: control, pool: pool, ping: ping, logger: logger, known: egress.NewKnown()}
	if allow != nil {
		allow.OnRestart(func() { go b.resend() })
	}
	return b
}

func (b *boxRoutes) resend() {
	if err := b.sendSubnets(context.Background()); err != nil {
		b.logger.Warn("boxes", "event", "egress-not-set", "why", "egress-restarted")
		return
	}
	b.logger.Info("boxes", "event", "egress-boxes-resent")
}

func (b *boxRoutes) helperDown() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.down
}

func (b *boxRoutes) checkHelper(ctx context.Context) {
	if b.ping == nil {
		return
	}
	pctx, cancel := context.WithTimeout(ctx, helperFor)
	err := b.ping(pctx)
	cancel()
	if ctx.Err() != nil {
		return
	}
	down := err != nil
	b.mu.Lock()
	was := b.down
	b.down = down
	b.mu.Unlock()
	if down != was {
		b.logger.Info("boxes", "event", "helper-reachable", "ok", !down)
	}
}

func (b *boxRoutes) useListener(l *page.BoxListener) {
	b.mu.Lock()
	b.listener = l
	entries := make([]boxEntry, 0, len(b.hosts))
	for _, e := range b.hosts {
		entries = append(entries, e)
	}
	b.mu.Unlock()
	for _, e := range entries {
		l.Allow(e.info.Box, e.info.Engine, e.info.Agent)
	}
}

func (b *boxRoutes) subnetsLocked() []netip.Prefix {
	out := make([]netip.Prefix, 0, len(b.hosts))
	for _, e := range b.hosts {
		out = append(out, e.info.Subnet)
	}
	slices.SortFunc(out, func(x, y netip.Prefix) int { return x.Addr().Compare(y.Addr()) })
	return out
}

func (b *boxRoutes) reviewSubnetsLocked() []netip.Prefix {
	out := make([]netip.Prefix, 0, len(b.reviews))
	for _, p := range b.reviews {
		out = append(out, p)
	}
	slices.SortFunc(out, func(x, y netip.Prefix) int { return x.Addr().Compare(y.Addr()) })
	return out
}

func (b *boxRoutes) reviewerUp(ctx context.Context, run string, subnet netip.Prefix) error {
	if !boxes.InPool(b.pool, subnet) {
		b.logger.Warn("reviewers", "event", "subnet-outside-pool", "run", run)
		return errors.New("the box helper put the reviewer container on " + subnet.String() + ", outside SF_BOX_SUBNETS, so the engine doesn't use it")
	}
	b.mu.Lock()
	var stale []string
	for box, e := range b.hosts {
		if e.info.Subnet.Overlaps(subnet) {
			stale = append(stale, box)
		}
	}
	for r, p := range b.reviews {
		if r != run && p.Overlaps(subnet) {
			delete(b.reviews, r)
		}
	}
	b.reviews[run] = subnet
	b.mu.Unlock()
	slices.Sort(stale)
	for _, box := range stale {
		b.logger.Warn("boxes", "event", "stale-route-evicted", "box", box, "by", run)
		b.Down(box)
	}
	if err := b.sendSubnets(ctx); err != nil {
		b.logger.Warn("reviewers", "event", "egress-not-set", "run", run)
		return errors.New("egress didn't take the reviewer container's network: " + err.Error())
	}
	return nil
}

func (b *boxRoutes) reviewerDown(ctx context.Context, run string) {
	b.mu.Lock()
	_, had := b.reviews[run]
	delete(b.reviews, run)
	b.mu.Unlock()
	if !had {
		return
	}
	if err := b.sendSubnets(context.WithoutCancel(ctx)); err != nil {
		shared.For(ctx, b.logger).Warn("reviewers", "event", "egress-not-set", "run", run)
	}
}

func (b *boxRoutes) sendSubnets(ctx context.Context) error {
	if b.control == nil {
		return nil
	}
	b.sendMu.Lock()
	defer b.sendMu.Unlock()
	b.mu.Lock()
	subnets, reviews := b.subnetsLocked(), b.reviewSubnetsLocked()
	acts, lists := b.actionSubnetsLocked()
	b.mu.Unlock()
	grants := b.grantLists()
	sent := maps.Clone(grants)
	maps.Copy(grants, lists)
	sctx, cancel := context.WithTimeout(ctx, subnetsFor)
	defer cancel()
	if err := b.control.SetBoxes(sctx, subnets, reviews, acts, grants, b.ownNetworks(), b.gitNames()); err != nil {
		return err
	}
	grants = sent
	b.mu.Lock()
	b.sent = grants
	b.mu.Unlock()
	return nil
}

func (b *boxRoutes) Up(ctx context.Context, info session.BoxInfo) error {
	if !session.ValidBox(info.Box) || info.Host == "" {
		return errors.New("the box helper's answer names no agent-host")
	}
	if !boxes.InPool(b.pool, info.Subnet) {
		b.logger.Warn("boxes", "event", "subnet-outside-pool", "box", info.Box)
		return errors.New("the box helper put the session's container on " + info.Subnet.String() + ", outside SF_BOX_SUBNETS, so the engine doesn't route to it")
	}
	client, err := agenthost.NewClient(info.Host)
	if err != nil {
		return err
	}
	b.mu.Lock()
	var stale []string
	for box, e := range b.hosts {
		if box != info.Box && (e.info.Subnet.Overlaps(info.Subnet) || e.info.Agent == info.Agent) {
			stale = append(stale, box)
		}
	}
	for run, p := range b.reviews {
		if p.Overlaps(info.Subnet) {
			delete(b.reviews, run)
		}
	}
	b.mu.Unlock()
	slices.Sort(stale)
	for _, box := range stale {
		b.logger.Warn("boxes", "event", "stale-route-evicted", "box", box, "by", info.Box)
		b.Down(box)
	}
	b.mu.Lock()
	b.hosts[info.Box] = boxEntry{info: info, host: withGateway(client, b.allow), up: time.Now()}
	l := b.listener
	b.mu.Unlock()
	if l != nil {
		l.Allow(info.Box, info.Engine, info.Agent)
	}
	if err := b.sendSubnets(ctx); err != nil {
		b.logger.Warn("boxes", "event", "egress-not-set", "box", info.Box)
		return errors.New("egress didn't take the session container's network: " + err.Error())
	}
	return nil
}

func (b *boxRoutes) Down(box string) {
	b.mu.Lock()
	_, had := b.hosts[box]
	delete(b.hosts, box)
	l := b.listener
	b.mu.Unlock()
	closed := 0
	if l != nil {
		closed = l.Drop(box)
	}
	dropped := b.reg.DropBox(box)
	if had {
		b.logger.Info("boxes", "event", "unrouted", "box", box, "connections", closed, "registrations", dropped)
	}
	if err := b.sendSubnets(context.Background()); err != nil {
		b.logger.Warn("boxes", "event", "egress-not-set", "box", box)
	}
	if g := b.grantsOf(); g != nil && had {
		go g.Tend(context.Background())
	}
}

func (b *boxRoutes) Host(box string) (session.Host, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e, ok := b.hosts[box]
	if !ok {
		return nil, false
	}
	return e.host, true
}

func (b *boxRoutes) Routed() []session.BoxInfo {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]session.BoxInfo, 0, len(b.hosts))
	for _, e := range b.hosts {
		out = append(out, e.info)
	}
	slices.SortFunc(out, func(x, y session.BoxInfo) int { return strings.Compare(x.Box, y.Box) })
	return out
}

func (b *boxRoutes) Info(box string) (session.BoxInfo, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e, ok := b.hosts[box]
	return e.info, ok
}

func (b *boxRoutes) watch(ctx context.Context) {
	t := time.NewTicker(subnetsEvery)
	defer t.Stop()
	h := time.NewTicker(helperEvery)
	defer h.Stop()
	b.checkHelper(ctx)
	if err := b.sendSubnets(ctx); err != nil {
		b.logger.Warn("boxes", "event", "egress-not-set")
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.C:
			b.checkHelper(ctx)
		case <-t.C:
			b.mu.Lock()
			n := len(b.hosts) + len(b.reviews) + len(b.actions)
			b.mu.Unlock()
			if n > 0 {
				if err := b.sendSubnets(ctx); err != nil {
					b.logger.Warn("boxes", "event", "egress-not-set")
				}
			}
		}
	}
}

type boxFolders struct {
	workspace string
	uploads   *page.UploadDir
}

var _ session.BoxFolders = boxFolders{}

func (f boxFolders) Make(box string) error {
	if err := files.MakeBox(f.workspace, box); err != nil {
		return err
	}
	if f.uploads != nil {
		return f.uploads.MakeBox(box)
	}
	return nil
}

func (f boxFolders) Remove(box string) error {
	err := files.RemoveBox(f.workspace, box)
	if f.uploads != nil {
		if uerr := f.uploads.DropBox(box); err == nil {
			err = uerr
		}
	}
	return err
}
