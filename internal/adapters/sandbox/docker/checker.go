package docker

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/level"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	probeFor     = 60 * time.Second
	statusFor    = 10 * time.Second
	maxStatus    = 64 << 10
	maxRefusals  = 512 << 10
	addrsPerHost = 2
	maxParts     = 6
)

var FixedGitHubAddresses = []string{"140.82.112.3", "140.82.114.4"}

type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

type Config struct {
	Host     session.Host
	Dir      string
	Probe    []string
	Proxy    string
	Status   string
	Side     string
	Allow    []string
	Deny     []string
	Outside  string
	Repos    func() []string
	Lists    Lists
	Fixed    []string
	Resolver Resolver
	Client   *http.Client
	TLS      *tls.Config
}

type Lists interface {
	Verify(ctx context.Context, side string, read func(context.Context) (session.EgressState, error)) (session.EgressList, session.EgressState, error)
}

type Checker struct {
	cfg Config
}

var _ session.Sandbox = (*Checker)(nil)

func New(cfg Config) (*Checker, error) {
	if len(cfg.Probe) == 0 || (cfg.Host != nil) != (cfg.Dir != "") {
		return nil, errors.New("sandbox check: needs the probe command, and a folder to run it in with an agent-host")
	}
	for _, a := range []string{cfg.Proxy, cfg.Status} {
		if _, _, err := net.SplitHostPort(a); err != nil {
			return nil, fmt.Errorf("sandbox check: %q must be host:port", a)
		}
	}
	if cfg.Side == "" {
		return nil, errors.New("sandbox check: needs the egress side the probe's requests arrive on")
	}
	if cfg.Outside == "" || covered(cfg.Allow, cfg.Outside) {
		return nil, fmt.Errorf("sandbox check: the host outside the allowlist, %q, must be one the allowlist doesn't name", cfg.Outside)
	}
	if cfg.Fixed == nil {
		cfg.Fixed = FixedGitHubAddresses
	}
	if cfg.Resolver == nil {
		cfg.Resolver = net.DefaultResolver
	}
	if cfg.Client == nil {
		cfg.Client = statusClient(cfg.TLS)
	}
	cfg.Allow = sortedCopy(cfg.Allow)
	cfg.Deny = sortedCopy(cfg.Deny)
	return &Checker{cfg: cfg}, nil
}

