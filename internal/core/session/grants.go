package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	AuditAccess      = "access"
	GrantSession     = "session"
	GrantProject     = "project"
	AskedByAgent     = "agent"
	AskedByPerson    = "person"
	AskedByPreset    = "preset"
	MaxGrants        = 64
	MaxEgressEntries = 256
	ResultGranted    = "allowed"
	ResultNotGranted = "refused"
	ResultGrantEnded = "ended"
	ResultTakenBack  = "taken-back"
	grantApplyFor    = 10 * time.Second
	grantIDBytes     = 16
	credentialKey    = "credential:"
)

type Grant struct {
	ID          string
	Host        string
	Port        int
	Credential  string
	Name        string
	Bound       []string
	Delivery    string
	Scope       string
	Session     string
	Project     string
	Expires     time.Time
	Reason      string
	AskedBy     string
	Preset      string
	Made        time.Time
	GitAccepted []string
}

func (gr Grant) Target() string {
	if gr.Port > 0 {
		return PortTarget(gr.Host, gr.Port)
	}
	return gr.Host
}

func (gr Grant) Hosts() []string {
	if gr.Credential != "" {
		return gr.Bound
	}
	return []string{gr.Target()}
}

func (gr Grant) what() string {
	if gr.Credential != "" {
		return gr.Name
	}
	return gr.Target()
}

func (gr Grant) key() string {
	if gr.Credential != "" {
		return credentialKey + gr.Credential
	}
	return gr.Target()
}

type GrantStore interface {
	Load() ([]Grant, error)
	Save([]Grant) error
}

type GrantTarget struct {
	SID      string
	Project  string
	Branch   string
	Box      string
	Worktree string
	Live     bool
	Running  bool
}

type GrantsConfig struct {
	Store      GrantStore
	Creds      CredentialStore
	Presets    PresetStore
	SSH        SSHKeys
	AWS        AWSCopies
	Allow      *Allowlist
	Session    func(sid string) (GrantTarget, bool)
	Targets    func() []GrantTarget
	InBox      func(box string) (string, bool)
	Host       func(box string) (HandoverHost, bool)
	Projects   func() []string
	Stack      func() []netip.Prefix
	GitAddress func(netip.Addr) string
	Apply      func(ctx context.Context) error
	Show       func(sid string, v AccessShown)
	Note       func(sid, text string)
	Actions    ActionsConfig
	Audit      Audit
	Now        func() time.Time
	Logger     shared.Logger
}

type Grants struct {
	cfg           GrantsConfig
	mu            sync.Mutex
	grants        []Grant
	broken        bool
	creds         []Credential
	credsBroken   bool
	presets       []Preset
	presetsBroken bool
	cards         map[string][]*accessCard
	answered      map[string]map[string]time.Time
	refused       map[string]map[string]time.Time
	asks          *limiter
	shown         map[string]string
	notes         map[string][]AccessNote
	autoDone      map[string]map[string]bool
	deliverMu     sync.Mutex
	delivered     map[string]string
	unsent        map[string]bool
	copies        map[string]awsCopy
	copyNotes     map[string][]string
	pubs          map[string]sshPublic
	used          map[string]signUse
	signs         *limiter
	actions       []Action
	actionsBroken bool
	actionGrants  []actionGrant
	runs          []ActionRun
	liveRuns      map[string]*actionLive
	actionAsks    *limiter
}

