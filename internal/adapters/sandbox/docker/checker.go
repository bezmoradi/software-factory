package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/level"
	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	probeFor     = 60 * time.Second
	statusFor    = 10 * time.Second
	maxStatus    = 64 << 10
	addrsPerHost = 2
	maxParts     = 6
)

var FixedGitHubAddresses = []string{"140.82.112.3", "140.82.114.4"}

type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

type Config struct {
	Host     session.Host
	Probe    []string
	Dir      string
	Proxy    string
	Status   string
	Allow    []string
	Deny     []string
	Outside  string
	Repos    func() []string
	Fixed    []string
	Resolver Resolver
	Client   *http.Client
}

type Checker struct {
	cfg Config
}

var _ session.Sandbox = (*Checker)(nil)

func New(cfg Config) (*Checker, error) {
	if cfg.Host == nil || len(cfg.Probe) == 0 || cfg.Dir == "" {
		return nil, errors.New("sandbox check: needs agent-host, the probe command and a folder to run it in")
	}
	for _, a := range []string{cfg.Proxy, cfg.Status} {
		if _, _, err := net.SplitHostPort(a); err != nil {
			return nil, fmt.Errorf("sandbox check: %q must be host:port", a)
		}
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
		cfg.Client = &http.Client{Timeout: statusFor, Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, DisableKeepAlives: true}}
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
	var failed []string
	failed = append(failed, c.status(ctx)...)
	repos := c.repoHosts()
	failed = append(failed, Uncovered(c.cfg.Allow, c.cfg.Deny, repos)...)
	cfg := c.probeConfig(ctx, repos)
	results, notRun := c.probe(ctx, cfg)
	failed = append(failed, results...)
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

type statusJSON struct {
	Version string   `json:"version"`
	Allow   []string `json:"allow"`
	Deny    []string `json:"deny"`
}

func (c *Checker) status(ctx context.Context) []string {
	ctx, cancel := context.WithTimeout(ctx, statusFor)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+c.cfg.Status+"/status", nil)
	if err != nil {
		return []string{"the engine couldn't ask the egress proxy for its allowlist"}
	}
	resp, err := c.cfg.Client.Do(req)
	if err != nil {
		return []string{"the egress proxy's status at " + c.cfg.Status + " didn't answer"}
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxStatus+1))
	var st statusJSON
	if err != nil || resp.StatusCode != http.StatusOK || len(data) > maxStatus || json.Unmarshal(data, &st) != nil || st.Allow == nil || st.Deny == nil {
		return []string{"the egress proxy's status couldn't be read"}
	}
	return Compare(c.cfg.Allow, c.cfg.Deny, st.Allow, st.Deny)
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

func (c *Checker) probeConfig(ctx context.Context, repos []string) ProbeConfig {
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
	return ProbeConfig{Proxy: c.cfg.Proxy, GitHosts: hosts, Addrs: list, Outside: c.cfg.Outside, Front: Front(c.cfg.Allow, c.cfg.Deny)}
}

func Args(cfg ProbeConfig) []string {
	return []string{"-proxy", cfg.Proxy, "-git", strings.Join(cfg.GitHosts, ","), "-addr", strings.Join(cfg.Addrs, ","), "-outside", cfg.Outside, "-front", cfg.Front}
}

func (c *Checker) probe(ctx context.Context, cfg ProbeConfig) ([]string, string) {
	argv := append(slices.Clone(c.cfg.Probe), Args(cfg)...)
	res, err := c.cfg.Host.Run(ctx, session.Command{Argv: argv, Dir: c.cfg.Dir, Timeout: probeFor})
	if err != nil {
		var h *session.HostError
		if errors.As(err, &h) && h.Message != "" {
			return nil, "agent-host didn't run the probe: " + clip(h.Message)
		}
		return nil, "agent-host didn't answer, so the probe couldn't run inside the agent container"
	}
	var rep Report
	if res.Code != 0 || json.Unmarshal([]byte(res.Stdout), &rep) != nil || rep.Version != ReportVersion {
		return []string{"the probe in the agent container gave no answer the engine could read"}, ""
	}
	return Judge(Expected(cfg), rep), ""
}

func Judge(expected []string, rep Report) []string {
	byCheck := map[string]Result{}
	for _, r := range rep.Results {
		if _, dup := byCheck[r.Check]; dup {
			return []string{"the probe in the agent container answered one check twice"}
		}
		byCheck[r.Check] = r
	}
	var parts []string
	for _, want := range expected {
		r, ok := byCheck[want]
		switch {
		case !ok:
			parts = append(parts, "the probe in the agent container didn't answer "+want)
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
