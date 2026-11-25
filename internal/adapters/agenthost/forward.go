package agenthost

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	forwardDial     = 10 * time.Second
	forwardMaxConns = 64
)

type forward struct {
	target string
	ln     net.Listener
	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	done   bool
}

type forwards struct {
	mu    sync.Mutex
	open  map[string]*forward
	proxy string
	log   shared.Logger
}

func newForwards(proxy string, log shared.Logger) *forwards {
	return &forwards{open: map[string]*forward{}, proxy: proxy, log: log}
}

func (f *forwards) set(targets []string) int {
	want := map[string]string{}
	taken := map[int]bool{}
	for _, t := range targets {
		_, port, ok := session.SplitPortTarget(t)
		if !ok || taken[port] {
			continue
		}
		taken[port] = true
		want[t] = net.JoinHostPort(session.PortForwardsAt, strconv.Itoa(port))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for t, fw := range f.open {
		if _, keep := want[t]; !keep {
			fw.close()
			delete(f.open, t)
		}
	}
	failed := 0
	for t, at := range want {
		if _, open := f.open[t]; open {
			continue
		}
		ln, err := net.Listen("tcp", at)
		if err != nil {
			failed++
			continue
		}
		fw := &forward{target: t, ln: ln, conns: map[net.Conn]struct{}{}}
		f.open[t] = fw
		go fw.serve(f.proxy, f.log)
	}
	if failed > 0 {
		f.log.Warn("handover", "event", "forwards-not-open", "ports", failed)
	}
	return len(f.open)
}

func (fw *forward) close() {
	fw.mu.Lock()
	fw.done = true
	conns := fw.conns
	fw.conns = map[net.Conn]struct{}{}
	fw.mu.Unlock()
	_ = fw.ln.Close()
	for c := range conns {
		_ = c.Close()
	}
}

func (fw *forward) keep(c net.Conn) bool {
	fw.mu.Lock()
	defer fw.mu.Unlock()
	if fw.done || len(fw.conns) >= forwardMaxConns {
		return false
	}
	fw.conns[c] = struct{}{}
	return true
}

func (fw *forward) drop(c net.Conn) {
	fw.mu.Lock()
	delete(fw.conns, c)
	fw.mu.Unlock()
	_ = c.Close()
}

func (fw *forward) serve(proxy string, log shared.Logger) {
	for {
		c, err := fw.ln.Accept()
		if err != nil {
			return
		}
		if !fw.keep(c) {
			_ = c.Close()
			continue
		}
		go func() {
			defer fw.drop(c)
			up, err := connectThrough(proxy, fw.target)
			if err != nil {
				log.Warn("handover", "event", "forward-refused")
				return
			}
			if !fw.keep(up) {
				_ = up.Close()
				return
			}
			defer fw.drop(up)
			splice(c, up)
		}()
	}
}

func connectThrough(proxy, target string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), forwardDial)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", proxy)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(forwardDial))
	if _, err := io.WriteString(conn, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n"); err != nil {
		_ = conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_ = conn.Close()
		return nil, errors.New("egress refused the forward")
	}
	_ = conn.SetDeadline(time.Time{})
	return &readAhead{Conn: conn, r: br}, nil
}

type readAhead struct {
	net.Conn
	r *bufio.Reader
}

func (c *readAhead) Read(p []byte) (int, error) { return c.r.Read(p) }

func (c *readAhead) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return c.Close()
}

func splice(a, b net.Conn) {
	var wg sync.WaitGroup
	half := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = dst.Close()
		}
	}
	wg.Go(func() { half(a, b) })
	wg.Go(func() { half(b, a) })
	wg.Wait()
}
