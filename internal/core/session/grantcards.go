package session

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	AccessAllowed     = "allowed"
	AccessHeld        = "held"
	AccessRefused     = "refused"
	AccessAsks        = 20
	AccessAskWindow   = time.Hour
	AccessRefusedFor  = 10 * time.Minute
	MaxAccessCards    = 8
	MaxAccessNotes    = 8
	AccessRequest     = "request"
	AccessRefusal     = "refusal"
	AccessGiven       = "person"
	AccessAction      = "action"
	AccessNetworkNote = "What this session can reach beyond Network access."
	HandedGoneWords   = "removed when the engine next reaches the session's container"
	SignedGoneWords   = "Software Factory signs no more SSH logins with it."
	maxAnsweredHosts  = 256
)

type accessCard struct {
	id     string
	kind   string
	host   string
	port   int
	cred   string
	reason string
	asked  time.Time
	action string
	argv   []string
}

func (c *accessCard) target() string {
	if c.port > 0 {
		return PortTarget(c.host, c.port)
	}
	return c.host
}

func (c *accessCard) key() string {
	if c.action != "" {
		return actionCardKey(c.action, c.argv)
	}
	if c.cred != "" {
		return credentialKey + c.cred
	}
	return c.target()
}

type AccessCard struct {
	ID         string
	Kind       string
	Host       string
	Port       int
	Credential string
	Title      string
	Why        string
	Reason     string
	Asked      time.Time
	Risks      []AccessRisk
	Action     string
	Command    string
	Argv       []string
	Target     string
	Files      bool
	Minutes    int
	Scopes     []GrantChoice
	Expiries   []GrantChoice
}

type AccessNote struct {
	ID   string
	Text string
}

type AccessShown struct {
	Cards  []AccessCard
	Marker string
	Notes  []AccessNote
}

type GrantView struct {
	Grant
	Own         bool
	Who         string
	Until       string
	DeliveryTag string
	Copy        string
}

var deliveryTags = map[string]string{DeliveryHanded: "handed over", DeliverySigned: "key kept by Software Factory", DeliveryCopy: "short-lived copy"}

type AccessView struct {
	Grants       []GrantView
	Cards        []AccessCard
	Presets      []PresetView
	Credentials  []CredentialInfo
	Network      string
	Now          time.Time
	ActionsOn    bool
	Actions      []ActionView
	ActionGrants []ActionGrantView
	Runs         []ActionRunView
}

type AccessAsk struct {
	Host       string
	Port       int
	Credential string
	Reason     string
}

type AccessChoice struct {
	Card       string
	Host       string
	Port       int
	Credential string
	Reason     string
	Scope      string
	Expires    string
	Confirmed  bool
}

type AccessAnswer struct {
	Result string
	Reason string
}

type AccessPage interface {
	AccessView(sid string) (AccessView, error)
	CheckAccess(sid string, q AccessAsk) (AccessCard, error)
	AllowAccess(ctx context.Context, sid string, c AccessChoice) (string, error)
	RefuseAccess(ctx context.Context, sid, card string) (string, error)
	TakeBack(ctx context.Context, sid, grant string) (string, error)
	TakeBackAll(ctx context.Context, sid string) (bool, string, error)
	ApplyPreset(ctx context.Context, sid, preset string) (string, error)
	Request(ctx context.Context, sid string, q AccessAsk) AccessAnswer
	SSHIdentities(box string) []SSHIdentity
	SSHSign(ctx context.Context, box string, q SSHSignAsk) (SSHSignature, error)
	RunAction(ctx context.Context, sid, name string, args map[string]string) AccessAnswer
	ActionResult(sid, run string) AccessAnswer
	StartAction(ctx context.Context, sid, action string, args map[string]string) (string, error)
	StopAction(ctx context.Context, sid, run string) (string, error)
}

var _ AccessPage = (*Grants)(nil)

func notStartedForAccess() error {
	return conflict("not started here", "Only a session this engine started has access grants here.")
}

func (g *Grants) target(sid string) (GrantTarget, error) {
	t, ok := g.cfg.Session(sid)
	if !ok {
		return GrantTarget{}, notStartedForAccess()
	}
	return t, nil
}

func (g *Grants) hostRule(raw string) (string, error) {
	name := HostName(raw)
	if !ValidAllowEntry(name) {
		return name, onField("host", invalid("host", "Not a host name."))
	}
	if err := g.cfg.Allow.refuseOutside(name); err != nil {
		return name, err
	}
	if g.gatewayOn() && ClaudeVendorHost(name) {
		return name, conflict("gateway", "With a company gateway set, Claude Code's vendor hosts stay closed, so a grant can't open this host.")
	}
	return name, nil
}

