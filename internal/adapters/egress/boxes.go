package egress

import (
	"errors"
	"net"
	"net/netip"
	"sync"
)

const (
	MaxSubnets     = 4096
	MaxSubnetConns = 64
	subnetBits     = 28
	agentOffset    = 4
)

type Subnets struct {
	mu    sync.Mutex
	pool  []netip.Prefix
	live  map[netip.Prefix]bool
	conns map[netip.Prefix]map[*subnetConn]struct{}
}

func NewSubnets(pool []netip.Prefix) *Subnets {
	return &Subnets{pool: pool, live: map[netip.Prefix]bool{}, conns: map[netip.Prefix]map[*subnetConn]struct{}{}}
}

func (s *Subnets) inPool(p netip.Prefix) bool {
	for _, r := range s.pool {
		if r.Bits() <= p.Bits() && r.Contains(p.Addr()) {
			return true
		}
	}
	return false
}

func (s *Subnets) Replace(list []netip.Prefix) error {
	if len(list) > MaxSubnets {
		return errors.New("egress: more subnets than egress takes")
	}
	next := make(map[netip.Prefix]bool, len(list))
	for _, p := range list {
		if !p.IsValid() || !p.Addr().Is4() || p.Bits() != subnetBits || p.Masked() != p {
			return errors.New("egress: a subnet isn't a /28 network address")
		}
		if !s.inPool(p) {
			return errors.New("egress: a subnet is outside SF_BOX_SUBNETS")
		}
		next[p] = true
	}
	s.mu.Lock()
	var closing []*subnetConn
	for p, set := range s.conns {
		if next[p] {
			continue
		}
		for c := range set {
			closing = append(closing, c)
		}
		delete(s.conns, p)
	}
	s.live = next
	s.mu.Unlock()
	for _, c := range closing {
		_ = c.Conn.Close()
	}
	return nil
}

func (s *Subnets) List() []netip.Prefix {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]netip.Prefix, 0, len(s.live))
	for p := range s.live {
		out = append(out, p)
	}
	return out
}

func agentAt(p netip.Prefix) netip.Addr {
	a := p.Addr()
	for range agentOffset {
		a = a.Next()
	}
	return a
}

func (s *Subnets) admit(c net.Conn) (*subnetConn, bool) {
	local, remote := tcpAddr(c.LocalAddr()), tcpAddr(c.RemoteAddr())
	if !local.Is4() || !remote.Is4() {
		return nil, false
	}
	p, err := remote.Prefix(subnetBits)
	if err != nil || remote != agentAt(p) || !p.Contains(local) || local == remote {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.live[p] || len(s.conns[p]) >= MaxSubnetConns {
		return nil, false
	}
	sc := &subnetConn{Conn: c, subnet: p.String(), prefix: p, owner: s}
	if s.conns[p] == nil {
		s.conns[p] = map[*subnetConn]struct{}{}
	}
	s.conns[p][sc] = struct{}{}
	return sc, true
}

func (s *Subnets) forget(c *subnetConn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if set := s.conns[c.prefix]; set != nil {
		delete(set, c)
		if len(set) == 0 {
			delete(s.conns, c.prefix)
		}
	}
}

type subnetConn struct {
	net.Conn
	subnet string
	prefix netip.Prefix
	owner  *Subnets
	once   sync.Once
}

func (c *subnetConn) Subnet() string { return c.subnet }

func (c *subnetConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { c.owner.forget(c) })
	return err
}

func (c *subnetConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

type subnetListener struct {
	net.Listener
	subnets *Subnets
	onDrop  func()
}

func AllowSubnets(l net.Listener, s *Subnets, onDrop func()) net.Listener {
	return &subnetListener{Listener: l, subnets: s, onDrop: onDrop}
}

func tcpAddr(a net.Addr) netip.Addr {
	t, ok := a.(*net.TCPAddr)
	if !ok {
		return netip.Addr{}
	}
	ip, _ := netip.AddrFromSlice(t.IP)
	return ip.Unmap()
}

func (l *subnetListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if sc, ok := l.subnets.admit(c); ok {
			return sc, nil
		}
		_ = c.Close()
		if l.onDrop != nil {
			l.onDrop()
		}
	}
}

func subnetOf(c net.Conn) string {
	if s, ok := c.(interface{ Subnet() string }); ok {
		return s.Subnet()
	}
	return ""
}
