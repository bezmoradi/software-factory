package egress

import (
	"errors"
	"net"
	"net/netip"
	"sync"
)

const (
	MaxSubnets = 4096
	subnetBits = 28
)

type Subnets struct {
	mu   sync.RWMutex
	live map[netip.Prefix]bool
	set  bool
}

func NewSubnets() *Subnets {
	return &Subnets{live: map[netip.Prefix]bool{}}
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
		next[p] = true
	}
	s.mu.Lock()
	s.live, s.set = next, true
	s.mu.Unlock()
	return nil
}

func (s *Subnets) List() []netip.Prefix {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]netip.Prefix, 0, len(s.live))
	for p := range s.live {
		out = append(out, p)
	}
	return out
}

func (s *Subnets) of(local, remote netip.Addr) (netip.Prefix, bool) {
	if !local.Is4() || !remote.Is4() {
		return netip.Prefix{}, false
	}
	p, err := remote.Prefix(subnetBits)
	if err != nil {
		return netip.Prefix{}, false
	}
	s.mu.RLock()
	ok := s.live[p]
	s.mu.RUnlock()
	return p, ok && p.Contains(local) && local != remote
}

type subnetConn struct {
	net.Conn
	subnet string
}

func (c subnetConn) Subnet() string { return c.subnet }

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
		if p, ok := l.subnets.of(tcpAddr(c.LocalAddr()), tcpAddr(c.RemoteAddr())); ok {
			return subnetConn{Conn: c, subnet: p.String()}, nil
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
