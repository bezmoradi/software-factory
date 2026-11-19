package session

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	AuditEgress     = "egress-allow"
	ExpireWatch     = 15 * time.Second
	egressSetFor    = 10 * time.Second
	maxRefusalsShow = 200
)

var (
	expiryChoices = map[string]time.Duration{"never": 0, "hour": time.Hour, "day": 24 * time.Hour, "week": 7 * 24 * time.Hour}
	expiryWords   = map[string]string{"hour": " for an hour", "day": " for a day", "week": " for a week"}
)

type EgressList struct {
	Allow   []string
	Expires map[string]time.Time
}

type EgressLists struct {
	Agents    EgressList
	Reviewers EgressList
}

func (l EgressLists) Side(side string) EgressList {
	if side == SideReviewers {
		return l.Reviewers
	}
	return l.Agents
}

type EgressState struct {
	Starting bool
	Lists    EgressLists
	Deny     []string
}

type AddedHost struct {
	Name    string
	Expires time.Time
	AddedAt time.Time
	Reason  string
}

type Gateway struct {
	URL       string
	Host      string
	Agents    bool
	Reviewers bool
}

type EgressSettings struct {
	Added   []AddedHost
	Gateway Gateway
}

type EgressStore interface {
	Load() (EgressSettings, bool, error)
	Save(EgressSettings) error
}

type EgressRefusal struct {
	Side   string
	Host   string
	Reason string
	Count  int
	Last   time.Time
}

type EgressControl interface {
	Status(ctx context.Context) (EgressState, error)
	Set(ctx context.Context, lists EgressLists) error
	Refused(ctx context.Context) ([]EgressRefusal, error)
}

type GatewayKeys interface {
	ClearSide(ctx context.Context, side string) error
	RunningOn(side string) []string
}

type EgressView struct {
	Saved   bool
	Pending bool
	Added   []AddedHost
	Gateway *Gateway
	Lists   EgressLists
	Base    []string
	Deny    []string
	Now     time.Time
}

type RefusalView struct {
	EgressRefusal
	Addable bool
	GitHost bool
	Uploads bool
	Probe   bool
	Suggest []string
}

type EgressPage interface {
	EgressView() (EgressView, error)
	AddHost(ctx context.Context, name, expiry string, uploadsOk bool, reason string) error
	RemoveHost(ctx context.Context, name string) error
	SetGateway(ctx context.Context, rawURL string, agents, reviewers bool) error
	EgressRefusals(ctx context.Context) ([]RefusalView, error)
}

type AllowlistConfig struct {
	Store   EgressStore
	Control EgressControl
	Base    []string
	Deny    []string
	Outside string
	Repos   func() []string
	Audit   Audit
	Changed func()
	Now     func() time.Time
	Logger  shared.Logger
}

type Allowlist struct {
	cfg      AllowlistConfig
	mu       sync.Mutex
	settings EgressSettings
	saved    bool
	broken   bool
	pending  bool
	keys     GatewayKeys
	gwMu     sync.Mutex
	sawStart bool
	onStart  []func()
}

func (a *Allowlist) OnRestart(f func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.onStart = append(a.onStart, f)
}

func (a *Allowlist) restarted() {
	a.mu.Lock()
	saw := a.sawStart
	a.sawStart = false
	fs := a.onStart
	a.mu.Unlock()
	if !saw {
		return
	}
	for _, f := range fs {
		f()
	}
}

var (
	_ EgressPage    = (*Allowlist)(nil)
	_ GatewayRoutes = (*Allowlist)(nil)
)

func NewAllowlist(cfg AllowlistConfig) (*Allowlist, error) {
	if cfg.Store == nil || cfg.Control == nil {
		return nil, errors.New("allowlist: needs a store and egress's control channel")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = quiet{}
	}
	cfg.Base = sortedHosts(cfg.Base)
	cfg.Deny = sortedHosts(cfg.Deny)
	a := &Allowlist{cfg: cfg, pending: true}
	s, ok, err := cfg.Store.Load()
	switch {
	case err != nil:
		a.broken = true
		cfg.Logger.Warn("egress allowlist", "event", "unreadable")
	case ok:
		a.settings, a.saved = s, true
	}
	return a, nil
}

func (a *Allowlist) OnChange(f func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cfg.Changed = f
}

