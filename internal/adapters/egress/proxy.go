package egress

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const maxHeaderBytes = 8 << 10

type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

type Limits struct {
	MaxConns int
	Idle     time.Duration
	Total    time.Duration
	Header   time.Duration
	Dial     time.Duration
}

type Proxy struct {
	policy   Source
	limits   Limits
	resolver Resolver
	dialer   Dialer
	logger   shared.Logger
	marks    Marks
	slots    chan struct{}
	mu       sync.Mutex
	tunnels  map[*tunnel]struct{}
	logs     *logGate
	grants   *Grants
	tried    *SubnetSeen
}

type tunnel struct {
	host   string
	port   string
	subnet string
	cancel context.CancelCauseFunc
}

var errRevoked = errors.New("the host is no longer allowed")

const sweepEvery = 10 * time.Second

func New(policy Source, limits Limits, resolver Resolver, dialer Dialer, logger shared.Logger, marks Marks) *Proxy {
	return &Proxy{
		policy:   policy,
		limits:   limits,
		resolver: resolver,
		dialer:   dialer,
		logger:   logger,
		marks:    marks,
		slots:    make(chan struct{}, max(limits.MaxConns, 0)),
		tunnels:  map[*tunnel]struct{}{},
		logs:     newLogGate(),
	}
}

func (p *Proxy) WithGrants(g *Grants, tried *SubnetSeen) *Proxy {
	p.grants, p.tried = g, tried
	return p
}

func (p *Proxy) check(policy Policy, subnet, host string) string {
	if p.grants.only(subnet) {
		policy = Policy{deny: policy.deny}
	}
	reason := policy.Check(host)
	switch {
	case reason == ReasonNotAllowlisted && p.grants.allows(subnet, normalize(host)):
		return ""
	case reason == ReasonGitHost && p.grants.allowsGit(subnet, normalize(host)):
		return ""
	}
	return reason
}

func (p *Proxy) sweep(ctx context.Context) {
	t := time.NewTicker(sweepEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			policy := p.policy.Current()
			p.mu.Lock()
			for tn := range p.tunnels {
				switch {
				case tn.port != "":
					if !p.grants.allowsPort(tn.subnet, tn.port) {
						tn.cancel(errRevoked)
					}
				case p.check(policy, tn.subnet, tn.host) != "":
					tn.cancel(errRevoked)
				}
			}
			p.mu.Unlock()
		}
	}
}

func (p *Proxy) track(ctx context.Context, host, port, subnet string) (context.Context, func()) {
	tctx, cancel := context.WithCancelCause(ctx)
	tn := &tunnel{host: host, port: port, subnet: subnet, cancel: cancel}
	p.mu.Lock()
	p.tunnels[tn] = struct{}{}
	p.mu.Unlock()
	return tctx, func() {
		p.mu.Lock()
		delete(p.tunnels, tn)
		p.mu.Unlock()
		cancel(nil)
	}
}

func (p *Proxy) Serve(ctx context.Context, ln net.Listener) error {
	stop := context.AfterFunc(ctx, func() { _ = ln.Close() })
	defer stop()
	var wg sync.WaitGroup
	defer wg.Wait()
	sweepCtx, stopSweep := context.WithCancel(ctx)
	defer stopSweep()
	wg.Go(func() { p.sweep(sweepCtx) })
	if p.grants != nil {
		wg.Go(func() { p.grants.watchKnown(sweepCtx, p.resolver) })
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("egress: accept: %w", err)
		}
		select {
		case p.slots <- struct{}{}:
			wg.Go(func() {
				defer func() { <-p.slots }()
				p.handle(ctx, conn)
			})
		default:
			subnet := subnetOf(conn)
			_ = conn.Close()
			p.refused(subnet, ReasonLimit, "")
		}
	}
}

