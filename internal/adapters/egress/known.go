package egress

import (
	"context"
	"encoding/hex"
	"io"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	knownEvery   = time.Minute
	knownFor     = 5 * time.Second
	routeFile    = "/proc/net/route"
	maxRouteFile = 64 << 10
)

var DockerHostNames = []string{"host.docker.internal", "gateway.docker.internal", "docker.for.mac.host.internal", "docker.for.mac.localhost", "kubernetes.docker.internal"}

type Known struct {
	mu      sync.RWMutex
	repos   []string
	byName  map[string][]netip.Addr
	git     map[netip.Addr]string
	host    map[netip.Addr]bool
	ready   chan struct{}
	readied sync.Once
	kick    chan struct{}
}

func NewKnown() *Known {
	return &Known{byName: map[string][]netip.Addr{}, git: map[netip.Addr]string{}, host: map[netip.Addr]bool{}, ready: make(chan struct{}), kick: make(chan struct{}, 1)}
}

func (k *Known) setRepos(names []string) {
	names = sortedUnique(names)
	k.mu.Lock()
	same := slices.Equal(k.repos, names)
	k.repos = names
	k.mu.Unlock()
	if same {
		return
	}
	select {
	case k.kick <- struct{}{}:
	default:
	}
}

func (k *Known) Watch(ctx context.Context, resolver Resolver, gitNames func() []string) {
	t := time.NewTicker(knownEvery)
	defer t.Stop()
	for {
		k.refresh(ctx, resolver, gitNames())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-k.kick:
		}
	}
}

func (k *Known) refresh(ctx context.Context, resolver Resolver, given []string) {
	k.mu.RLock()
	gitNames := sortedUnique(slices.Concat(given, k.repos, session.ForgeSSHNames))
	k.mu.RUnlock()
	names := append(slices.Clone(gitNames), DockerHostNames...)
	rctx, cancel := context.WithTimeout(ctx, knownFor)
	defer cancel()
	found := make([][]netip.Addr, len(names))
	ok := make([]bool, len(names))
	var wg sync.WaitGroup
	for i, n := range names {
		if a, err := netip.ParseAddr(n); err == nil {
			found[i], ok[i] = []netip.Addr{a}, true
			continue
		}
		wg.Go(func() {
			got, err := resolver.LookupNetIP(rctx, "ip", n+".")
			found[i], ok[i] = got, err == nil
		})
	}
	wg.Wait()
	k.mu.Lock()
	next := map[string][]netip.Addr{}
	for i, n := range names {
		if ok[i] {
			next[n] = found[i]
		} else {
			next[n] = k.byName[n]
		}
	}
	k.byName = next
	k.git, k.host = map[netip.Addr]string{}, map[netip.Addr]bool{}
	for _, n := range gitNames {
		for _, a := range next[n] {
			if _, had := k.git[a.Unmap()]; !had {
				k.git[a.Unmap()] = n
			}
		}
	}
	for _, n := range DockerHostNames {
		for _, a := range next[n] {
			k.host[a.Unmap()] = true
		}
	}
	for _, a := range routeGateways() {
		k.host[a] = true
	}
	k.mu.Unlock()
	k.readied.Do(func() { close(k.ready) })
}

func (k *Known) wait(ctx context.Context) bool {
	select {
	case <-k.ready:
		return true
	case <-ctx.Done():
		return false
	}
}

func (k *Known) GitHost(a netip.Addr) string {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.git[a.Unmap()]
}

func (k *Known) gitAddress(a netip.Addr) bool {
	return k.GitHost(a) != ""
}

func (k *Known) dockerHost(a netip.Addr) bool {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.host[a.Unmap()]
}

func (k *Known) DockerHosts() []netip.Addr {
	k.mu.RLock()
	defer k.mu.RUnlock()
	out := make([]netip.Addr, 0, len(k.host))
	for a := range k.host {
		out = append(out, a)
	}
	slices.SortFunc(out, func(x, y netip.Addr) int { return x.Compare(y) })
	return out
}

func routeGateways() []netip.Addr {
	f, err := os.Open(routeFile)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	data, _ := io.ReadAll(io.LimitReader(f, maxRouteFile))
	var out []netip.Addr
	for i, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if i == 0 || len(fields) < 3 {
			continue
		}
		b, err := hex.DecodeString(fields[2])
		if err != nil || len(b) != 4 {
			continue
		}
		a := netip.AddrFrom4([4]byte{b[3], b[2], b[1], b[0]})
		if !a.IsUnspecified() && !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out
}