func (a *Allowlist) UseKeys(k GatewayKeys) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.keys = k
}

func (a *Allowlist) changed() func() {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.Changed
}

func (a *Allowlist) derived() EgressSettings {
	var s EgressSettings
	known := map[string]bool{}
	for _, ag := range AgentHostTable() {
		for _, h := range ag.Hosts {
			known[h] = true
		}
	}
	for _, h := range a.cfg.Base {
		if !known[h] {
			s.Added = append(s.Added, AddedHost{Name: h})
		}
	}
	return s
}

func (a *Allowlist) current() EgressSettings {
	if a.saved {
		return a.settings
	}
	return a.derived()
}

func (a *Allowlist) wantLocked(side string) EgressList {
	if !a.saved {
		return EgressList{Allow: slices.Clone(a.cfg.Base), Expires: map[string]time.Time{}}
	}
	s := a.settings
	fixed := map[string]bool{}
	for _, h := range agentHostsOf("claude") {
		fixed[h] = true
	}
	if side == SideReviewers && s.Gateway.Reviewers && s.Gateway.Host != "" || side != SideReviewers && s.Gateway.Agents && s.Gateway.Host != "" {
		fixed[s.Gateway.Host] = true
	}
	expires := map[string]time.Time{}
	allow := slices.Collect(maps.Keys(fixed))
	if side != SideReviewers {
		for _, h := range s.Added {
			if fixed[h.Name] {
				continue
			}
			allow = append(allow, h.Name)
			if !h.Expires.IsZero() {
				expires[h.Name] = h.Expires.Truncate(time.Second)
			}
		}
	}
	if gw := gatewayHostFor(s.Gateway, side); gw != "" {
		allow = slices.DeleteFunc(allow, func(h string) bool { return h != gw && ClaudeVendorHost(h) })
		for h := range expires {
			if !slices.Contains(allow, h) {
				delete(expires, h)
			}
		}
	}
	return EgressList{Allow: sortedHosts(allow), Expires: expires}
}

func (a *Allowlist) lists() EgressLists {
	return EgressLists{Agents: a.wantLocked(SideAgents), Reviewers: a.wantLocked(SideReviewers)}
}

func (a *Allowlist) syncLocked(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, egressSetFor)
	defer cancel()
	l := a.lists()
	err := a.cfg.Control.Set(ctx, l)
	a.pending = err != nil
	if err != nil {
		shared.For(ctx, a.cfg.Logger).Warn("egress allowlist", "event", "not-set")
		return err
	}
	shared.For(ctx, a.cfg.Logger).Info("egress allowlist", "event", "set", "agents", len(l.Agents.Allow), "reviewers", len(l.Reviewers.Allow))
	return nil
}

func SameList(want, got EgressList) bool {
	if !slices.Equal(sortedHosts(want.Allow), sortedHosts(got.Allow)) || len(want.Expires) != len(got.Expires) {
		return false
	}
	for h, t := range want.Expires {
		g, ok := got.Expires[h]
		if !ok || g.Unix() != t.Unix() {
			return false
		}
	}
	return true
}

func (a *Allowlist) Verify(ctx context.Context, side string, read func(context.Context) (EgressState, error)) (EgressList, EgressState, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pruneLocked() {
		_ = a.syncLocked(ctx)
	}
	got, err := read(ctx)
	if err != nil {
		return EgressList{}, EgressState{}, err
	}
	a.sawStart = a.sawStart || got.Starting
	want := a.wantLocked(side)
	if !got.Starting && (SameList(want, got.Lists.Side(side)) || !a.pending) {
		return want, got, nil
	}
	if a.syncLocked(ctx) != nil {
		return want, got, nil
	}
	shared.For(ctx, a.cfg.Logger).Info("egress allowlist", "event", "restored", "starting", got.Starting)
	if again, err := read(ctx); err == nil {
		got = again
	}
	return want, got, nil
}

func (a *Allowlist) pruneLocked() bool {
	if !a.saved {
		return false
	}
	now := a.cfg.Now()
	kept := a.settings.Added[:0:0]
	for _, h := range a.settings.Added {
		if h.Expires.IsZero() || now.Before(h.Expires) {
			kept = append(kept, h)
		}
	}
	if len(kept) == len(a.settings.Added) {
		return false
	}
	a.settings.Added = kept
	if err := a.cfg.Store.Save(a.settings); err != nil {
		a.cfg.Logger.Warn("egress allowlist", "event", "not-saved")
	}
	a.cfg.Logger.Info("egress allowlist", "event", "expired")
	return true
}