func (g *Grants) credentialRule(name string) (Credential, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.credsBroken {
		return Credential{}, conflict("credentials", "The engine couldn't read its credentials file, so it hands over no credential. Move the file out of the engine's state folder and restart the engine.")
	}
	c, ok := g.credentialByName(name)
	if !ok {
		c, ok = g.credentialByID(name)
	}
	if !ok {
		return Credential{}, onField("credential", notFound("credential", "No credential of that name is saved."))
	}
	return c, nil
}

func cardTitle(kind string, a accessTarget) string {
	if a.cred != "" {
		ask, give := "Hand it over?", "Hand over "+a.cred+"?"
		switch a.delivery {
		case DeliverySigned:
			ask, give = "Sign its SSH logins with it?", "Sign the agent's SSH logins with "+a.cred+"?"
		case DeliveryCopy:
			ask, give = "Hand over short-lived copies?", "Hand over short-lived copies of "+a.cred+"?"
		}
		if kind == AccessRequest {
			return "The agent asks for " + a.cred + ". " + ask
		}
		return give
	}
	switch kind {
	case AccessRequest:
		return "The agent asks to reach " + a.what() + ". Allow it?"
	case AccessRefusal:
		return "The agent tried to reach " + a.what() + ". Allow it?"
	}
	return "Give access to " + a.what() + "?"
}

func cardWhy(kind, reason string) string {
	switch kind {
	case AccessRequest:
		return "The agent says (this can repeat instructions planted in what it read):"
	case AccessRefusal:
		return ""
	}
	if reason != "" {
		return "Your reason:"
	}
	return ""
}

func (g *Grants) cardTargetLocked(c *accessCard) (accessTarget, bool) {
	switch {
	case c.cred == "" && c.port > 0:
		return g.portTarget(c.host, c.port), true
	case c.cred == "":
		return g.hostTarget(c.host), true
	}
	cred, ok := g.credentialByID(c.cred)
	if !ok || g.credsBroken {
		return accessTarget{}, false
	}
	return g.credentialTarget(cred), true
}

func (g *Grants) cardOfLocked(c *accessCard, project string) (AccessCard, bool) {
	if c.kind == AccessAction {
		return g.actionCardLocked(c)
	}
	a, ok := g.cardTargetLocked(c)
	if !ok {
		return AccessCard{}, false
	}
	return AccessCard{ID: c.id, Kind: c.kind, Host: a.host, Port: a.port, Credential: a.cred, Title: cardTitle(c.kind, a), Why: cardWhy(c.kind, c.reason), Reason: c.reason, Asked: c.asked, Risks: risksFor(a, project)}, true
}

func (g *Grants) cardsLocked(sid, project string) []AccessCard {
	list := make([]AccessCard, 0, len(g.cards[sid]))
	for _, c := range g.cards[sid] {
		if v, ok := g.cardOfLocked(c, project); ok {
			list = append(list, v)
		}
	}
	return list
}

func (g *Grants) markerLocked(t GrantTarget) string {
	var parts []string
	if g.unsent[t.Box] {
		parts = append(parts, HandoverPendingNote)
	}
	parts = append(parts, g.copyNotes[t.Box]...)
	return strings.Join(parts, " ")
}

func (g *Grants) shownLocked(t GrantTarget) AccessShown {
	return AccessShown{Cards: g.cardsLocked(t.SID, t.Project), Marker: g.markerLocked(t), Notes: slices.Clone(g.notes[t.SID])}
}

func shownKey(v AccessShown) string {
	var b strings.Builder
	b.WriteString(v.Marker)
	for _, c := range v.Cards {
		b.WriteString("|" + c.ID)
		for _, r := range c.Risks {
			b.WriteString(r.Level)
		}
	}
	for _, n := range v.Notes {
		b.WriteString("#" + n.ID)
	}
	return b.String()
}

func (g *Grants) show(sid string) {
	t, ok := g.cfg.Session(sid)
	if !ok {
		return
	}
	g.showTarget(t, true)
}

func (g *Grants) showTarget(t GrantTarget, always bool) {
	g.mu.Lock()
	v := g.shownLocked(t)
	key := shownKey(v)
	same := g.shown[t.SID] == key
	g.shown[t.SID] = key
	g.mu.Unlock()
	if same && !always {
		return
	}
	g.cfg.Show(t.SID, v)
}

func (g *Grants) showAll() {
	seen := map[string]bool{}
	for _, t := range g.cfg.Targets() {
		seen[t.SID] = true
		if t.Live {
			g.showTarget(t, false)
		}
	}
	g.mu.Lock()
	for sid := range g.shown {
		if !seen[sid] {
			delete(g.shown, sid)
		}
	}
	g.mu.Unlock()
}

func (g *Grants) openLocked(sid, key string) *accessCard {
	for _, c := range g.cards[sid] {
		if c.key() == key {
			return c
		}
	}
	return nil
}

func (g *Grants) recentlyRefusedLocked(sid, key string, now time.Time) (time.Time, bool) {
	at, ok := g.refused[sid][key]
	return at, ok && now.Sub(at) < AccessRefusedFor
}

