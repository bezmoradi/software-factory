package page

import (
	"context"
	"net"
	"net/netip"
	"sync"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

type limitedListener struct {
	net.Listener
	mu     sync.Mutex
	open   int
	max    int
	onDrop func()
}

func LimitListener(l net.Listener, max int, onDrop func()) net.Listener {
	return &limitedListener{Listener: l, max: max, onDrop: onDrop}
}

func (l *limitedListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		l.mu.Lock()
		if l.open >= l.max {
			l.mu.Unlock()
			_ = c.Close()
			if l.onDrop != nil {
				l.onDrop()
			}
			continue
		}
		l.open++
		l.mu.Unlock()
		return &countedConn{Conn: c, release: l.release}, nil
	}
}

func AllowLocal(l net.Listener, allowed []netip.Addr, onDrop func()) net.Listener {
	set := make(map[netip.Addr]bool, len(allowed))
	for _, a := range allowed {
		set[a.Unmap()] = true
	}
	return &allowingListener{Listener: l, allowed: set, onDrop: onDrop}
}

type allowingListener struct {
	net.Listener
	allowed map[netip.Addr]bool
	onDrop  func()
}

func (l *allowingListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if local, ok := c.LocalAddr().(*net.TCPAddr); ok {
			if addr, valid := netip.AddrFromSlice(local.IP); valid && (addr.Unmap().IsLoopback() || l.allowed[addr.Unmap()]) {
				return c, nil
			}
		}
		_ = c.Close()
		if l.onDrop != nil {
			l.onDrop()
		}
	}
}

func (l *limitedListener) release() {
	l.mu.Lock()
	l.open--
	l.mu.Unlock()
}

type countedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *countedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}

func (c *countedConn) Unwrap() net.Conn { return c.Conn }

const BoxConns = 64

type boxPair struct {
	box   string
	agent netip.Addr
}

type BoxListener struct {
	net.Listener
	mu     sync.Mutex
	pairs  map[netip.Addr]boxPair
	conns  map[string]map[*boxConn]struct{}
	onDrop func()
}

func NewBoxListener(l net.Listener, onDrop func()) *BoxListener {
	return &BoxListener{Listener: l, pairs: map[netip.Addr]boxPair{}, conns: map[string]map[*boxConn]struct{}{}, onDrop: onDrop}
}

func (l *BoxListener) Allow(box string, engine, agent netip.Addr) {
	if !session.ValidBox(box) || !engine.IsValid() || !agent.IsValid() {
		return
	}
	engine, agent = engine.Unmap(), agent.Unmap()
	l.mu.Lock()
	for at, p := range l.pairs {
		if p.box == box && at != engine {
			delete(l.pairs, at)
		}
	}
	old, had := l.pairs[engine]
	l.pairs[engine] = boxPair{box: box, agent: agent}
	stale := had && (old.box != box || old.agent != agent)
	var closing []*boxConn
	if stale {
		for c := range l.conns[old.box] {
			closing = append(closing, c)
		}
		delete(l.conns, old.box)
	}
	l.mu.Unlock()
	for _, c := range closing {
		_ = c.Conn.Close()
	}
}

func (l *BoxListener) Drop(box string) int {
	l.mu.Lock()
	for at, p := range l.pairs {
		if p.box == box {
			delete(l.pairs, at)
		}
	}
	closing := make([]*boxConn, 0, len(l.conns[box]))
	for c := range l.conns[box] {
		closing = append(closing, c)
	}
	delete(l.conns, box)
	l.mu.Unlock()
	for _, c := range closing {
		_ = c.Conn.Close()
	}
	return len(closing)
}

func addrOf(a net.Addr) (netip.Addr, bool) {
	t, ok := a.(*net.TCPAddr)
	if !ok {
		return netip.Addr{}, false
	}
	ip, ok := netip.AddrFromSlice(t.IP)
	return ip.Unmap(), ok
}

func (l *BoxListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		local, okL := addrOf(c.LocalAddr())
		remote, okR := addrOf(c.RemoteAddr())
		l.mu.Lock()
		p, known := l.pairs[local]
		if okL && okR && known && p.agent == remote && len(l.conns[p.box]) < BoxConns {
			bc := &boxConn{Conn: c, box: p.box, l: l}
			if l.conns[p.box] == nil {
				l.conns[p.box] = map[*boxConn]struct{}{}
			}
			l.conns[p.box][bc] = struct{}{}
			l.mu.Unlock()
			return bc, nil
		}
		l.mu.Unlock()
		_ = c.Close()
		if l.onDrop != nil {
			l.onDrop()
		}
	}
}

type boxConn struct {
	net.Conn
	box  string
	l    *BoxListener
	once sync.Once
}

func (c *boxConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() {
		c.l.mu.Lock()
		if set := c.l.conns[c.box]; set != nil {
			delete(set, c)
			if len(set) == 0 {
				delete(c.l.conns, c.box)
			}
		}
		c.l.mu.Unlock()
	})
	return err
}

type connBox struct{}

func BoxContext(ctx context.Context, c net.Conn) context.Context {
	for c != nil {
		switch v := c.(type) {
		case *boxConn:
			return context.WithValue(ctx, connBox{}, v.box)
		case interface{ Unwrap() net.Conn }:
			c = v.Unwrap()
		default:
			return ctx
		}
	}
	return ctx
}

func ConnBox(ctx context.Context) string {
	box, _ := ctx.Value(connBox{}).(string)
	if !session.ValidBox(box) {
		return ""
	}
	return box
}
