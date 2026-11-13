package docker

import (
	"bufio"
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	ReportVersion = 1
	dialFor       = 3 * time.Second
	answerFor     = 10 * time.Second
	maxReason     = 64
)

type ProbeConfig struct {
	Proxy    string
	GitHosts []string
	Addrs    []string
	Outside  string
	Front    string
	Dial     time.Duration
}

type Result struct {
	Check  string `json:"check"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

type Report struct {
	Version int      `json:"version"`
	Results []Result `json:"results"`
}

type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

func ProxyCheck(host, port string) string { return "proxy:" + net.JoinHostPort(host, port) }

func DirectCheck(address string) string { return "direct:" + address }

func FrontCheck(front, name string) string { return "front:" + front + ">" + name }

func Expected(cfg ProbeConfig) []string {
	var out []string
	for _, h := range cfg.GitHosts {
		out = append(out, ProxyCheck(h, "443"), ProxyCheck(h, "22"), DirectCheck(net.JoinHostPort(h, "443")), DirectCheck(net.JoinHostPort(h, "22")))
	}
	for _, a := range cfg.Addrs {
		out = append(out, DirectCheck(a))
	}
	if cfg.Outside != "" {
		out = append(out, ProxyCheck(cfg.Outside, "443"))
	}
	if cfg.Front != "" {
		for _, h := range cfg.GitHosts {
			out = append(out, FrontCheck(cfg.Front, h))
		}
	}
	return out
}

func Probe(ctx context.Context, cfg ProbeConfig, d Dialer) Report {
	if cfg.Dial <= 0 {
		cfg.Dial = dialFor
	}
	type job struct {
		check string
		run   func() Result
	}
	var jobs []job
	for _, h := range cfg.GitHosts {
		for _, port := range []string{"443", "22"} {
			jobs = append(jobs, job{ProxyCheck(h, port), func() Result { return throughProxy(ctx, d, cfg, h, port) }})
			address := net.JoinHostPort(h, port)
			jobs = append(jobs, job{DirectCheck(address), func() Result { return direct(ctx, d, cfg, address) }})
		}
	}
	for _, a := range cfg.Addrs {
		jobs = append(jobs, job{DirectCheck(a), func() Result { return direct(ctx, d, cfg, a) }})
	}
	if cfg.Outside != "" {
		jobs = append(jobs, job{ProxyCheck(cfg.Outside, "443"), func() Result { return throughProxy(ctx, d, cfg, cfg.Outside, "443") }})
	}
	if cfg.Front != "" {
		for _, h := range cfg.GitHosts {
			jobs = append(jobs, job{FrontCheck(cfg.Front, h), func() Result { return fronted(ctx, d, cfg, h) }})
		}
	}
	results := make([]Result, len(jobs))
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Go(func() {
			r := j.run()
			r.Check = j.check
			results[i] = r
		})
	}
	wg.Wait()
	return Report{Version: ReportVersion, Results: results}
}

func direct(ctx context.Context, d Dialer, cfg ProbeConfig, address string) Result {
	ctx, cancel := context.WithTimeout(ctx, cfg.Dial)
	defer cancel()
	conn, err := d.DialContext(ctx, "tcp", address)
	if err != nil {
		return Result{OK: true, Detail: "no route to " + address}
	}
	_ = conn.Close()
	return Result{Detail: "the agent container reached " + address + " directly"}
}

func throughProxy(ctx context.Context, d Dialer, cfg ProbeConfig, host, port string) Result {
	target := net.JoinHostPort(host, port)
	dctx, cancel := context.WithTimeout(ctx, cfg.Dial)
	conn, err := d.DialContext(dctx, "tcp", cfg.Proxy)
	cancel()
	if err != nil {
		return Result{Detail: "the egress proxy at " + cfg.Proxy + " didn't answer"}
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(answerFor))
	if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target); err != nil {
		return Result{Detail: "the egress proxy at " + cfg.Proxy + " didn't answer"}
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
	if err != nil {
		return Result{Detail: "the egress proxy at " + cfg.Proxy + " didn't answer for " + target}
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
		return Result{Detail: "the egress proxy let " + target + " through"}
	case http.StatusForbidden:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxReason))
		return Result{OK: true, Detail: "the egress proxy refused " + target + " (" + reasonWord(string(body)) + ")"}
	}
	return Result{Detail: fmt.Sprintf("the egress proxy answered %d for %s, not a refusal", resp.StatusCode, target)}
}

type counted struct {
	net.Conn
	read *atomic.Int64
}

func (c counted) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	c.read.Add(int64(n))
	return n, err
}

func fronted(ctx context.Context, d Dialer, cfg ProbeConfig, name string) Result {
	front := net.JoinHostPort(cfg.Front, "443")
	dctx, cancel := context.WithTimeout(ctx, cfg.Dial)
	conn, err := d.DialContext(dctx, "tcp", cfg.Proxy)
	cancel()
	if err != nil {
		return Result{Detail: "the egress proxy at " + cfg.Proxy + " didn't answer"}
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(answerFor))
	if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", front, front); err != nil {
		return Result{Detail: "the egress proxy at " + cfg.Proxy + " didn't answer"}
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		return Result{Detail: "the egress proxy at " + cfg.Proxy + " didn't answer for " + front}
	}
	_ = resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusBadGateway:
		return Result{OK: true, Detail: "the egress proxy couldn't reach " + front + ", so nothing could be relayed"}
	default:
		return Result{Detail: fmt.Sprintf("the egress proxy answered %d for the allowed host %s, so the check for a TLS name sent through it couldn't run", resp.StatusCode, front)}
	}
	var read atomic.Int64
	tc := tls.Client(counted{Conn: &buffered{Conn: conn, r: br}, read: &read}, &tls.Config{ServerName: name, MinVersion: tls.VersionTLS12})
	_ = tc.HandshakeContext(ctx)
	if read.Load() > 0 {
		return Result{Detail: "the egress proxy relayed a TLS hello naming " + name + " through the allowed host " + cfg.Front}
	}
	return Result{OK: true, Detail: "the egress proxy closed a tunnel to " + cfg.Front + " that named " + name}
}

type buffered struct {
	net.Conn
	r *bufio.Reader
}

func (b *buffered) Read(p []byte) (int, error) { return b.r.Read(p) }

func reasonWord(s string) string {
	s = strings.TrimSpace(s)
	for _, c := range s {
		if (c < 'a' || c > 'z') && c != '_' {
			return "refused"
		}
	}
	if s == "" {
		return "refused"
	}
	return s
}

func ParseProbeArgs(args []string) (ProbeConfig, error) {
	fs := flag.NewFlagSet("sandbox-probe", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	proxy := fs.String("proxy", "", "the egress proxy, host:port")
	git := fs.String("git", "", "comma-separated git host names")
	addrs := fs.String("addr", "", "comma-separated ip:port addresses of git hosts")
	outside := fs.String("outside", "", "a host outside the allowlist")
	front := fs.String("front", "", "an allowed host to send other TLS names through")
	if err := fs.Parse(args); err != nil {
		return ProbeConfig{}, err
	}
	if fs.NArg() > 0 {
		return ProbeConfig{}, fmt.Errorf("sandbox-probe: unexpected argument %q", fs.Arg(0))
	}
	if _, _, err := net.SplitHostPort(*proxy); err != nil {
		return ProbeConfig{}, fmt.Errorf("sandbox-probe: -proxy must be host:port")
	}
	cfg := ProbeConfig{Proxy: *proxy, GitHosts: split(*git), Addrs: split(*addrs), Outside: *outside, Front: *front}
	for _, a := range cfg.Addrs {
		if _, err := netip.ParseAddrPort(a); err != nil {
			return ProbeConfig{}, fmt.Errorf("sandbox-probe: %q isn't ip:port", a)
		}
	}
	return cfg, nil
}

func split(v string) []string {
	var out []string
	for item := range strings.SplitSeq(v, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}