func (g *Grants) noteAnsweredLocked(sid, key string, now time.Time) {
	if g.answered[sid] == nil {
		g.answered[sid] = map[string]time.Time{}
	}
	keys := g.answered[sid]
	if _, known := keys[key]; !known && len(keys) >= maxAnsweredHosts {
		oldest, at := "", time.Time{}
		for k, t := range keys {
			if oldest == "" || t.Before(at) {
				oldest, at = k, t
			}
		}
		delete(keys, oldest)
	}
	keys[key] = now
}

func (g *Grants) openTo(t GrantTarget, host string) (string, bool) {
	if g.cfg.Allow.agentsCover(host) {
		return "Network access allows it for every session", true
	}
	now := g.cfg.Now()
	git := g.gitHosts()
	g.mu.Lock()
	defer g.mu.Unlock()
	if gr, ok := grantFor(g.coveringLocked(t, now), host, git); ok {
		return "a grant allows it for " + scopeWords(gr.Scope, gr.Project) + ", " + untilWords(gr.Expires), true
	}
	return "", false
}

func (g *Grants) portOpenTo(t GrantTarget, target string) (string, bool) {
	now := g.cfg.Now()
	git := g.gitHosts()
	g.mu.Lock()
	defer g.mu.Unlock()
	if gr, ok := portGrantFor(g.coveringLocked(t, now), target, git); ok {
		return "a grant allows it for " + scopeWords(gr.Scope, gr.Project) + ", " + untilWords(gr.Expires), true
	}
	return "", false
}

func agentPortWords(target string, err error) string {
	e, ok := errors.AsType[*Error](err)
	if !ok || e.Message == "" {
		return "a port grant can't name " + target
	}
	msg := strings.TrimSuffix(e.Message, ".")
	return "a port grant can't name " + target + " (" + lowerFirst(msg) + ")"
}

func (g *Grants) heldBy(t GrantTarget, id string) (Grant, bool) {
	now := g.cfg.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	return credentialGrant(g.coveringLocked(t, now), id)
}

func agentHostWords(host string, err error) string {
	e, ok := errors.AsType[*Error](err)
	if !ok || e.Kind == Invalid {
		return CleanLine(clip(host, 120), 120) + " isn't a host name; give one like api.example.com, or .example.com for every name under it, and for plain TCP give the host and its port apart"
	}
	msg := strings.TrimSuffix(e.Message, ".")
	if msg != "" {
		msg = strings.ToLower(msg[:1]) + msg[1:]
	}
	return "a grant can never cover " + host + " (" + msg + ")"
}

func (g *Grants) Request(ctx context.Context, sid string, q AccessAsk) AccessAnswer {
	refuse := func(why string) AccessAnswer { return AccessAnswer{Result: AccessRefused, Reason: why} }
	t, ok := g.cfg.Session(sid)
	if !ok || !t.Live {
		return refuse("only a running session this engine started can ask for access")
	}
	if g.broken {
		return refuse("the engine can't read its grants file, so it takes no request; tell the person what you need and why")
	}
	card := &accessCard{kind: AccessRequest}
	what := ""
	if q.Credential != "" {
		c, err := g.credentialRule(q.Credential)
		if err != nil {
			return refuse("no credential named " + CleanLine(clip(q.Credential, 60), 60) + " is saved, or the engine can't read its credentials; tell the person which one you need and why")
		}
		if a := g.credentialTarget(c); len(a.gits) > 0 {
			return refuse(agentGitWords(a))
		}
		card.cred, what = c.ID, c.Name
		if gr, held := g.heldBy(t, c.ID); held {
			return AccessAnswer{Result: AccessAllowed, Reason: c.Name + " is already given to this session, " + untilWords(gr.Expires) + ": " + c.Where()}
		}
	} else if q.Port != 0 {
		named := CleanLine(clip(q.Host, 120), 120) + ":" + strconv.Itoa(q.Port)
		host, err := g.portRule(q.Host, q.Port)
		if err != nil {
			return refuse(agentPortWords(named, err))
		}
		if a := g.portTarget(host, q.Port); len(a.gits) > 0 {
			return refuse(agentGitWords(a))
		}
		card.host, card.port = host, q.Port
		what = card.target()
		if how, open := g.portOpenTo(t, what); open {
			return AccessAnswer{Result: AccessAllowed, Reason: what + " is already open to this session: " + how + "; " + PortReachWords(host, q.Port)}
		}
	} else {
		host, err := g.hostRule(q.Host)
		if err != nil {
			return refuse(agentHostWords(host, err))
		}
		if a := g.hostTarget(host); len(a.gits) > 0 {
			return refuse(agentGitWords(a))
		}
		card.host, what = host, host
		if how, open := g.openTo(t, host); open {
			return AccessAnswer{Result: AccessAllowed, Reason: host + " is already open to this session: " + how + "; reach it over HTTPS through the proxy"}
		}
	}
	why, err := CleanHostReason(q.Reason)
	if err != nil || why == "" {
		return refuse("give a reason: one line of plain text, at most 200 characters, with nothing that looks like a token or a key")
	}
	card.reason = why
	now := g.cfg.Now()
	key := card.key()
	g.mu.Lock()
	if at, recent := g.recentlyRefusedLocked(sid, key, now); recent {
		g.mu.Unlock()
		return refuse("the person didn't allow " + what + " less than 10 minutes ago, so don't ask for it again before " + at.Add(AccessRefusedFor).UTC().Format(time.RFC3339) + "; tell them why you need it instead")
	}
	for _, c := range g.cards[sid] {
		if c.kind != AccessRequest {
			continue
		}
		g.mu.Unlock()
		if c.key() == key {
			return AccessAnswer{Result: AccessHeld, Reason: "your request for " + what + " is still waiting for the person's answer on Software Factory's page; the answer reaches you with your next message"}
		}
		return refuse("this session already has a request waiting for the person; wait for its answer, which reaches you with your next message")
	}
	if !g.asks.allow(sid, now) {
		g.mu.Unlock()
		return refuse("this session asked for access " + strconv.Itoa(AccessAsks) + " times in the last hour, the most Software Factory takes; tell the person what you need and why")
	}
	list := slices.DeleteFunc(g.cards[sid], func(c *accessCard) bool { return c.key() == key })
	if len(list) >= MaxAccessCards {
		list = list[1:]
	}
	card.id, card.asked = newGrantID(), now
	g.cards[sid] = append(list, card)
	g.mu.Unlock()
	g.show(sid)
	g.cfg.Logger.Info("grants", "event", "asked")
	return AccessAnswer{Result: AccessHeld, Reason: "Software Factory's page shows your request for " + what + " to the person, who decides whether to allow it, for how long and for which sessions; the answer reaches you with your next message. Go on with other work, or end your turn if you can't go on without it"}
}