func NewGrants(cfg GrantsConfig) (*Grants, error) {
	if cfg.Store == nil || cfg.Creds == nil || cfg.Presets == nil || cfg.SSH == nil || cfg.AWS == nil || cfg.Allow == nil || cfg.Session == nil || cfg.Targets == nil || cfg.InBox == nil || cfg.Host == nil {
		return nil, errors.New("grants: need the stores, the signer, the AWS copier, the allowlist, the sessions and their hosts")
	}
	if cfg.Projects == nil {
		cfg.Projects = func() []string { return nil }
	}
	if cfg.GitAddress == nil {
		cfg.GitAddress = func(netip.Addr) string { return "" }
	}
	if cfg.Apply == nil {
		cfg.Apply = func(context.Context) error { return nil }
	}
	if cfg.Show == nil {
		cfg.Show = func(string, AccessShown) {}
	}
	if cfg.Note == nil {
		cfg.Note = func(string, string) {}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = quiet{}
	}
	cfg.Audit = masking(cfg.Audit)
	g := &Grants{cfg: cfg, cards: map[string][]*accessCard{}, answered: map[string]map[string]time.Time{}, refused: map[string]map[string]time.Time{},
		asks: newLimiter(AccessAsks, AccessAskWindow), shown: map[string]string{}, notes: map[string][]AccessNote{}, autoDone: map[string]map[string]bool{}, delivered: map[string]string{}, unsent: map[string]bool{},
		copies: map[string]awsCopy{}, copyNotes: map[string][]string{}, pubs: map[string]sshPublic{}, used: map[string]signUse{}, signs: newLimiter(SSHSigns, SSHSignWindow),
		liveRuns: map[string]*actionLive{}, actionAsks: newLimiter(AccessAsks, AccessAskWindow)}
	if a := cfg.Actions; a.Store != nil && (a.Host == nil || a.Files == nil || a.Net == nil) {
		g.cfg.Actions = ActionsConfig{}
	}
	if list, err := cfg.Store.Load(); err != nil {
		g.broken = true
		cfg.Logger.Warn("grants", "event", "unreadable")
	} else {
		g.grants = list
	}
	if list, err := cfg.Creds.Load(); err != nil {
		g.credsBroken = true
		cfg.Logger.Warn("credentials", "event", "unreadable")
	} else {
		g.creds = list
	}
	if g.cfg.Actions.Store != nil {
		if list, err := g.cfg.Actions.Store.Load(); err != nil {
			g.actionsBroken = true
			cfg.Logger.Warn("actions", "event", "unreadable")
		} else {
			g.actions = list
		}
	}
	if list, err := cfg.Presets.Load(); err != nil {
		g.presetsBroken = true
		cfg.Logger.Warn("presets", "event", "unreadable")
	} else {
		g.presets = list
	}
	return g, nil
}

func newGrantID() string {
	b := make([]byte, grantIDBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func grantLive(gr Grant, now time.Time) bool {
	return gr.Expires.IsZero() || now.Before(gr.Expires)
}

func covers(gr Grant, t GrantTarget) bool {
	if gr.Scope == GrantProject {
		return gr.Project != "" && gr.Project == t.Project
	}
	return gr.Session == t.SID
}

func (g *Grants) usableLocked(gr Grant) bool {
	if gr.Credential == "" {
		return true
	}
	if g.credsBroken {
		return false
	}
	_, ok := g.credentialByID(gr.Credential)
	return ok
}

func (g *Grants) ForBox(box string) EgressList {
	out := EgressList{Allow: []string{}, Ports: []string{}, Expires: map[string]time.Time{}, Git: []string{}}
	sid, ok := g.cfg.InBox(box)
	if !ok {
		return out
	}
	t, ok := g.cfg.Session(sid)
	if !ok || !t.Live {
		return out
	}
	gateway := g.gatewayOn()
	git := g.gitHosts()
	now := g.cfg.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	lasting := map[string]bool{}
	for _, gr := range g.grants {
		if !covers(gr, t) || !grantLive(gr, now) || !g.usableLocked(gr) {
			continue
		}
		open, gits, _ := opens(gr, git)
		for _, h := range open {
			if gateway && ClaudeVendorHost(h) {
				continue
			}
			switch {
			case portEntry(h):
				if !slices.Contains(out.Ports, h) {
					out.Ports = append(out.Ports, h)
				}
			case !slices.Contains(out.Allow, h):
				out.Allow = append(out.Allow, h)
			}
			if slices.Contains(gits, h) && !slices.Contains(out.Git, h) {
				out.Git = append(out.Git, h)
			}
			switch {
			case gr.Expires.IsZero():
				lasting[h] = true
				delete(out.Expires, h)
			case !lasting[h]:
				if was, ok := out.Expires[h]; !ok || gr.Expires.After(was) {
					out.Expires[h] = gr.Expires.Truncate(time.Second)
				}
			}
		}
	}
	out.Allow = sortedHosts(out.Allow)
	out.Ports = sortedHosts(out.Ports)
	out.Git = sortedHosts(out.Git)
	return out
}

func (g *Grants) gitHosts() func(string) string {
	byName := g.cfg.Allow.gitCheck()
	return func(entry string) string {
		host := targetHost(entry)
		if a, err := netip.ParseAddr(host); err == nil {
			return g.cfg.GitAddress(a.Unmap())
		}
		if byName(host) {
			return host
		}
		return ""
	}
}

func (g *Grants) gitsOf(entries []string) []string {
	git := g.gitHosts()
	var out []string
	for _, e := range entries {
		if git(e) != "" {
			out = append(out, e)
		}
	}
	return out
}

func opens(gr Grant, git func(string) string) ([]string, []string, []string) {
	var open, gits, closed []string
	for _, h := range gr.Hosts() {
		switch {
		case git(h) == "":
			open = append(open, h)
		case slices.Contains(gr.GitAccepted, h):
			open, gits = append(open, h), append(gits, h)
		default:
			closed = append(closed, h)
		}
	}
	return open, gits, closed
}

func acceptedOf(accepted, hosts []string) []string {
	var out []string
	for _, h := range hosts {
		if slices.Contains(accepted, h) && !slices.Contains(out, h) {
			out = append(out, h)
		}
	}
	return out
}

func (g *Grants) overLimitLocked(next []Grant, gr Grant, targets []GrantTarget, now time.Time) bool {
	check := []GrantTarget{{SID: gr.Session, Project: gr.Project}}
	if gr.Scope == GrantProject {
		check = append(check, GrantTarget{Project: gr.Project})
		for _, t := range targets {
			if t.Project == gr.Project {
				check = append(check, t)
			}
		}
	}
	for _, t := range check {
		allow, ports := map[string]bool{}, map[string]bool{}
		for _, x := range next {
			if !covers(x, t) || !grantLive(x, now) || !g.usableLocked(x) {
				continue
			}
			for _, h := range x.Hosts() {
				if portEntry(h) {
					ports[h] = true
				} else {
					allow[h] = true
				}
			}
		}
		if len(allow) > MaxEgressEntries || len(ports) > MaxEgressEntries {
			return true
		}
	}
	return false
}

func (g *Grants) gatewayOn() bool {
	host, ok := g.cfg.Allow.GatewayHost()
	return !ok || host != ""
}

func (g *Grants) saveLocked(next []Grant) error {
	if err := g.cfg.Store.Save(slices.Clone(next)); err != nil {
		g.cfg.Logger.Warn("grants", "event", "not-saved")
		return conflict("state", "The engine couldn't save its grants, so nothing changed.")
	}
	g.grants = next
	return nil
}

func (g *Grants) apply(ctx context.Context) error {
	dctx, done := context.WithTimeout(context.WithoutCancel(ctx), grantApplyFor)
	g.showAll()
	g.deliver(dctx)
	done()
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), grantApplyFor)
	defer cancel()
	if err := g.cfg.Apply(actx); err != nil {
		shared.For(ctx, g.cfg.Logger).Warn("grants", "event", "egress-not-set")
		return err
	}
	return nil
}

