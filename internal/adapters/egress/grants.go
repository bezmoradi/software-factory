package egress

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	MaxActionSubnets = 8
	MaxSubnetHosts   = 32
	MaxStack         = 64
	maxBoxesBody     = 4 << 20
)

type Grants struct {
	base  Source
	pool  []netip.Prefix
	mu    sync.RWMutex
	lists map[string]Policy
	alone map[string]bool
	stack []netip.Prefix
	known *Known
}

func NewGrants(base Source, pool []netip.Prefix) *Grants {
	return &Grants{base: base, pool: slices.Clone(pool), lists: map[string]Policy{}, known: NewKnown()}
}

func (g *Grants) SetGitHosts(names []string) {
	g.known.setRepos(names)
}

func (g *Grants) watchKnown(ctx context.Context, resolver Resolver) {
	g.known.Watch(ctx, resolver, func() []string { return g.base.Current().Deny() })
}

func (g *Grants) SetStack(list []netip.Prefix) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.stack = slices.Clone(list)
}

func (g *Grants) ownNetwork(a netip.Addr) bool {
	a = a.Unmap()
	g.mu.RLock()
	list := append(slices.Clone(g.pool), g.stack...)
	g.mu.RUnlock()
	if slices.ContainsFunc(list, func(p netip.Prefix) bool { return p.Contains(a) }) || g.known.dockerHost(a) {
		return true
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return true
	}
	for _, x := range addrs {
		n, ok := x.(*net.IPNet)
		if !ok {
			continue
		}
		ip, ok := netip.AddrFromSlice(n.IP)
		ones, bits := n.Mask.Size()
		if ip.Is4In6() && bits == 128 {
			ones -= 96
		}
		if ok && netip.PrefixFrom(ip.Unmap(), ones).Masked().Contains(a) {
			return true
		}
	}
	return false
}

func (g *Grants) Check(subnets []netip.Prefix, lists map[string]Lists) (map[string]Policy, error) {
	next := map[string]Policy{}
	base := g.base.Current()
	for key, l := range lists {
		p, err := netip.ParsePrefix(key)
		if err != nil || p.String() != key || !slices.Contains(subnets, p) {
			return nil, fmt.Errorf("egress: grants name %q, which isn't a session container's subnet", key)
		}
		if len(l.Allow) > MaxEntries || len(l.Ports) > MaxEntries || len(l.Expires) > 2*MaxEntries {
			return nil, fmt.Errorf("egress: the grants for %s hold more than %d entries", key, MaxEntries)
		}
		pol, err := base.withGrant(l.Allow, l.Ports, l.Expires)
		if err != nil {
			return nil, fmt.Errorf("egress: grants for %s: %w", key, err)
		}
		next[key] = pol
	}
	return next, nil
}

func (g *Grants) gitTarget(git []string, entry string) bool {
	host := entry
	if h, _, ok := session.SplitPortTarget(entry); ok {
		host = h
	}
	if a, err := netip.ParseAddr(host); err == nil {
		return g.known.gitAddress(a)
	}
	return slices.Contains(git, host) || slices.Contains(session.ForgeSSHNames, host) || gitHost(g.base.Current().Deny(), host)
}

func (g *Grants) Replace(next map[string]Policy) {
	g.ReplaceAlone(next, nil)
}

func (g *Grants) ReplaceAlone(next map[string]Policy, alone []netip.Prefix) {
	only := map[string]bool{}
	for _, p := range alone {
		only[p.String()] = true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.lists, g.alone = next, only
}

func (g *Grants) only(subnet string) bool {
	if g == nil || subnet == "" {
		return false
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.alone[subnet]
}

func (g *Grants) allows(subnet, name string) bool {
	if g == nil || subnet == "" {
		return false
	}
	g.mu.RLock()
	p, ok := g.lists[subnet]
	g.mu.RUnlock()
	return ok && !p.denied(name) && p.allowed(name)
}

func (g *Grants) allowsPort(subnet, target string) bool {
	if g == nil || subnet == "" {
		return false
	}
	g.mu.RLock()
	p, ok := g.lists[subnet]
	g.mu.RUnlock()
	return ok && p.portAllowed(target)
}

func (g *Grants) snapshot() map[string]sideJSON {
	out := map[string]sideJSON{}
	if g == nil {
		return out
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	for key, p := range g.lists {
		sj := sideJSON{Allow: p.Allow(), Ports: p.Ports(), Expires: map[string]int64{}}
		for h, t := range p.Expires() {
			sj.Expires[h] = t.Unix()
		}
		out[key] = sj
	}
	return out
}

type SubnetSeen struct {
	mu      sync.Mutex
	subnets map[string]map[string]*SeenRefusal
	now     func() time.Time
}

func NewSubnetSeen() *SubnetSeen {
	return &SubnetSeen{subnets: map[string]map[string]*SeenRefusal{}, now: time.Now}
}

func (s *SubnetSeen) add(subnet, host string) {
	if s == nil || subnet == "" || host == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	hosts := s.subnets[subnet]
	if hosts == nil {
		if len(s.subnets) >= MaxSubnets {
			return
		}
		hosts = map[string]*SeenRefusal{}
		s.subnets[subnet] = hosts
	}
	e := hosts[host]
	if e == nil {
		if len(hosts) >= MaxSubnetHosts {
			oldest, at := "", time.Time{}
			for h, v := range hosts {
				if oldest == "" || v.Last.Before(at) {
					oldest, at = h, v.Last
				}
			}
			delete(hosts, oldest)
		}
		e = &SeenRefusal{Host: host, Reason: ReasonNotAllowlisted}
		hosts[host] = e
	}
	e.Count = min(e.Count+1, maxCount)
	e.Last = s.now()
}

func (s *SubnetSeen) Keep(live []netip.Prefix) {
	if s == nil {
		return
	}
	keep := map[string]bool{}
	for _, p := range live {
		keep[p.String()] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key := range s.subnets {
		if !keep[key] {
			delete(s.subnets, key)
		}
	}
}

func (s *SubnetSeen) list() map[string][]seenJSON {
	out := map[string][]seenJSON{}
	if s == nil {
		return out
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, hosts := range s.subnets {
		list := make([]seenJSON, 0, len(hosts))
		for _, e := range hosts {
			list = append(list, seenJSON{Host: e.Host, Reason: e.Reason, Count: e.Count, Last: e.Last.Unix()})
		}
		slices.SortFunc(list, func(a, b seenJSON) int { return int(b.Last - a.Last) })
		out[key] = list
	}
	return out
}
