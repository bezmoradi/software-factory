package docker

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	ReportVersion = 2
	dialFor       = 3 * time.Second
	answerFor     = 10 * time.Second
	maxReason     = 64
	MarkerHeader  = "Software-Factory-Probe"
	RequestHeader = "Software-Factory-Request"
)

var (
	markerPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
	GatewayPorts  = []string{"22", "80", "443", "2375", "3000", "8080"}
)

type ProbeConfig struct {
	Proxy    string
	GitHosts []string
	Granted  []string
	Addrs    []string
	Outside  string
	Front    string
	Marker   string
	Request  string
	Gateway  string
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

func GatewayCheck(address string) string { return "gateway:" + address }

func (cfg ProbeConfig) granted(h, port string) bool {
	return slices.Contains(cfg.Granted, net.JoinHostPort(h, port))
}

func proxyPorts(cfg ProbeConfig, h string) []string {
	var out []string
	for _, port := range []string{"443", "22"} {
		if !cfg.granted(h, port) {
			out = append(out, port)
		}
	}
	return out
}

func Expected(cfg ProbeConfig) []string {
	var out []string
	for _, h := range cfg.GitHosts {
		for _, port := range proxyPorts(cfg, h) {
			out = append(out, ProxyCheck(h, port))
		}
		out = append(out, DirectCheck(net.JoinHostPort(h, "443")), DirectCheck(net.JoinHostPort(h, "22")))
	}
	for _, a := range cfg.Addrs {
		out = append(out, DirectCheck(a))
	}
	if cfg.Outside != "" {
		out = append(out, ProxyCheck(cfg.Outside, "443"), DNSCheck(cfg.Outside), DNSCheck(RandomLabel(cfg.Outside)))
	}
	if cfg.Front != "" {
		for _, h := range cfg.GitHosts {
			if !cfg.granted(h, "443") {
				out = append(out, FrontCheck(cfg.Front, h))
			}
		}
	}
	for _, a := range OutwardUDP {
		out = append(out, UDPCheck(a))
	}
	if cfg.Gateway != "" {
		for _, port := range GatewayPorts {
			out = append(out, GatewayCheck(net.JoinHostPort(cfg.Gateway, port)))
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
		for _, port := range proxyPorts(cfg, h) {
			jobs = append(jobs, job{ProxyCheck(h, port), func() Result { return throughProxy(ctx, d, cfg, h, port) }})
		}
		for _, port := range []string{"443", "22"} {
			address := net.JoinHostPort(h, port)
			jobs = append(jobs, job{DirectCheck(address), func() Result { return direct(ctx, d, cfg, address) }})
		}
	}
	for _, a := range cfg.Addrs {
		jobs = append(jobs, job{DirectCheck(a), func() Result { return direct(ctx, d, cfg, a) }})
	}
	if cfg.Outside != "" {
		jobs = append(jobs, job{ProxyCheck(cfg.Outside, "443"), func() Result { return throughProxy(ctx, d, cfg, cfg.Outside, "443") }})
		servers := nameServers()
		random := randomHex(8) + "." + cfg.Outside
		jobs = append(jobs,
			job{DNSCheck(cfg.Outside), func() Result { return lookupOutside(ctx, d, servers, cfg.Outside, cfg.Outside) }},
			job{DNSCheck(RandomLabel(cfg.Outside)), func() Result { return lookupOutside(ctx, d, servers, random, "a random name under "+cfg.Outside) }})
	}
	for _, a := range OutwardUDP {
		jobs = append(jobs, job{UDPCheck(a), func() Result { return udpOut(ctx, d, cfg, a) }})
	}
	if cfg.Front != "" {
		for _, h := range cfg.GitHosts {
			if !cfg.granted(h, "443") {
				jobs = append(jobs, job{FrontCheck(cfg.Front, h), func() Result { return fronted(ctx, d, cfg, h) }})
			}
		}
	}
	if cfg.Gateway != "" {
		for _, port := range GatewayPorts {
			address := net.JoinHostPort(cfg.Gateway, port)
			jobs = append(jobs, job{GatewayCheck(address), func() Result { return gateway(ctx, d, cfg, address) }})
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
	if err == nil {
		_ = conn.Close()
		return Result{Detail: "the probed container reached " + address + " directly"}
	}
	var dnsErr *net.DNSError
	switch {
	case errors.As(err, &dnsErr):
		return Result{OK: true, Detail: "the name in " + address + " doesn't resolve in the probed container"}
	case NoRoute(err):
		return Result{OK: true, Detail: "no route to " + address}
	}
	return Result{Detail: "a direct dial to " + address + " failed with " + errWord(err) + " instead of finding no route, so packets may leave the probed container"}
}

func gateway(ctx context.Context, d Dialer, cfg ProbeConfig, address string) Result {
	ctx, cancel := context.WithTimeout(ctx, cfg.Dial)
	defer cancel()
	conn, err := d.DialContext(ctx, "tcp", address)
	if err == nil {
		_ = conn.Close()
		return Result{Detail: "the session's container reached its network's gateway at " + address}
	}
	var ne net.Error
	switch {
	case NoRoute(err):
		return Result{OK: true, Detail: "no route to the gateway at " + address}
	case errors.As(err, &ne) && ne.Timeout():
		return Result{OK: true, Detail: "nothing answered at the gateway " + address}
	}
	return Result{Detail: "a dial to the network's gateway at " + address + " failed with " + errWord(err) + ", so something holds that address on the session's network"}
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
	if _, err := io.WriteString(conn, connectLine(target, cfg.Marker, cfg.Request)); err != nil {
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

func connectLine(target, marker, request string) string {
	line := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n"
	if marker != "" {
		line += MarkerHeader + ": " + marker + "\r\n"
	}
	if marker != "" && shared.ValidRequest(request) {
		line += RequestHeader + ": " + request + "\r\n"
	}
	return line + "\r\n"
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
	if _, err := io.WriteString(conn, connectLine(front, cfg.Marker, cfg.Request)); err != nil {
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
	granted := fs.String("granted", "", "comma-separated host:port of git hosts a grant opens to this container on 443 or 22, which egress lets through")
	addrs := fs.String("addr", "", "comma-separated ip:port addresses of git hosts")
	outside := fs.String("outside", "", "a host outside the allowlist")
	front := fs.String("front", "", "an allowed host to send other TLS names through")
	marker := fs.String("marker", "", "the engine's marker for this run, sent with every request to the proxy")
	gw := fs.String("gateway", "", "the address of the session network's gateway, which must not answer")
	request := fs.String("request", "", "the engine's request id for the action that runs this check, sent beside the marker")
	if err := fs.Parse(args); err != nil {
		return ProbeConfig{}, err
	}
	if fs.NArg() > 0 {
		return ProbeConfig{}, fmt.Errorf("sandbox-probe: unexpected argument %q", fs.Arg(0))
	}
	if _, _, err := net.SplitHostPort(*proxy); err != nil {
		return ProbeConfig{}, fmt.Errorf("sandbox-probe: -proxy must be host:port")
	}
	if *marker != "" && !markerPattern.MatchString(*marker) {
		return ProbeConfig{}, fmt.Errorf("sandbox-probe: -marker must be 32 lowercase hex digits")
	}
	if *request != "" && !shared.ValidRequest(*request) {
		return ProbeConfig{}, fmt.Errorf("sandbox-probe: -request must be 16 lowercase hex digits")
	}
	if *gw != "" {
		a, err := netip.ParseAddr(*gw)
		if err != nil || !a.Is4() {
			return ProbeConfig{}, fmt.Errorf("sandbox-probe: -gateway must be an IPv4 address")
		}
	}
	cfg := ProbeConfig{Proxy: *proxy, GitHosts: split(*git), Granted: split(*granted), Addrs: split(*addrs), Outside: *outside, Front: *front, Marker: *marker, Request: *request, Gateway: *gw}
	for _, g := range cfg.Granted {
		h, port, err := net.SplitHostPort(g)
		if err != nil || !slices.Contains(cfg.GitHosts, h) || port != "443" && port != "22" {
			return ProbeConfig{}, fmt.Errorf("sandbox-probe: -granted names %q, which isn't a host -git names on 443 or 22", g)
		}
	}
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