func (g *Grants) Watch(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		g.Tend(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

type endedGrant struct {
	grant  Grant
	result string
	words  string
}

func (g *Grants) Tend(ctx context.Context) {
	now := g.cfg.Now()
	targets := map[string]GrantTarget{}
	for _, t := range g.cfg.Targets() {
		targets[t.SID] = t
	}
	g.mu.Lock()
	var ended []endedGrant
	kept := make([]Grant, 0, len(g.grants))
	for _, gr := range g.grants {
		switch {
		case !grantLive(gr, now):
			ended = append(ended, endedGrant{gr, ResultExpired, grantWords(gr, ResultExpired)})
		case gr.Scope == GrantSession && !targets[gr.Session].Live:
			ended = append(ended, endedGrant{gr, ResultGrantEnded, grantWords(gr, ResultGrantEnded)})
		case gr.Credential != "" && !g.credsBroken && !g.usableLocked(gr):
			ended = append(ended, endedGrant{gr, ResultGrantEnded, gr.what() + " for " + scopeWords(gr.Scope, gr.Project) + ", which ended when its credential was deleted"})
		default:
			kept = append(kept, gr)
		}
	}
	if len(ended) > 0 && !g.broken && g.saveLocked(kept) != nil {
		ended = nil
	}
	var gone []string
	for sid := range g.cards {
		if !targets[sid].Live {
			delete(g.cards, sid)
			gone = append(gone, sid)
		}
	}
	for sid, keys := range g.refused {
		for k, at := range keys {
			if now.Sub(at) >= AccessRefusedFor {
				delete(keys, k)
			}
		}
		if len(keys) == 0 {
			delete(g.refused, sid)
		}
	}
	for _, m := range []map[string]map[string]time.Time{g.answered} {
		for sid := range m {
			if !targets[sid].Live {
				delete(m, sid)
			}
		}
	}
	for sid := range g.autoDone {
		if !targets[sid].Live {
			delete(g.autoDone, sid)
			delete(g.notes, sid)
		}
	}
	g.mu.Unlock()
	g.tendActions(ctx, targets, now)
	for _, sid := range gone {
		g.cfg.Show(sid, AccessShown{})
	}
	for _, e := range ended {
		g.auditGrant(ctx, e.grant, ByEngine, e.result, e.words)
	}
	g.autoApply(ctx, targets)
	_ = g.apply(ctx)
}

func (g *Grants) auditGrant(ctx context.Context, gr Grant, by, result, words string) {
	if g.cfg.Audit == nil {
		return
	}
	if result == ResultExpired || result == ResultGrantEnded || result == ResultTakenBack {
		words += g.useWords(gr)
	}
	e := AuditEntry{Time: g.cfg.Now(), Kind: AuditAccess, By: by, Result: result, Reason: words, Session: gr.Session, Repository: gr.Project, Req: shared.RequestOf(ctx)}
	if gr.Scope == GrantSession {
		if t, ok := g.cfg.Session(gr.Session); ok {
			e.Branch, e.Box = t.Branch, t.Box
		}
	}
	if err := g.cfg.Audit.Append(e); err != nil {
		shared.For(ctx, g.cfg.Logger).Warn("grants", "event", "audit-not-written")
	}
}

func (g *Grants) coveringLocked(t GrantTarget, now time.Time) []Grant {
	var out []Grant
	for _, gr := range g.grants {
		if covers(gr, t) && grantLive(gr, now) && g.usableLocked(gr) {
			out = append(out, gr)
		}
	}
	slices.SortFunc(out, func(a, b Grant) int { return b.Made.Compare(a.Made) })
	return out
}

func grantFor(list []Grant, host string, git func(string) string) (Grant, bool) {
	for _, gr := range list {
		if open, _, _ := opens(gr, git); gr.Credential != "" || gr.Port > 0 || len(open) == 0 {
			continue
		}
		if gr.Host == host || EntryCovers(gr.Host, host) {
			return gr, true
		}
	}
	return Grant{}, false
}

func portGrantFor(list []Grant, target string, git func(string) string) (Grant, bool) {
	for _, gr := range list {
		if open, _, _ := opens(gr, git); gr.Credential == "" && gr.Port > 0 && gr.Target() == target && len(open) > 0 {
			return gr, true
		}
	}
	return Grant{}, false
}

func credentialGrant(list []Grant, id string) (Grant, bool) {
	for _, gr := range list {
		if gr.Credential == id {
			return gr, true
		}
	}
	return Grant{}, false
}

func (a *Allowlist) agentsCover(host string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.ContainsFunc(a.wantLocked(SideAgents).Allow, func(e string) bool { return e == host || EntryCovers(e, host) })
}

func (a *Allowlist) GitNames() []string {
	out := slices.Clone(a.cfg.Deny)
	if a.cfg.Repos != nil {
		for _, r := range a.cfg.Repos() {
			if r = HostName(r); ValidAllowEntry(r) && !strings.HasPrefix(r, ".") {
				out = append(out, r)
			} else if a, err := netip.ParseAddr(r); err == nil {
				out = append(out, a.Unmap().String())
			}
		}
	}
	return sortedHosts(out)
}

var ForgeSSHNames = []string{"ssh.github.com", "altssh.gitlab.com", "altssh.bitbucket.org"}

func (a *Allowlist) gitEntry(name string) bool {
	return a.gitCheck()(name)
}

func (a *Allowlist) gitCheck() func(string) bool {
	var repos []string
	if a.cfg.Repos != nil {
		for _, r := range a.cfg.Repos() {
			if r = HostName(r); r != "" {
				repos = append(repos, r)
			}
		}
	}
	return func(name string) bool {
		if DeniedBy(a.cfg.Deny, name) != "" || slices.Contains(ForgeSSHNames, name) {
			return true
		}
		return slices.ContainsFunc(repos, func(r string) bool { return EntryCovers(name, r) || name == r })
	}
}

func (a *Allowlist) refuseOutside(name string) error {
	if a.cfg.Outside != "" && (EntryCovers(name, a.cfg.Outside) || name == a.cfg.Outside || name == "."+a.cfg.Outside) {
		return conflict("outside host", "Covers the sandbox check's host; change SF_SANDBOX_OUTSIDE.")
	}
	return nil
}

func (g *Grants) ForgetProject(ctx context.Context, id ProjectID) {
	if id.Name == "" {
		return
	}
	g.mu.Lock()
	var gone []Grant
	if !g.broken {
		next := slices.DeleteFunc(slices.Clone(g.grants), func(x Grant) bool {
			if x.Project == id.Name {
				gone = append(gone, x)
				return true
			}
			return false
		})
		if len(gone) > 0 && g.saveLocked(next) != nil {
			gone = nil
		}
	}
	dropped := false
	if !g.presetsBroken {
		next := slices.DeleteFunc(slices.Clone(g.presets), func(p Preset) bool { return p.Project == id.Name })
		if len(next) != len(g.presets) {
			dropped = g.savePresetsLocked(next) == nil
		}
	}
	g.mu.Unlock()
	for _, x := range gone {
		g.auditGrant(ctx, x, ByEngine, ResultGrantEnded, x.what()+" for "+scopeWords(x.Scope, x.Project)+", which ended when the project was removed")
	}
	if dropped {
		g.cfg.Logger.Info("presets", "event", "project-removed")
	}
	if len(gone) > 0 {
		_ = g.apply(ctx)
	}
}