func (g *Grants) Tried(box, raw string, last time.Time) {
	sid, ok := g.cfg.InBox(box)
	if !ok {
		return
	}
	t, ok := g.cfg.Session(sid)
	if !ok || !t.Live || g.broken {
		return
	}
	host, err := g.hostRule(raw)
	if err != nil || host != raw || ClaudeVendorHost(host) || g.cfg.Allow.gitEntry(host) {
		return
	}
	if _, open := g.openTo(t, host); open {
		return
	}
	now := g.cfg.Now()
	g.mu.Lock()
	_, recent := g.recentlyRefusedLocked(sid, host, now)
	answered, was := g.answered[sid][host]
	stale := was && !last.After(answered.Truncate(time.Second))
	if recent || stale || g.openLocked(sid, host) != nil || len(g.cards[sid]) >= MaxAccessCards {
		g.mu.Unlock()
		return
	}
	g.cards[sid] = append(g.cards[sid], &accessCard{id: newGrantID(), kind: AccessRefusal, host: host, asked: now})
	g.mu.Unlock()
	g.show(sid)
}

func (g *Grants) AccessView(sid string) (AccessView, error) {
	t, err := g.target(sid)
	if err != nil {
		return AccessView{}, err
	}
	now := g.cfg.Now()
	g.mu.Lock()
	covering := g.coveringLocked(t, now)
	cards := g.cardsLocked(sid, t.Project)
	broken := g.broken
	v := AccessView{Cards: cards, Network: AccessNetworkNote, Now: now, Presets: g.presetViewsLocked(t.Project, true), ActionsOn: g.cfg.Actions.Store != nil, Actions: g.actionViewsLocked()}
	v.ActionGrants, v.Runs = g.actionViewFor(t, now)
	if !g.credsBroken {
		for _, c := range g.creds {
			v.Credentials = append(v.Credentials, c.Info())
		}
	}
	type deliveryOf struct {
		tag, copy string
	}
	shown := map[string]deliveryOf{}
	for _, gr := range covering {
		c, ok := g.credentialByID(gr.Credential)
		if gr.Credential == "" || !ok {
			continue
		}
		d := deliveryOf{tag: deliveryTags[c.Delivery()]}
		if c.Delivery() == DeliveryCopy {
			if until := g.copyUntilLocked(c.ID, []string{t.Box}, now); !until.IsZero() {
				d.copy = CopyKeptWords(until)
			} else if note := g.copyNoteLocked(t.Box, c); note != "" {
				d.copy = note
			}
		}
		shown[gr.ID] = d
	}
	g.mu.Unlock()
	for _, gr := range covering {
		who := "Given by you"
		switch {
		case gr.AskedBy == AskedByAgent:
			who = "Asked for by the agent"
		case gr.AskedBy == AskedByPreset:
			who = "From the preset " + gr.Preset + ", when the session started"
		case gr.Preset != "":
			who = "From the preset " + gr.Preset
		}
		v.Grants = append(v.Grants, GrantView{Grant: gr, Own: gr.Scope == GrantSession, Who: who, Until: untilWords(gr.Expires), DeliveryTag: shown[gr.ID].tag, Copy: shown[gr.ID].copy})
	}
	if broken {
		return v, conflict("state", "The engine couldn't read grants.json, so it makes and takes back no grant. Move the file out of the engine's state folder and restart the engine.")
	}
	return v, nil
}

