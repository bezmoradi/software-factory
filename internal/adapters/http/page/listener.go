package page

import (
	"net"
	"net/netip"
	"sync"
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