func sortedCopy(in []string) []string {
	out := slices.Clone(in)
	if out == nil {
		out = []string{}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func (c *Checker) Check(ctx context.Context) level.Observation {
	if c.cfg.Host != nil {
		return c.probed(ctx, session.BoxProbe{Host: c.cfg.Host, Dir: c.cfg.Dir})
	}
	parts, want := c.status(ctx)
	parts = append(parts, Uncovered(want.Allow, c.cfg.Deny, c.repoHosts())...)
	if len(parts) > 0 {
		return observation(level.CheckFailed, parts)
	}
	return level.Observation{By: level.Engine, Event: level.CheckPassed}
}

func (c *Checker) CheckBox(ctx context.Context, b session.BoxProbe) level.Observation {
	if b.Host == nil || b.Dir == "" || !b.Subnet.IsValid() || !b.Gateway.IsValid() {
		return observation(level.CheckNotRun, []string{"the engine has no route to the session's container, so the probe couldn't run in it"})
	}
	return c.probed(ctx, b)
}

func (c *Checker) probed(ctx context.Context, b session.BoxProbe) level.Observation {
	var failed []string
	parts, want := c.status(ctx)
	failed = append(failed, parts...)
	repos := c.repoHosts()
	failed = append(failed, Uncovered(want.Allow, c.cfg.Deny, repos)...)
	cfg := c.probeConfig(ctx, repos, lasting(want))
	cfg.Marker = newMarker()
	cfg.Request = shared.RequestOf(ctx)
	if b.Gateway.IsValid() {
		cfg.Gateway = b.Gateway.String()
	}
	results, notRun := c.probe(ctx, b, cfg)
	failed = append(failed, results...)
	if len(failed) == 0 && notRun == "" {
		failed = append(failed, c.confirm(ctx, cfg, b.Subnet)...)
	}
	switch {
	case len(failed) > 0:
		return observation(level.CheckFailed, failed)
	case notRun != "":
		return observation(level.CheckNotRun, []string{notRun})
	}
	return level.Observation{By: level.Engine, Event: level.CheckPassed}
}

func observation(e level.Event, parts []string) level.Observation {
	parts = slices.Compact(parts)
	if len(parts) > maxParts {
		parts = append(parts[:maxParts-1], fmt.Sprintf("and %d more", len(parts)-maxParts+1))
	}
	return level.Observation{By: level.Engine, Event: e, Parts: parts}
}

func (c *Checker) status(ctx context.Context) ([]string, session.EgressList) {
	var want session.EgressList
	var st session.EgressState
	var err error
	if c.cfg.Lists != nil {
		want, st, err = c.cfg.Lists.Verify(ctx, c.cfg.Side, c.readStatus)
	} else {
		want = session.EgressList{Allow: c.cfg.Allow}
		st, err = c.readStatus(ctx)
	}
	if err != nil {
		return []string{err.Error()}, want
	}
	got := st.Lists.Side(c.cfg.Side)
	parts := Compare(want.Allow, c.cfg.Deny, got.Allow, st.Deny)
	if !session.SameList(session.EgressList{Allow: got.Allow, Expires: want.Expires}, got) {
		parts = append(parts, "the egress proxy's expiring hosts aren't the ones the engine set")
	}
	if covered(want.Allow, c.cfg.Outside) {
		parts = append(parts, "the egress allowlist covers "+c.cfg.Outside+", the host the sandbox check expects egress to refuse")
	}
	return parts, want
}

func lasting(l session.EgressList) []string {
	var out []string
	for _, a := range l.Allow {
		if _, expires := l.Expires[a]; !expires {
			out = append(out, a)
		}
	}
	return out
}

func Compare(wantAllow, wantDeny, allow, deny []string) []string {
	var parts []string
	allow, deny = sortedCopy(allow), sortedCopy(deny)
	if extra, missing := diff(wantAllow, allow); len(extra)+len(missing) > 0 {
		parts = append(parts, "the egress proxy's allowlist isn't the one the engine configured"+listed(extra, missing))
	}
	if extra, missing := diff(wantDeny, deny); len(extra)+len(missing) > 0 {
		parts = append(parts, "the egress proxy's denied hosts aren't the ones the engine configured"+listed(extra, missing))
	}
	for _, a := range allow {
		if d := deniedBy(deny, a); d != "" {
			parts = append(parts, "the egress proxy's allowlist names the git host "+a)
		}
	}
	return parts
}

func diff(want, got []string) ([]string, []string) {
	var extra, missing []string
	for _, g := range got {
		if !slices.Contains(want, g) {
			extra = append(extra, g)
		}
	}
	for _, w := range want {
		if !slices.Contains(got, w) {
			missing = append(missing, w)
		}
	}
	return extra, missing
}

func listed(extra, missing []string) string {
	var out []string
	if len(extra) > 0 {
		out = append(out, "it adds "+clipList(extra))
	}
	if len(missing) > 0 {
		out = append(out, "it lacks "+clipList(missing))
	}
	return ": " + strings.Join(out, ", ")
}

func clipList(names []string) string {
	if len(names) > 4 {
		return strings.Join(names[:4], ", ") + fmt.Sprintf(" and %d more", len(names)-4)
	}
	return strings.Join(names, ", ")
}

func deniedBy(deny []string, allowed string) string {
	bare, wild := strings.CutPrefix(allowed, ".")
	for _, d := range deny {
		if bare == d || strings.HasSuffix(bare, "."+d) || (wild && strings.HasSuffix(d, "."+bare)) {
			return d
		}
	}
	return ""
}

func covered(allow []string, name string) bool {
	for _, a := range allow {
		if a == name || (strings.HasPrefix(a, ".") && strings.HasSuffix(name, a)) {
			return true
		}
	}
	return false
}

func (c *Checker) repoHosts() []string {
	if c.cfg.Repos == nil {
		return nil
	}
	var out []string
	for _, h := range c.cfg.Repos() {
		if h = strings.TrimSuffix(strings.ToLower(h), "."); hostLike(h) {
			out = append(out, h)
		}
	}
	return sortedCopy(out)
}

func hostLike(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	for _, c := range h {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '.' {
			return false
		}
	}
	return true
}

func Uncovered(allow, deny, repos []string) []string {
	var parts []string
	for _, h := range repos {
		if covered(allow, h) && deniedBy(deny, h) == "" {
			parts = append(parts, "the egress allowlist covers the repository host "+h+", which isn't a denied host: add it to SF_EXTRA_DENY")
		}
	}
	return parts
}

func Front(allow, deny []string) string {
	for _, a := range allow {
		if !strings.HasPrefix(a, ".") && deniedBy(deny, a) == "" {
			return a
		}
	}
	return ""
}

func (c *Checker) probeConfig(ctx context.Context, repos, allow []string) ProbeConfig {
	var hosts []string
	for _, h := range sortedCopy(append(slices.Clone(c.cfg.Deny), repos...)) {
		if net.ParseIP(h) == nil {
			hosts = append(hosts, h)
		}
	}
	addrs := map[string]bool{}
	for _, a := range c.cfg.Fixed {
		addrs[a] = true
	}
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	found := make([][]netip.Addr, len(hosts))
	var wg sync.WaitGroup
	for i, h := range hosts {
		wg.Go(func() {
			if got, err := c.cfg.Resolver.LookupNetIP(rctx, "ip4", h); err == nil {
				found[i] = got
			}
		})
	}
	wg.Wait()
	for _, got := range found {
		for j, a := range got {
			if j >= addrsPerHost {
				break
			}
			addrs[a.Unmap().String()] = true
		}
	}
	list := make([]string, 0, len(addrs)*2)
	for a := range addrs {
		list = append(list, net.JoinHostPort(a, "443"), net.JoinHostPort(a, "22"))
	}
	slices.Sort(list)
	return ProbeConfig{Proxy: c.cfg.Proxy, GitHosts: hosts, Addrs: list, Outside: c.cfg.Outside, Front: Front(sortedCopy(allow), c.cfg.Deny)}
}

func Args(cfg ProbeConfig) []string {
	args := []string{"-proxy", cfg.Proxy, "-git", strings.Join(cfg.GitHosts, ","), "-addr", strings.Join(cfg.Addrs, ","), "-outside", cfg.Outside, "-front", cfg.Front, "-marker", cfg.Marker, "-gateway", cfg.Gateway}
	if shared.ValidRequest(cfg.Request) {
		args = append(args, "-request", cfg.Request)
	}
	return args
}

func (c *Checker) probe(ctx context.Context, b session.BoxProbe, cfg ProbeConfig) ([]string, string) {
	argv := append(slices.Clone(c.cfg.Probe), Args(cfg)...)
	res, err := b.Host.Run(ctx, session.Command{Argv: argv, Dir: b.Dir, Timeout: probeFor})
	if err != nil {
		var h *session.HostError
		if errors.As(err, &h) && h.Message != "" {
			return nil, "agent-host didn't run the probe: " + clip(h.Message)
		}
		return nil, "agent-host didn't answer, so the probe couldn't run inside the probed container"
	}
	var rep Report
	if res.Code != 0 || json.Unmarshal([]byte(res.Stdout), &rep) != nil || rep.Version != ReportVersion {
		return []string{"the probe gave no answer the engine could read"}, ""
	}
	return Judge(Expected(cfg), rep), ""
}

func Judge(expected []string, rep Report) []string {
	byCheck := map[string]Result{}
	for _, r := range rep.Results {
		if _, dup := byCheck[r.Check]; dup {
			return []string{"the probe answered one check twice"}
		}
		byCheck[r.Check] = r
	}
	var parts []string
	for _, want := range expected {
		r, ok := byCheck[want]
		switch {
		case !ok:
			parts = append(parts, "the probe didn't answer "+want)
		case !r.OK:
			detail := clip(r.Detail)
			if detail == "" {
				detail = want + " failed"
			}
			parts = append(parts, detail)
		}
	}
	return parts
}

func clip(s string) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	if len(s) > 200 {
		s = strings.ToValidUTF8(s[:200], "")
	}
	return s
}