func (g *Grants) CheckAccess(sid string, q AccessAsk) (AccessCard, error) {
	t, err := g.target(sid)
	if err != nil {
		return AccessCard{}, err
	}
	why, err := CleanHostReason(q.Reason)
	if err != nil {
		return AccessCard{}, err
	}
	card := &accessCard{kind: AccessGiven, reason: why, asked: g.cfg.Now()}
	if q.Credential != "" {
		c, err := g.credentialRule(q.Credential)
		if err != nil {
			return AccessCard{}, err
		}
		if err := gitRefusal(g.credentialTarget(c)); err != nil {
			return AccessCard{}, err
		}
		if _, held := g.heldBy(t, c.ID); held {
			return AccessCard{}, onField("credential", conflict("open", "Already given to this session."))
		}
		card.cred = c.ID
	} else if q.Port != 0 {
		host, err := g.portRule(q.Host, q.Port)
		if err != nil {
			return AccessCard{}, err
		}
		if err := gitRefusal(g.portTarget(host, q.Port)); err != nil {
			return AccessCard{}, err
		}
		card.host, card.port = host, q.Port
		if _, open := g.portOpenTo(t, card.target()); open {
			return AccessCard{}, onField("host", conflict("open", "Already open to this session."))
		}
	} else {
		host, err := g.hostRule(q.Host)
		if err != nil {
			return AccessCard{}, onField("host", err)
		}
		if err := gitRefusal(g.hostTarget(host)); err != nil {
			return AccessCard{}, err
		}
		if _, open := g.openTo(t, host); open {
			return AccessCard{}, onField("host", conflict("open", "Already open to this session."))
		}
		card.host = host
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	v, ok := g.cardOfLocked(card, t.Project)
	if !ok {
		return AccessCard{}, onField("credential", notFound("credential", "No credential of that name is saved."))
	}
	return v, nil
}

type grantMaking struct {
	host    string
	port    int
	cred    string
	scope   string
	expires string
	reason  string
	askedBy string
	preset  string
	card    string
}

func (g *Grants) clashLocked(c Credential, project string, now time.Time) error {
	mine := c.places()
	for _, gr := range g.grants {
		if gr.Credential == "" || gr.Credential == c.ID || gr.Project != project || !grantLive(gr, now) {
			continue
		}
		other, ok := g.credentialByID(gr.Credential)
		if !ok {
			continue
		}
		for _, p := range other.places() {
			if slices.Contains(mine, p) {
				return conflict("clash", c.Name+" would use the "+p+", which "+other.Name+" already uses in this project's sessions. Take "+other.Name+" back first.")
			}
		}
	}
	return nil
}

func (g *Grants) makeGrant(ctx context.Context, t GrantTarget, m grantMaking) (Grant, error) {
	if g.broken {
		return Grant{}, conflict("state", "The engine couldn't read grants.json, so it makes no grant.")
	}
	if !grantChoice(GrantScopes, m.scope) {
		return Grant{}, onField("scope", invalid("scope", "Choose this session or this project."))
	}
	if !grantChoice(GrantExpiries, m.expires) {
		return Grant{}, onField("expires", invalid("expires", "Choose 1 hour, 1 day, 1 week or until taken back."))
	}
	if m.scope == GrantSession && !t.Live {
		return Grant{}, conflict("stopped", "This session isn't running, and a session's grants end when it stops. Wake it first, or choose This project.")
	}
	now := g.cfg.Now()
	gr := Grant{ID: newGrantID(), Scope: m.scope, Session: t.SID, Project: t.Project, Reason: m.reason, AskedBy: m.askedBy, Preset: m.preset, Made: now.Truncate(time.Second)}
	if ttl := expiryChoices[m.expires]; ttl > 0 {
		gr.Expires = now.Add(ttl).Truncate(time.Second)
	}
	var cred Credential
	if m.cred != "" {
		c, err := g.credentialRule(m.cred)
		if err != nil {
			return Grant{}, err
		}
		cred = c
		gr.Credential, gr.Name, gr.Bound, gr.Delivery = c.ID, c.Name, slices.Clone(c.Bound), c.Delivery()
	} else if m.port != 0 {
		host, err := g.portRule(m.host, m.port)
		if err != nil {
			return Grant{}, err
		}
		gr.Host, gr.Port = host, m.port
	} else {
		host, err := g.hostRule(m.host)
		if err != nil {
			return Grant{}, onField("host", err)
		}
		gr.Host = host
	}
	if gits := g.gitsOf(gr.Hosts()); len(gits) > 0 {
		return Grant{}, gitRefusal(accessTarget{cred: gr.Name, gits: gits})
	}
	key := gr.key()
	var targets []GrantTarget
	if gr.Scope == GrantProject {
		targets = g.cfg.Targets()
	}
	g.mu.Lock()
	if m.card != "" && !slices.ContainsFunc(g.cards[t.SID], func(c *accessCard) bool { return c.id == m.card }) {
		g.mu.Unlock()
		return Grant{}, notFound("card", "That card isn't waiting any more. Reload the page.")
	}
	if cred.ID != "" {
		if err := g.clashLocked(cred, t.Project, now); err != nil {
			g.mu.Unlock()
			return Grant{}, err
		}
	}
	next := slices.DeleteFunc(slices.Clone(g.grants), func(x Grant) bool {
		return x.key() == key && x.Scope == gr.Scope && (gr.Scope == GrantProject && x.Project == gr.Project || gr.Scope == GrantSession && x.Session == t.SID)
	})
	if len(next) >= MaxGrants {
		g.mu.Unlock()
		return Grant{}, limited("grants", "Software Factory keeps at most "+strconv.Itoa(MaxGrants)+" grants. Take one back first.")
	}
	if g.overLimitLocked(append(slices.Clone(next), gr), gr, targets, now) {
		g.mu.Unlock()
		return Grant{}, limited("grants", "That would give one session's container more than "+strconv.Itoa(MaxEgressEntries)+" hosts, or more than "+strconv.Itoa(MaxEgressEntries)+" host ports, through grants, the most egress takes for one session. Take back a grant first.")
	}
	if err := g.saveLocked(append(next, gr)); err != nil {
		g.mu.Unlock()
		return Grant{}, err
	}
	g.cards[t.SID] = slices.DeleteFunc(g.cards[t.SID], func(c *accessCard) bool {
		return c.id == m.card || c.key() == key && (c.kind == AccessRefusal || m.askedBy == AskedByPreset)
	})
	if len(g.cards[t.SID]) == 0 {
		delete(g.cards, t.SID)
	}
	delete(g.refused[t.SID], key)
	g.noteAnsweredLocked(t.SID, key, now)
	g.mu.Unlock()
	g.auditGrant(ctx, gr, ByPage, ResultGranted, grantWords(gr, ResultGranted))
	if cred.ID != "" {
		g.cfg.Note(t.SID, madeNote(cred, gr))
	}
	return gr, nil
}

func madeNote(c Credential, gr Grant) string {
	lasts := " for " + scopeWords(gr.Scope, gr.Project) + ", " + untilWords(gr.Expires) + ": "
	switch c.Delivery() {
	case DeliverySigned:
		return "Software Factory signs SSH logins with the credential " + c.Name + lasts + c.Where() + ". You can't read or copy the key; only " + hostList(c.Bound) + " open for it."
	case DeliveryCopy:
		return "Software Factory hands over the credential " + c.Name + lasts + c.Where() + ". Your shell commands get each copy from the next one, and only " + hostList(c.Bound) + " open for it. Don't copy it elsewhere."
	}
	return "Software Factory handed over the credential " + c.Name + lasts + c.Where() + ". Your shell commands get it from the next one, and only " + hostList(c.Bound) + " open for it. Don't copy it elsewhere."
}

func (g *Grants) AllowAccess(ctx context.Context, sid string, q AccessChoice) (string, error) {
	t, err := g.target(sid)
	if err != nil {
		return "", err
	}
	m := grantMaking{host: q.Host, port: q.Port, cred: q.Credential, scope: q.Scope, expires: q.Expires, askedBy: AskedByPerson, card: q.Card}
	kind := AccessGiven
	if q.Card != "" {
		g.mu.Lock()
		var found *accessCard
		at := -1
		for i, c := range g.cards[sid] {
			if c.id == q.Card {
				found, at = c, i
			}
		}
		if found != nil && found.kind == AccessAction {
			g.takeCardLocked(sid, at)
		}
		g.mu.Unlock()
		if found == nil {
			return "", notFound("card", "That card isn't waiting any more. Reload the page.")
		}
		if found.kind == AccessAction {
			said, err := g.allowActionCard(ctx, t, found, q)
			if err != nil {
				g.putBackCard(sid, at, found)
			}
			return said, err
		}
		kind, m.host, m.port, m.cred, m.reason = found.kind, found.host, found.port, found.cred, found.reason
		if kind != AccessGiven {
			m.askedBy = AskedByAgent
		}
	} else if m.reason, err = CleanHostReason(q.Reason); err != nil {
		return "", err
	}
	a, err := g.targetOf(m)
	if err != nil {
		return "", err
	}
	if grantChoice(GrantScopes, q.Scope) && grantChoice(GrantExpiries, q.Expires) {
		if risk := riskFor(a, q.Scope, q.Expires, t.Project); risk.Accept != "" && !q.Confirmed {
			return "", onField("confirmed", invalid("confirm", "Tick the box to accept the risk first."))
		}
	}
	gr, err := g.makeGrant(ctx, t, m)
	if err != nil {
		return "", err
	}
	switch {
	case kind == AccessRequest && gr.Credential == "" && gr.Port > 0:
		g.cfg.Note(sid, "The person allowed "+gr.Target()+" for "+scopeWords(gr.Scope, gr.Project)+", "+untilWords(gr.Expires)+", on Software Factory's page, so you can reach it now: "+PortReachWords(gr.Host, gr.Port)+".")
	case kind == AccessRequest && gr.Credential == "":
		g.cfg.Note(sid, "The person allowed "+gr.Host+" for "+scopeWords(gr.Scope, gr.Project)+", "+untilWords(gr.Expires)+", on Software Factory's page, so you can reach it over HTTPS through the proxy now.")
	}
	said := "Allowed " + gr.what() + " for " + scopeWords(gr.Scope, gr.Project) + ", " + grantForWords[q.Expires] + "."
	switch {
	case gr.Delivery == DeliverySigned:
		said = "Software Factory signs SSH logins with " + gr.Name + " for " + scopeWords(gr.Scope, gr.Project) + ", " + grantForWords[q.Expires] + "."
	case gr.Delivery == DeliveryCopy:
		said = "Handing over short-lived copies of " + gr.Name + " to " + scopeWords(gr.Scope, gr.Project) + ", " + grantForWords[q.Expires] + "."
	case gr.Credential != "":
		said = "Handed over " + gr.Name + " to " + scopeWords(gr.Scope, gr.Project) + ", " + grantForWords[q.Expires] + "."
	}
	if g.apply(ctx) != nil {
		return said, conflict("egress", "Saved, but egress didn't take it yet, so the agent can't reach "+hostList(gr.Hosts())+" until it does. The engine sets it again within 30 seconds.")
	}
	return said, nil
}

func (g *Grants) takeCardLocked(sid string, at int) {
	g.cards[sid] = slices.Delete(g.cards[sid], at, at+1)
	if len(g.cards[sid]) == 0 {
		delete(g.cards, sid)
	}
}

func (g *Grants) putBackCard(sid string, at int, c *accessCard) {
	g.mu.Lock()
	list := g.cards[sid]
	g.cards[sid] = slices.Insert(list, min(at, len(list)), c)
	g.mu.Unlock()
	g.show(sid)
}

func (g *Grants) targetOf(m grantMaking) (accessTarget, error) {
	a, err := g.ruledTarget(m)
	if err != nil {
		return a, err
	}
	return a, gitRefusal(a)
}

func (g *Grants) ruledTarget(m grantMaking) (accessTarget, error) {
	if m.cred != "" {
		c, err := g.credentialRule(m.cred)
		if err != nil {
			return accessTarget{}, err
		}
		return g.credentialTarget(c), nil
	}
	if m.port != 0 {
		host, err := g.portRule(m.host, m.port)
		if err != nil {
			return accessTarget{}, err
		}
		return g.portTarget(host, m.port), nil
	}
	host, err := g.hostRule(m.host)
	if err != nil {
		return accessTarget{}, onField("host", err)
	}
	return g.hostTarget(host), nil
}

func (g *Grants) RefuseAccess(ctx context.Context, sid, id string) (string, error) {
	t, err := g.target(sid)
	if err != nil {
		return "", err
	}
	g.mu.Lock()
	var c *accessCard
	for i, x := range g.cards[sid] {
		if x.id == id {
			c = x
			g.cards[sid] = slices.Delete(g.cards[sid], i, i+1)
			break
		}
	}
	if c == nil {
		g.mu.Unlock()
		return "", notFound("card", "That card isn't waiting any more. Reload the page.")
	}
	if len(g.cards[sid]) == 0 {
		delete(g.cards, sid)
	}
	if c.kind == AccessAction {
		now := g.cfg.Now()
		if g.refused[sid] == nil {
			g.refused[sid] = map[string]time.Time{}
		}
		g.refused[sid][c.key()] = now
		g.noteAnsweredLocked(sid, c.key(), now)
		g.mu.Unlock()
		g.show(sid)
		return g.refuseActionCard(ctx, t, c), nil
	}
	what := c.target()
	if cred, ok := g.credentialByID(c.cred); ok && c.cred != "" {
		what = cred.Name
	}
	now := g.cfg.Now()
	if g.refused[sid] == nil {
		g.refused[sid] = map[string]time.Time{}
	}
	g.refused[sid][c.key()] = now
	g.noteAnsweredLocked(sid, c.key(), now)
	g.mu.Unlock()
	g.show(sid)
	gr := Grant{Host: c.host, Port: c.port, Scope: GrantSession, Session: sid, Project: t.Project}
	g.auditGrant(ctx, gr, ByPage, ResultNotGranted, refusedWords(what, c.kind == AccessRequest))
	if c.kind == AccessRequest {
		g.cfg.Note(sid, "The person didn't allow "+what+" on Software Factory's page. Don't ask for it again for 10 minutes; if you still need it, say why in your reply.")
	}
	return "Didn't allow " + what + ". The agent can't ask for it again for 10 minutes.", nil
}

func (g *Grants) takeBack(ctx context.Context, sid string, pick func(Grant) bool) ([]Grant, error) {
	t, err := g.target(sid)
	if err != nil {
		return nil, err
	}
	if g.broken {
		return nil, conflict("state", "The engine couldn't read grants.json, so it takes back no grant.")
	}
	now := g.cfg.Now()
	g.mu.Lock()
	var taken []Grant
	next := slices.DeleteFunc(slices.Clone(g.grants), func(x Grant) bool {
		if covers(x, t) && grantLive(x, now) && pick(x) {
			taken = append(taken, x)
			return true
		}
		return false
	})
	if len(taken) == 0 {
		g.mu.Unlock()
		return nil, nil
	}
	if err := g.saveLocked(next); err != nil {
		g.mu.Unlock()
		return nil, err
	}
	for _, x := range taken {
		g.noteAnsweredLocked(sid, x.key(), now)
	}
	g.mu.Unlock()
	for _, x := range taken {
		g.auditGrant(ctx, x, ByPage, ResultTakenBack, x.what()+" for "+scopeWords(x.Scope, x.Project))
	}
	return taken, nil
}

func (g *Grants) takenBackWords(taken []Grant) string {
	targets := g.cfg.Targets()
	now := g.cfg.Now()
	var parts []string
	add := func(w string) {
		if !slices.Contains(parts, w) {
			parts = append(parts, w)
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, x := range taken {
		if x.Credential == "" {
			continue
		}
		delivery := x.Delivery
		if c, ok := g.credentialByID(x.Credential); ok {
			delivery = c.Delivery()
		}
		switch delivery {
		case DeliverySigned:
			add(SignedGoneWords)
		case DeliveryCopy:
			w := "The short-lived copy is " + HandedGoneWords + "."
			if until := g.copyUntilLocked(x.Credential, boxesFor(x, targets), now); !until.IsZero() {
				w += " " + CopyKeptWords(until)
			}
			add(w)
		default:
			add("The handed-over files and variables are " + HandedGoneWords + "; copies the agent made stay valid, so rotate the secret.")
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " " + strings.Join(parts, " ")
}

func (g *Grants) TakeBack(ctx context.Context, sid, id string) (string, error) {
	taken, err := g.takeBack(ctx, sid, func(x Grant) bool { return x.ID == id })
	if err != nil {
		return "", err
	}
	if len(taken) == 0 {
		acts, stopped := g.takeBackActions(ctx, sid, func(x actionGrant) bool { return x.ID == id })
		if len(acts) == 0 {
			return "", notFound("grant", "That grant isn't there any more. Reload the page.")
		}
		said := "Took back running " + acts[0].Name + " with " + QuotedCommand(acts[0].Argv) + " without a card."
		if stopped > 0 {
			said += " The run it allowed that was still going is stopped."
		}
		return said, nil
	}
	said := "Took back " + taken[0].what() + " for " + scopeWords(taken[0].Scope, taken[0].Project) + "." + g.takenBackWords(taken)
	if g.apply(ctx) != nil {
		return said, conflict("egress", "Taken back, but egress didn't take the new list yet. The engine sets it again within 30 seconds, and the sandbox check holds sessions until it does.")
	}
	return said, nil
}

func (g *Grants) TakeBackAll(ctx context.Context, sid string) (bool, string, error) {
	taken, err := g.takeBack(ctx, sid, func(x Grant) bool { return x.Scope == GrantSession })
	if err != nil {
		return false, "", err
	}
	acts, stopped := g.takeBackActions(ctx, sid, func(actionGrant) bool { return true })
	if len(taken)+len(acts) == 0 {
		return false, "No change: this session holds no grants of its own.", nil
	}
	said := "Took back this session's " + strconv.Itoa(len(taken)+len(acts)) + " grants."
	if len(taken)+len(acts) == 1 {
		said = "Took back this session's grant."
	}
	said += g.takenBackWords(taken)
	if stopped > 0 {
		said += " Action runs they allowed that were still going are stopped."
	}
	if len(taken) == 0 {
		return true, said, nil
	}
	if g.apply(ctx) != nil {
		return true, said, conflict("egress", "Taken back, but egress didn't take the new list yet. The engine sets it again within 30 seconds, and the sandbox check holds sessions until it does.")
	}
	return true, said, nil
}
