package server

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/adapters/agenthost"
	"github.com/bezmoradi/software-factory/internal/adapters/git/files"
	"github.com/bezmoradi/software-factory/internal/adapters/http/page"
	"github.com/bezmoradi/software-factory/internal/adapters/sandbox/docker"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	subnetsFor   = 10 * time.Second
	subnetsEvery = 30 * time.Second
)

type boxEntry struct {
	info session.BoxInfo
	host agentHost
}

type boxRoutes struct {
	mu       sync.Mutex
	hosts    map[string]boxEntry
	listener *page.BoxListener
	control  *docker.Control
	reg      *session.Registry
	allow    *session.Allowlist
	logger   shared.Logger
}

var _ session.BoxRoutes = (*boxRoutes)(nil)

func newBoxRoutes(reg *session.Registry, allow *session.Allowlist, control *docker.Control, logger shared.Logger) *boxRoutes {
	return &boxRoutes{hosts: map[string]boxEntry{}, reg: reg, allow: allow, control: control, logger: logger}
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

func (b *boxRoutes) sendSubnets(ctx context.Context) error {
	if b.control == nil {
		return nil
	}
	b.mu.Lock()
	subnets := b.subnetsLocked()
	b.mu.Unlock()
	sctx, cancel := context.WithTimeout(ctx, subnetsFor)
	defer cancel()
	return b.control.SetBoxes(sctx, subnets)
}

func (b *boxRoutes) Up(ctx context.Context, info session.BoxInfo) error {
	if !session.ValidBox(info.Box) || info.Host == "" {
		return errors.New("the box helper's answer names no agent-host")
	}
	client, err := agenthost.NewClient(info.Host)
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.hosts[info.Box] = boxEntry{info: info, host: withGateway(client, b.allow, session.SideAgents)}
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

func (b *boxRoutes) Info(box string) (session.BoxInfo, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e, ok := b.hosts[box]
	return e.info, ok
}

func (b *boxRoutes) watch(ctx context.Context) {
	t := time.NewTicker(subnetsEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.mu.Lock()
			n := len(b.hosts)
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
