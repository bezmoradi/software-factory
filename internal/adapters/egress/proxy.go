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
	"sync"
	"time"

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
	policy   Policy
	limits   Limits
	resolver Resolver
	dialer   Dialer
	logger   shared.Logger
	slots    chan struct{}
}

func New(policy Policy, limits Limits, resolver Resolver, dialer Dialer, logger shared.Logger) *Proxy {
	return &Proxy{
		policy:   policy,
		limits:   limits,
		resolver: resolver,
		dialer:   dialer,
		logger:   logger,
		slots:    make(chan struct{}, max(limits.MaxConns, 0)),
	}
}

func (p *Proxy) Serve(ctx context.Context, ln net.Listener) error {
	stop := context.AfterFunc(ctx, func() { _ = ln.Close() })
	defer stop()
	var wg sync.WaitGroup
	defer wg.Wait()
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
			_ = conn.Close()
			p.refused(ReasonLimit, "")
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
	if req.Method != http.MethodConnect {
		p.refuse(conn, http.StatusForbidden, ReasonMethod, "")
		return
	}
	host, reason := p.target(req.RequestURI)
	if reason != "" {
		requested, _, _ := net.SplitHostPort(req.RequestURI)
		p.refuse(conn, http.StatusForbidden, reason, logged(requested))
		return
	}
	upstream, status, reason := p.connect(ctx, host)
	if reason != "" {
		p.refuse(conn, status, reason, host)
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
		p.refused(helloReason(err), host)
		return
	}
	if normalize(name) != host {
		p.refused(ReasonSNIMismatch, host, "sni", logged(name))
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
	p.logger.Info("egress", "event", "allowed")
	end := relay(ctx, conn, upstream, p.limits)
	p.logger.Info("egress", "event", "closed", "reason", end)
}

func (p *Proxy) target(authority string) (string, string) {
	host, port, err := net.SplitHostPort(authority)
	if err != nil || host == "" {
		return "", ReasonBadTarget
	}
	if port != "443" {
		return "", ReasonPort
	}
	if reason := p.policy.Check(host); reason != "" {
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

func (p *Proxy) refuse(conn net.Conn, status int, reason, host string) {
	p.refused(reason, host)
	_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
	body := reason + "\n"
	_, _ = fmt.Fprintf(conn, "HTTP/1.1 %d %s\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		status, http.StatusText(status), len(body), body)
}

func (p *Proxy) refused(reason, host string, more ...any) {
	args := []any{"event", "refused", "reason", reason}
	if host != "" {
		args = append(args, "host", host)
	}
	p.logger.Warn("egress", append(args, more...)...)
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
	stop := context.AfterFunc(ctx, func() { end("shutdown") })
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