func (p *Proxy) handle(ctx context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	_ = conn.SetDeadline(time.Now().Add(p.limits.Header))
	reader := bufio.NewReader(io.LimitReader(conn, maxHeaderBytes))
	req, err := http.ReadRequest(reader)
	if err != nil {
		p.refuse(conn, http.StatusBadRequest, ReasonBadRequest, "")
		return
	}
	mark := markOf(req)
	tag := p.marks.requestTag(req, mark)
	marks := p.marks.from(subnetOf(conn))
	if req.Method != http.MethodConnect {
		marks.note(mark, requestedTarget(req.RequestURI), "", ReasonMethod)
		if req.URL != nil {
			p.marks.saw(logged(req.URL.Hostname()), ReasonMethod)
		}
		p.refuse(conn, http.StatusForbidden, ReasonMethod, "", tag...)
		return
	}
	if target, host, ok := p.portGrant(req.RequestURI, subnetOf(conn)); ok {
		p.forward(ctx, conn, reader, stop, target, host, subnetOf(conn), mark, marks, tag)
		return
	}
	host, reason := p.target(req.RequestURI, subnetOf(conn))
	if reason != "" {
		requested, _, _ := net.SplitHostPort(req.RequestURI)
		marks.note(mark, requestedTarget(req.RequestURI), "", reason)
		p.refuse(conn, http.StatusForbidden, reason, logged(requested), tag...)
		return
	}
	target := net.JoinHostPort(host, "443")
	upstream, status, reason := p.connect(ctx, host)
	if reason != "" {
		marks.note(mark, target, "", reason)
		p.refuse(conn, status, reason, host, tag...)
		return
	}
	defer func() { _ = upstream.Close() }()
	if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	early, _ := reader.Peek(reader.Buffered())
	pending := bytes.NewReader(early)
	hello, name, err := readClientHello(io.MultiReader(pending, conn))
	if err != nil {
		marks.note(mark, target, "", helloReason(err))
		p.refused(subnetOf(conn), helloReason(err), host, tag...)
		return
	}
	if normalize(name) != host {
		marks.note(mark, target, logged(name), ReasonSNIMismatch)
		p.marks.saw(logged(name), ReasonSNIMismatch)
		p.logRefused(subnetOf(conn), ReasonSNIMismatch, host, append([]any{"sni", logged(name)}, tag...)...)
		return
	}
	_ = conn.SetDeadline(time.Time{})
	first := append(hello, early[len(early)-pending.Len():]...)
	if _, err := upstream.Write(first); err != nil {
		return
	}
	if !stop() {
		return
	}
	p.logger.Info("egress", append([]any{"event", "allowed", "host", host}, tag...)...)
	tctx, done := p.track(ctx, host, "", subnetOf(conn))
	defer done()
	end := relay(tctx, conn, upstream, p.limits)
	p.logger.Info("egress", append([]any{"event", "closed", "reason", end}, tag...)...)
}

func (p *Proxy) portGrant(authority, subnet string) (string, string, bool) {
	host, port, err := net.SplitHostPort(authority)
	if err != nil || host == "" || p.grants == nil {
		return "", "", false
	}
	host = normalize(host)
	if a, err := netip.ParseAddr(host); err == nil {
		host = a.Unmap().String()
	}
	target := net.JoinHostPort(host, port)
	if _, _, ok := session.SplitPortTarget(target); !ok || !p.grants.allowsPort(subnet, target) {
		return "", "", false
	}
	return target, host, true
}

func (p *Proxy) forward(ctx context.Context, conn net.Conn, reader *bufio.Reader, stop func() bool, target, host, subnet, mark string, marks Marks, tag []any) {
	upstream, status, reason := p.dialPort(ctx, subnet, target)
	if reason != "" {
		marks.note(mark, target, "", reason)
		p.refuse(conn, status, reason, logged(host), tag...)
		return
	}
	defer func() { _ = upstream.Close() }()
	if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if early, _ := reader.Peek(reader.Buffered()); len(early) > 0 {
		if _, err := upstream.Write(early); err != nil {
			return
		}
	}
	_ = conn.SetDeadline(time.Time{})
	if !stop() {
		return
	}
	p.logger.Info("egress", append([]any{"event", "allowed", "host", logged(host), "target", target}, tag...)...)
	tctx, done := p.track(ctx, host, target, subnetOf(conn))
	defer done()
	end := relay(tctx, conn, upstream, p.limits)
	p.logger.Info("egress", append([]any{"event", "closed", "reason", end}, tag...)...)
}

func (p *Proxy) dialPort(ctx context.Context, subnet, target string) (net.Conn, int, string) {
	host, port, _ := session.SplitPortTarget(target)
	ctx, cancel := context.WithTimeout(ctx, p.limits.Dial)
	defer cancel()
	if !p.grants.known.wait(ctx) {
		return nil, http.StatusBadGateway, ReasonResolveFailed
	}
	var addrs []netip.Addr
	if a, err := netip.ParseAddr(host); err == nil {
		if session.AddressNever(a) != "" {
			return nil, http.StatusForbidden, ReasonPrivateAddress
		}
		addrs = []netip.Addr{a}
	} else {
		got, err := p.resolver.LookupNetIP(ctx, "ip", host+".")
		if err != nil || len(got) == 0 {
			return nil, http.StatusBadGateway, ReasonResolveFailed
		}
		for _, a := range got {
			if !publicAddress(a) {
				return nil, http.StatusForbidden, ReasonPrivateAddress
			}
		}
		addrs = got
	}
	if slices.ContainsFunc(addrs, p.grants.ownNetwork) {
		return nil, http.StatusForbidden, ReasonOwnNetwork
	}
	if slices.ContainsFunc(addrs, p.grants.known.gitAddress) && !p.grants.openedGit(subnet, target) {
		return nil, http.StatusForbidden, ReasonGitHost
	}
	for _, a := range addrs {
		conn, err := p.dialer.DialContext(ctx, "tcp", netip.AddrPortFrom(a.Unmap(), uint16(port)).String())
		if err == nil {
			return conn, 0, ""
		}
		if ctx.Err() != nil {
			break
		}
	}
	return nil, http.StatusBadGateway, ReasonDialFailed
}