func (a *Allowlist) Watch(ctx context.Context, every time.Duration) {
	if f := a.changed(); a.tend(ctx) && f != nil {
		f()
	}
	a.restarted()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if f := a.changed(); a.tend(ctx) && f != nil {
				f()
			}
			a.restarted()
		}
	}
}

func (a *Allowlist) tend(ctx context.Context) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pruneLocked() {
		_ = a.syncLocked(ctx)
		return true
	}
	sctx, cancel := context.WithTimeout(ctx, egressSetFor)
	got, err := a.cfg.Control.Status(sctx)
	cancel()
	a.sawStart = a.sawStart || err == nil && got.Starting
	if err != nil || !got.Starting && !a.pending {
		return false
	}
	l := a.lists()
	if !got.Starting && SameList(l.Agents, got.Lists.Agents) && SameList(l.Reviewers, got.Lists.Reviewers) {
		a.pending = false
		return false
	}
	if a.syncLocked(ctx) != nil {
		return false
	}
	shared.For(ctx, a.cfg.Logger).Info("egress allowlist", "event", "restored", "starting", got.Starting)
	return true
}

func (a *Allowlist) BaseURL(side string) string {
	if a == nil {
		return ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	g := a.settings.Gateway
	if !a.saved || g.URL == "" || side == SideAgents && !g.Agents || side == SideReviewers && !g.Reviewers {
		return ""
	}
	return g.URL
}

func (a *Allowlist) GatewayHost(side string) (string, bool) {
	if a == nil {
		return "", true
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.broken {
		return "", false
	}
	if !a.saved || a.settings.Gateway.URL == "" {
		return "", true
	}
	return gatewayHostFor(a.settings.Gateway, side), true
}

func (a *Allowlist) HoldGateway() func() {
	a.gwMu.Lock()
	return a.gwMu.Unlock
}

func (a *Allowlist) EgressView() (EgressView, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.current()
	v := EgressView{Saved: a.saved, Pending: a.pending, Lists: a.lists(), Base: slices.Clone(a.cfg.Base), Deny: slices.Clone(a.cfg.Deny), Now: a.cfg.Now(), Added: slices.Clone(s.Added)}
	if s.Gateway.URL != "" {
		g := s.Gateway
		v.Gateway = &g
	}
	if a.broken {
		return v, explained(conflict("state"), "The engine couldn't read egress.json, so it uses the starting list from SF_ALLOW and SF_EXTRA_ALLOW and saves no change. Move the file out of the engine's state folder to start again.")
	}
	return v, nil
}

func (a *Allowlist) change(ctx context.Context, what, words string, edit func(*EgressSettings) error) error {
	if a.broken {
		return explained(conflict("state"), "The engine couldn't read egress.json, so it saves no change to the allowlist.")
	}
	a.mu.Lock()
	s := a.current()
	s.Added = slices.Clone(s.Added)
	if err := edit(&s); err != nil {
		a.mu.Unlock()
		return err
	}
	if err := a.cfg.Store.Save(s); err != nil {
		a.mu.Unlock()
		return explained(conflict("state"), "The engine couldn't save the allowlist.")
	}
	a.settings, a.saved = s, true
	a.pruneLocked()
	err := a.syncLocked(ctx)
	a.mu.Unlock()
	a.audit(what, words)
	if f := a.changed(); f != nil {
		f()
	}
	if err != nil {
		return explained(conflict("egress"), "Saved, but egress didn't take the new list, so the sandbox check fails and sessions are Held until it does. The engine sets it again at the next check.")
	}
	return nil
}

func (a *Allowlist) audit(what, words string) {
	if a.cfg.Audit == nil {
		return
	}
	if err := masking(a.cfg.Audit).Append(AuditEntry{Time: a.cfg.Now(), Kind: AuditEgress, By: "page", Result: what, Reason: words}); err != nil {
		a.cfg.Logger.Warn("egress allowlist", "event", "audit-not-written")
	}
}

func (a *Allowlist) refuseHost(name string) error {
	if !ValidAllowEntry(name) {
		return explained(invalid("host"), "Give a host name with at least two labels, such as registry.npmjs.org, or a leading dot for its subdomains, such as .example.com; not an IP address, a port or a path.")
	}
	if d := DeniedBy(a.cfg.Deny, name); d != "" {
		return explained(conflict("git host"), name+" is a git host or covers one ("+d+"). Agents never reach a git host: the engine pushes for them.")
	}
	if a.cfg.Outside != "" && (EntryCovers(name, a.cfg.Outside) || name == "."+a.cfg.Outside) {
		return explained(conflict("outside host"), "The sandbox check uses "+a.cfg.Outside+" as a host egress must refuse, so it can't be allowed. Set SF_SANDBOX_OUTSIDE to another host first.")
	}
	if a.cfg.Repos != nil {
		for _, r := range a.cfg.Repos() {
			if r = HostName(r); EntryCovers(name, r) || name == r {
				return explained(conflict("repository host"), name+" covers "+r+", the host of a repository you added, so agents could push there. Add "+r+" to SF_EXTRA_DENY, or allow a narrower host.")
			}
		}
	}
	return nil
}

func (a *Allowlist) AddHost(ctx context.Context, raw, expiry string, uploadsOk bool, reason string) error {
	name := HostName(raw)
	if err := a.refuseHost(name); err != nil {
		return err
	}
	why, err := CleanHostReason(reason)
	if err != nil {
		return err
	}
	ttl, ok := expiryChoices[expiry]
	if !ok {
		return explained(invalid("expires"), "Choose never, an hour, a day or a week.")
	}
	if UploadHost(name) && !uploadsOk {
		msg := name + " " + UploadsWarning
		if PyPIHost(name) {
			msg += " " + PyPIReadHostsNote
		}
		return explained(conflict("uploads"), msg)
	}
	now := a.cfg.Now()
	h := AddedHost{Name: name, AddedAt: now.Truncate(time.Second), Reason: why}
	words := "added " + name
	if ttl > 0 {
		h.Expires = now.Add(ttl).Truncate(time.Second)
		words += expiryWords[expiry]
	}
	if why != "" {
		words += ", because: " + why
	}
	return a.change(ctx, "added", words, func(s *EgressSettings) error {
		s.Added = slices.DeleteFunc(s.Added, func(x AddedHost) bool { return x.Name == name })
		if len(s.Added) >= MaxAddedHosts {
			return explained(limited("hosts"), "The allowlist holds at most 64 added hosts. Remove one first.")
		}
		s.Added = append(s.Added, h)
		return nil
	})
}

func (a *Allowlist) RemoveHost(ctx context.Context, raw string) error {
	name := HostName(raw)
	return a.change(ctx, "removed", "removed "+clipWords(name), func(s *EgressSettings) error {
		n := len(s.Added)
		s.Added = slices.DeleteFunc(s.Added, func(x AddedHost) bool { return x.Name == name })
		if len(s.Added) == n {
			return explained(notFound("host"), "That host isn't one you added.")
		}
		return nil
	})
}

func (a *Allowlist) SetGateway(ctx context.Context, raw string, agents, reviewers bool) error {
	next, words := Gateway{}, "gateway removed"
	if strings.TrimSpace(raw) != "" {
		u, host, err := CheckGatewayURL(raw)
		if err != nil {
			return err
		}
		if err := a.refuseHost(host); err != nil {
			return err
		}
		if !agents && !reviewers {
			return explained(invalid("gateway"), "Choose the agents, the reviewers or both for the gateway.")
		}
		next, words = Gateway{URL: u, Host: host, Agents: agents, Reviewers: reviewers}, "gateway "+host+sides(agents, reviewers)
	}
	a.gwMu.Lock()
	defer a.gwMu.Unlock()
	if err := a.gatewayMayChange(ctx, next); err != nil {
		return err
	}
	if err := a.dropKeys(ctx, next); err != nil {
		return err
	}
	return a.change(ctx, "gateway", words, func(s *EgressSettings) error {
		s.Gateway = next
		return nil
	})
}

func (a *Allowlist) gatewayMayChange(ctx context.Context, next Gateway) error {
	a.mu.Lock()
	broken, was, keys := a.broken, a.current().Gateway, a.keys
	a.mu.Unlock()
	if broken {
		return explained(conflict("state"), "The engine couldn't read egress.json, so it saves no change to the allowlist.")
	}
	if keys == nil {
		return explained(conflict("gateway"), "This engine can't remove a key saved for Claude Code, so it doesn't change the gateway.")
	}
	var running []string
	for _, side := range []string{SideAgents, SideReviewers} {
		if gatewayHostFor(was, side) != gatewayHostFor(next, side) {
			running = append(running, keys.RunningOn(side)...)
		}
	}
	if len(running) > 0 {
		return explained(conflict("running"), "Stop these sessions first: "+runningWords(running)+". They started with the old gateway setting, and Claude Code would send the next key you paste to the address they started with. Reviews finish by themselves; wait for them.")
	}
	if next.Host == "" {
		return nil
	}
	sctx, cancel := context.WithTimeout(ctx, egressSetFor)
	defer cancel()
	if _, err := a.cfg.Control.Status(sctx); err != nil {
		return explained(conflict("egress"), "The gateway wasn't changed: egress's control channel doesn't answer, so the engine couldn't let Claude Code reach the gateway. Check that egress runs: "+Compose()+" ps egress.")
	}
	return nil
}

func gatewayHostFor(g Gateway, side string) string {
	if side == SideAgents && g.Agents || side == SideReviewers && g.Reviewers {
		return g.Host
	}
	return ""
}

func (a *Allowlist) dropKeys(ctx context.Context, next Gateway) error {
	a.mu.Lock()
	was, keys := a.current().Gateway, a.keys
	a.mu.Unlock()
	for _, side := range []string{SideAgents, SideReviewers} {
		if gatewayHostFor(was, side) == gatewayHostFor(next, side) {
			continue
		}
		if err := keys.ClearSide(ctx, side); err != nil {
			why := "The gateway wasn't changed. Claude Code would send the key saved in the " + containerOf(side) + " to the new address, and it couldn't be removed first"
			if e, ok := errors.AsType[*Error](err); ok && e.Message != "" {
				why += ": " + e.Message
			} else {
				why += "."
			}
			return explained(conflict("gateway"), why)
		}
	}
	return nil
}

func containerOf(side string) string {
	if side == SideReviewers {
		return "reviewer containers"
	}
	return "sessions' containers"
}

func sides(agents, reviewers bool) string {
	switch {
	case agents && reviewers:
		return " for agents and reviewers"
	case agents:
		return " for agents"
	}
	return " for reviewers"
}

func (a *Allowlist) EgressRefusals(ctx context.Context) ([]RefusalView, error) {
	ctx, cancel := context.WithTimeout(ctx, egressSetFor)
	defer cancel()
	got, err := a.cfg.Control.Refused(ctx)
	if err != nil {
		return nil, explained(conflict("egress"), "egress didn't say what it refused. Check that it runs: "+Compose()+" ps egress.")
	}
	var repos []string
	if a.cfg.Repos != nil {
		repos = a.cfg.Repos()
	}
	a.mu.Lock()
	agentsWant := a.wantLocked(SideAgents)
	agentsGateway := a.saved && gatewayHostFor(a.settings.Gateway, SideAgents) != ""
	a.mu.Unlock()
	out := make([]RefusalView, 0, len(got))
	for _, r := range got {
		r.Host = HostName(r.Host)
		v := RefusalView{EgressRefusal: r}
		v.GitHost = DeniedBy(a.cfg.Deny, r.Host) != ""
		v.Probe = v.GitHost || r.Host == a.cfg.Outside || slices.ContainsFunc(repos, func(h string) bool { return HostName(h) == r.Host })
		v.Uploads = UploadHost(r.Host)
		if PyPIHost(r.Host) {
			v.Suggest = slices.Clone(PyPIReadHosts)
		}
		allowed := slices.ContainsFunc(agentsWant.Allow, func(e string) bool { return EntryCovers(e, r.Host) })
		v.Addable = r.Side == SideAgents && r.Reason == "not_allowlisted" && !allowed && (!agentsGateway || !ClaudeVendorHost(r.Host)) && a.refuseHost(r.Host) == nil
		out = append(out, v)
	}
	slices.SortFunc(out, func(x, y RefusalView) int { return y.Last.Compare(x.Last) })
	if len(out) > maxRefusalsShow*2 {
		out = out[:maxRefusalsShow*2]
	}
	return out, nil
}

func clipWords(s string) string {
	if len(s) > 80 {
		return s[:80]
	}
	return s
}
