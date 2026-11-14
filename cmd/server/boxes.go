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

var errNoBox = &session.HostError{Code: "no-box", Message: "No session's container is running."}

type boxEntry struct {
	info session.BoxInfo
	host agentHost
}

type boxRoutes struct {
	mu        sync.Mutex
	hosts     map[string]boxEntry
	order     []string
	listener  *page.BoxListener
	control   *docker.Control
	reg       *session.Registry
	allow     *session.Allowlist
	workspace string
	logger    shared.Logger
}

var _ session.BoxRoutes = (*boxRoutes)(nil)

func newBoxRoutes(workspace string, reg *session.Registry, allow *session.Allowlist, control *docker.Control, logger shared.Logger) *boxRoutes {
	return &boxRoutes{hosts: map[string]boxEntry{}, reg: reg, allow: allow, control: control, workspace: workspace, logger: logger}
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
	b.order = append(slices.DeleteFunc(b.order, func(s string) bool { return s == info.Box }), info.Box)
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
	b.order = slices.DeleteFunc(b.order, func(s string) bool { return s == box })
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

func (b *boxRoutes) pick(ctx context.Context) (agentHost, string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if box, ok := session.BoxOf(ctx); ok {
		if e, live := b.hosts[box]; live {
			return e.host, box, nil
		}
	}
	if len(b.order) == 0 {
		return nil, "", errNoBox
	}
	box := b.order[len(b.order)-1]
	return b.hosts[box].host, box, nil
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

type anyBox struct {
	routes *boxRoutes
}

func (a anyBox) in(ctx context.Context, c session.Command) (agentHost, session.Command, error) {
	h, box, err := a.routes.pick(ctx)
	if err != nil {
		return nil, c, err
	}
	if c.Dir == "" || c.Dir == a.routes.workspace {
		c.Dir = session.BoxFolder(a.routes.workspace, box)
	}
	return h, c, nil
}

func (a anyBox) client(ctx context.Context) (*agenthost.Client, error) {
	h, _, err := a.routes.pick(ctx)
	if err != nil {
		return nil, err
	}
	switch v := h.(type) {
	case *agenthost.Client:
		return v, nil
	case gatewayHost:
		return v.Client, nil
	}
	return nil, errNoBox
}

func (a anyBox) AddWorktree(context.Context, string, string, string, string, session.CommitIdentity) (string, error) {
	return "", errNoBox
}

func (a anyBox) AddExistingWorktree(context.Context, string, string, string, session.CommitIdentity) (string, error) {
	return "", errNoBox
}

func (a anyBox) SetIdentity(context.Context, string, string, session.CommitIdentity) error {
	return errNoBox
}

func (a anyBox) RemoveWorktree(context.Context, string, string, bool) error { return errNoBox }

func (a anyBox) Run(ctx context.Context, c session.Command) (session.RunResult, error) {
	h, c, err := a.in(ctx, c)
	if err != nil {
		return session.RunResult{}, err
	}
	return h.Run(ctx, c)
}

func (a anyBox) Start(ctx context.Context, c session.Command) (session.Process, error) {
	h, c, err := a.in(ctx, c)
	if err != nil {
		return nil, err
	}
	return h.Start(ctx, c)
}

func (a anyBox) Stop(ctx context.Context, id string) error {
	h, _, err := a.routes.pick(ctx)
	if err != nil {
		return err
	}
	return h.Stop(ctx, id)
}

func (a anyBox) SignIn(ctx context.Context, c session.Command, kind string) (session.SignIn, error) {
	h, c, err := a.in(ctx, c)
	if err != nil {
		return session.SignIn{}, err
	}
	return h.SignIn(ctx, c, kind)
}

func (a anyBox) Login(ctx context.Context, c session.Command, step, code string) (string, error) {
	cl, err := a.client(ctx)
	if err != nil {
		return "", err
	}
	_, c, err = a.in(ctx, c)
	if err != nil {
		return "", err
	}
	return cl.Login(ctx, c, step, code)
}

func (a anyBox) APIKey(ctx context.Context, step, key string) error {
	cl, err := a.client(ctx)
	if err != nil {
		return err
	}
	return cl.APIKey(ctx, step, key)
}

func (a anyBox) Trust(ctx context.Context, c session.Command) error {
	cl, err := a.client(ctx)
	if err != nil {
		return err
	}
	return cl.Trust(ctx, c)
}

func (a anyBox) HomeSettings(ctx context.Context, mcp *session.Command) ([]session.HomeEntry, error) {
	cl, err := a.client(ctx)
	if err != nil {
		return nil, err
	}
	if mcp != nil {
		_, c, err := a.in(ctx, *mcp)
		if err != nil {
			return nil, err
		}
		mcp = &c
	}
	return cl.HomeSettings(ctx, mcp)
}

func (a anyBox) ReadAllowRules(ctx context.Context, repo string) ([]session.SettingsFile, error) {
	cl, err := a.client(ctx)
	if err != nil {
		return nil, err
	}
	return cl.ReadAllowRules(ctx, repo)
}

func (a anyBox) RemoveAllowRule(ctx context.Context, repo string, place session.RulePlace, worktree, rule string) error {
	cl, err := a.client(ctx)
	if err != nil {
		return err
	}
	return cl.RemoveAllowRule(ctx, repo, place, worktree, rule)
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