func (p *Proxy) target(authority, subnet string) (string, string) {
	host, port, err := net.SplitHostPort(authority)
	if err != nil || host == "" {
		return "", ReasonBadTarget
	}
	if port != "443" {
		return "", ReasonPort
	}
	if reason := p.check(p.policy.Current(), subnet, host); reason != "" {
		return "", reason
	}
	return normalize(host), ""
}

func (p *Proxy) connect(ctx context.Context, host string) (net.Conn, int, string) {
	ctx, cancel := context.WithTimeout(ctx, p.limits.Dial)
	defer cancel()
	addrs, err := p.resolver.LookupNetIP(ctx, "ip", host+".")
	if err != nil || len(addrs) == 0 {
		return nil, http.StatusBadGateway, ReasonResolveFailed
	}
	for _, addr := range addrs {
		if !publicAddress(addr) {
			return nil, http.StatusForbidden, ReasonPrivateAddress
		}
	}
	for _, addr := range addrs {
		conn, err := p.dialer.DialContext(ctx, "tcp", netip.AddrPortFrom(addr.Unmap(), 443).String())
		if err == nil {
			return conn, 0, ""
		}
		if ctx.Err() != nil {
			break
		}
	}
	return nil, http.StatusBadGateway, ReasonDialFailed
}

func helloReason(err error) string {
	switch {
	case errors.Is(err, errECH):
		return ReasonECH
	case errors.Is(err, errNoSNI):
		return ReasonNoSNI
	default:
		return ReasonNotTLS
	}
}

func (p *Proxy) refuse(conn net.Conn, status int, reason, host string, more ...any) {
	p.refused(subnetOf(conn), reason, host, more...)
	_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
	body := reason + "\n"
	_, _ = fmt.Fprintf(conn, "HTTP/1.1 %d %s\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		status, http.StatusText(status), len(body), body)
}

func (p *Proxy) refused(subnet, reason, host string, more ...any) {
	p.marks.saw(host, reason)
	if reason == ReasonNotAllowlisted {
		p.tried.add(subnet, host)
	}
	p.logRefused(subnet, reason, host, more...)
}

func (p *Proxy) logRefused(subnet, reason, host string, more ...any) {
	ok, skipped := p.logs.allow(subnet, time.Now())
	if !ok {
		return
	}
	args := []any{"event", "refused", "reason", reason}
	if host != "" {
		args = append(args, "host", host)
	}
	if skipped > 0 {
		args = append(args, "unlogged_before", skipped)
	}
	p.logger.Warn("egress", append(args, more...)...)
}

const (
	logWindow  = time.Minute
	logsPerKey = 20
	maxLogKeys = 256
)

type logCount struct {
	start   time.Time
	logged  int
	skipped int
}

type logGate struct {
	mu   sync.Mutex
	keys map[string]*logCount
}

func newLogGate() *logGate {
	return &logGate{keys: map[string]*logCount{}}
}

func (g *logGate) allow(key string, now time.Time) (bool, int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	c := g.keys[key]
	if c == nil {
		if len(g.keys) >= maxLogKeys {
			clear(g.keys)
		}
		c = &logCount{start: now}
		g.keys[key] = c
	}
	if now.Sub(c.start) >= logWindow {
		c.start, c.logged = now, 0
	}
	if c.logged >= logsPerKey {
		c.skipped++
		return false, 0
	}
	c.logged++
	skipped := c.skipped
	c.skipped = 0
	return true, skipped
}

func logged(host string) string {
	if host == "" {
		return ""
	}
	name := normalize(host)
	if addr, err := netip.ParseAddr(name); err == nil {
		return addr.WithZone("").String()
	}
	if validName(name) {
		return name
	}
	return "(not a host name)"
}

func relay(ctx context.Context, client, upstream net.Conn, limits Limits) string {
	var once sync.Once
	var reason string
	end := func(why string) {
		once.Do(func() {
			reason = why
			_ = client.Close()
			_ = upstream.Close()
		})
	}
	idle := time.AfterFunc(limits.Idle, func() { end("idle") })
	defer idle.Stop()
	total := time.AfterFunc(limits.Total, func() { end("total") })
	defer total.Stop()
	stop := context.AfterFunc(ctx, func() {
		if errors.Is(context.Cause(ctx), errRevoked) {
			end("revoked")
			return
		}
		end("shutdown")
	})
	defer stop()
	touch := func() { idle.Reset(limits.Idle) }
	var wg sync.WaitGroup
	wg.Go(func() {
		if !pipe(upstream, client, touch) {
			end("closed")
		}
	})
	wg.Go(func() {
		if !pipe(client, upstream, touch) {
			end("closed")
		}
	})
	wg.Wait()
	end("closed")
	return reason
}

func pipe(dst, src net.Conn, touch func()) bool {
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			touch()
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return false
			}
		}
		if errors.Is(err, io.EOF) {
			closeWrite(dst)
			return true
		}
		if err != nil {
			return false
		}
	}
}

func closeWrite(conn net.Conn) {
	if c, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = c.CloseWrite()
	}
}
