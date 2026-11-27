package session

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/level"
	"github.com/bezmoradi/software-factory/internal/core/shared"
	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

const (
	StartGap     = 3 * time.Second
	WakeGap      = 5 * time.Second
	OfferLife    = 10 * time.Minute
	claudeRun    = 30 * time.Second
	claudeList   = 20 * time.Second
	openFor      = 45 * time.Second
	stopFor      = 10 * time.Second
	stopSettle   = 10 * time.Second
	maxName      = 80
	maxOutput    = 2000
	findAttempts = 5
	launchMode   = "default"
	PushServer   = "software-factory"
	NotRunReason = "this engine has no sandbox check configured, so this session started without one"
)

var (
	ClaudeModels  = []string{"", "fable", "opus", "sonnet", "haiku"}
	backgrounded  = regexp.MustCompile(`backgrounded · ([0-9a-f]{8})\b`)
	shortID       = regexp.MustCompile(`^[0-9a-f]{8}$`)
	discardOffer  = regexp.MustCompile("--discard-unpushed[\\s=]+['\"`]?([0-9a-f]{7,64}@[A-Za-z0-9._-]{1,100})")
	forceOffer    = regexp.MustCompile("--force-remove-worktree[\\s=]+['\"`]?([A-Za-z0-9._-]{1,100})")
	ansi          = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)
	errNoSessions = errors.New("claude agents --json answered no list")
)

type Dispatch struct {
	Path    string
	Agent   string
	Name    string
	Model   string
	Type    string
	Consent Consent

	From      StartFrom
	Issue     int
	IssueHash string
	Effort    string
	Box       string

	group     string
	groupKind string
	asked     string
}

type Started struct {
	SID string
	ID  string
}

type Offer struct {
	Flag  string
	Value string
}

type Starter interface {
	Agents() []AgentView
	Projects() ([]Repository, error)
	Dispatch(ctx context.Context, d Dispatch) (Started, error)
	Stop(ctx context.Context, sid string) error
	Wake(ctx context.Context, sid string, consent Consent) error
	RefuseWake(ctx context.Context, sid string) error
	Remove(ctx context.Context, sid string, offer *Offer) (*Offer, error)
	Expect(agent string, x Expect) error
	Expectations() map[string]Expect
	SessionName() string
}

type DispatcherConfig struct {
	Agents       []AgentSpec
	Boxes        Boxes
	BoxStore     BoxStore
	Routes       BoxRoutes
	Folders      BoxFolders
	Clones       SessionClones
	CloneReader  CloneReader
	Stdio        Opener
	Repositories Repositories
	PluginDir    string
	PluginSocket string
	MCPCommand   string
	Version      string
	Sandbox      Sandbox
	Installs     Installs
	Reviews      *Reviews
	Startup      StartupReader
	Approvals    Approvals
	Expected     *Expected
	Work         *Work
	Packs        SessionPacks
	Workflow     func() (dir, commit string, err error)
	Refused      func(workflowCommit string) bool
	Workspace    string
	Runs         RunStore
	Versions     func(agent string) string
	Note         func(sid, text string)
	Audit        Audit
	Identity     *Identities
	KeyRoutes    KeyRoutes
	DevChecks    bool
	Logger       shared.Logger
}

type run struct {
	spec       AgentSpec
	repo       string
	repoPath   string
	base       string
	name       string
	branch     string
	cwd        string
	box        string
	clone      string
	sid        string
	docs       string
	id         string
	display    string
	running    bool
	busy       bool
	stopping   bool
	process    Process
	bridge     *bridge
	rmDone     bool
	offer      *Offer
	offerAt    time.Time
	claudeArgv []string
	check      CheckState
	signIn     SignIn
	typ        string
	at         time.Time
	stamp      runStamp

	origin string
	from   startedFrom
	model  string
	effort string
	ports  int

	group     string
	groupKind string
	history   bool
	doubled   bool
	asked     string
	waitAt    time.Time
	aside     string
	homeAsk   string
	homeClear bool
	homeGated bool
	pack      SessionPack

	pidPendingSince time.Time
}

type Dispatcher struct {
	cfg        DispatcherConfig
	skips      doubledSkips
	checks     checks
	reg        *Registry
	mu         sync.Mutex
	runs       map[string]*run
	started    time.Time
	woken      time.Time
	starting   bool
	startRepo  string
	approvalMu sync.Mutex
	saveMu     sync.Mutex
	home       *HomeSettings
	keyMu      keyLock
	gateways   GatewayRoutes

	forges *Forges
	work   *SessionWork

	decisions  *DecisionLog
	branchCopy *Keep
	boxes      boxBook
	waiting    map[string]*run
	boxRuns    map[string]*sync.Mutex
	syncMu     sync.Mutex
	runsUnread bool
	removed    []func(sid string)

	branchState func(WorkspaceSession) (string, string, string)
	trustMoved  trustChanges
}

var (
	_ Starter       = (*Dispatcher)(nil)
	_ ReviewStarter = (*Dispatcher)(nil)
)

func NewDispatcher(reg *Registry, cfg DispatcherConfig) *Dispatcher {
	if cfg.Logger == nil {
		cfg.Logger = quiet{}
	}
	cfg.Audit = masking(cfg.Audit)
	d := &Dispatcher{cfg: cfg, reg: reg, runs: map[string]*run{}, waiting: map[string]*run{}, boxRuns: map[string]*sync.Mutex{}}
	if cfg.Runs != nil {
		reg.OnTurn(d.saveRuns)
	}
	return d
}

func notRun() level.Observation {
	return level.Observation{By: level.Engine, Event: level.CheckNotRun, Parts: []string{NotRunReason}}
}

func (d *Dispatcher) Agents() []AgentView {
	out := make([]AgentView, 0, len(d.cfg.Agents))
	for _, a := range d.cfg.Agents {
		out = append(out, viewOf(d.installed(a)))
	}
	return out
}

func (d *Dispatcher) installed(a AgentSpec) AgentSpec {
	if !a.Enabled || d.cfg.Installs == nil {
		return a
	}
	if ok, note := d.cfg.Installs.Ready(a.ID); !ok {
		a.Enabled = false
		a.Off = note
		a.install = true
	}
	return a
}

func (d *Dispatcher) Projects() ([]Repository, error) {
	if d.cfg.Repositories == nil {
		return nil, nil
	}
	return d.cfg.Repositories.List()
}

func hostMessage(err error) string {
	if h, ok := errors.AsType[*HostError](err); ok {
		if h.Message != "" {
			return clip(h.Message, maxOutput)
		}
		return "agent-host refused the request (" + clip(h.Code, 40) + ")"
	}
	return "agent-host didn't answer: " + clip(err.Error(), 300)
}

func output(r RunResult) string {
	return strings.TrimSpace(clip(ansi.ReplaceAllString(r.Stdout+"\n"+r.Stderr, ""), maxOutput))
}

func firstLine(s string) string {
	for l := range strings.SplitSeq(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return clip(l, 300)
		}
	}
	return ""
}

func (d *Dispatcher) Dispatch(ctx context.Context, q Dispatch) (Started, error) {
	started, err := d.dispatch(ctx, q)
	return started, onStartField(err)
}

func (d *Dispatcher) dispatch(ctx context.Context, q Dispatch) (Started, error) {
	spec, ok := findAgent(d.cfg.Agents, q.Agent)
	if !ok {
		return Started{}, invalid("agent", "Software Factory doesn't know that agent.")
	}
	if !spec.Enabled {
		return Started{}, conflict("agent off", spec.Off)
	}
	if spec = d.installed(spec); !spec.Enabled {
		return Started{}, conflict("not installed", spec.Off)
	}
	if err := checkChoice(spec, q.Model, q.Effort); err != nil {
		return Started{}, err
	}
	if utf16Len(q.Name) > maxName || controlChars.MatchString(q.Name) || !utf8Valid(q.Name) {
		return Started{}, invalid("name", "At most 80 characters.")
	}
	if !d.boxed() || d.cfg.Repositories == nil {
		return Started{}, conflict("no agent-host", "This engine has no box helper to start sessions' containers in.")
	}
	if err := d.runsReadable(); err != nil {
		return Started{}, err
	}
	if err := checkStartFrom(q); err != nil {
		return Started{}, err
	}
	repos, err := d.cfg.Repositories.List()
	if err != nil {
		return Started{}, conflict("workspace", "The engine couldn't read the workspace's repositories.")
	}
	i := slices.IndexFunc(repos, func(r Repository) bool { return r.Path == q.Path })
	if i < 0 {
		return Started{}, notFound("project", "That project is no longer in the list.")
	}
	repo := repos[i]
	if d.cfg.Work != nil {
		if err := d.cfg.Work.Check(repo.Name, q.Type); err != nil {
			return Started{}, err
		}
	}
	id := d.cfg.Identity.Current()
	if !id.Set() {
		return Started{}, noIdentity()
	}
	if err := d.claimStart(repo.Name); err != nil {
		return Started{}, err
	}
	defer d.endStart()
	if q.Box == "" {
		q.Box = d.askedBox(q)
	}
	if q.Box != "" {
		return d.resume(ctx, q, spec, repo)
	}
	from, err := d.startSource(repo, q)
	if err != nil {
		return Started{}, err
	}
	box, err := d.openBox(ctx, repo.Name, spec.ID)
	if err != nil {
		return Started{}, err
	}
	kept := false
	defer func() {
		if !kept {
			d.dropBox(box, true)
		}
	}()
	check, err := d.startCheck(ctx, box)
	if err != nil {
		return Started{}, err
	}
	main, err := d.cfg.Repositories.Fresh(ctx, repo.Name)
	if err != nil {
		if e, ok := errors.AsType[*Error](err); ok {
			return Started{}, e
		}
		return Started{}, conflict("fetch", "The engine couldn't fetch the project's main branch, so no session started.")
	}
	at, err := d.startAt(ctx, repo, q, main)
	if err != nil {
		return Started{}, err
	}
	base := at.commit
	short := randomHex(4)
	display := strings.TrimSpace(q.Name)
	if display == "" {
		display = d.SessionName()
	}
	r := &run{spec: spec, repo: repo.Name, repoPath: repo.Path, base: base, name: "sf-" + short, branch: "sf/" + short, id: short, display: display, check: check, typ: q.Type, at: d.reg.now()}
	r.box, r.clone = box, BoxClone(d.cfg.Workspace, box, repo.Name)
	r.origin = at.origin
	r.from = startFromOf(q)
	r.group, r.groupKind, r.asked = q.group, q.groupKind, q.asked
	if err := d.branchFree(r.branch); err != nil {
		return Started{}, err
	}
	r.model, r.effort = q.Model, q.Effort
	r.stamp = d.stampOf(spec.ID, time.Time{})
	if err := d.makeClone(ctx, r, main, from); err != nil {
		return Started{}, err
	}
	host := d.host(r)
	cwd, err := host.AddWorktree(ctx, repo.Name, r.name, r.branch, base, id)
	if err != nil {
		shared.For(ctx, d.cfg.Logger).Warn("dispatch", "event", "worktree-failed")
		return Started{}, conflict("worktree", "agent-host couldn't create the session's worktree: "+hostMessage(err))
	}
	if err := d.checkWorktree(r, cwd); err != nil {
		return Started{}, err
	}
	r.cwd = cwd
	return d.launch(ctx, r, q, &kept)
}

func (d *Dispatcher) launch(ctx context.Context, r *run, q Dispatch, kept *bool) (Started, error) {
	if err := d.admit(ctx, r, q.Consent); err != nil {
		d.mu.Lock()
		d.started = time.Time{}
		d.mu.Unlock()
		*kept = d.holdForAnswer(r, q, err)
		return Started{}, err
	}
	d.homeBaseline(ctx, r)
	d.reservePorts(r)
	var err error
	if r.spec.Method == MethodPlugin {
		err = d.startClaude(ctx, r, r.model)
	} else {
		err = d.startStdio(ctx, r)
	}
	if r.sid != "" {
		*kept = true
		d.unwait(r.box)
		d.settleBox(r.box)
		if err != nil {
			go d.idleBox(r)
		}
	} else if err != nil {
		*kept = d.holdForAnswer(r, q, err)
	}
	if d.cfg.Work != nil && r.sid != "" && d.hasRun(r.sid) {
		d.cfg.Work.Begin(r.sid, r.repo, r.typ)
	}
	if r.sid != "" && err == nil {
		d.homeServers(r)
	}
	d.startedWork(r, err)
	d.offerDecisions(r, err)
	if err != nil {
		return Started{SID: r.sid, ID: r.id}, err
	}
	d.audited(ctx, r)
	return Started{SID: r.sid, ID: r.id}, nil
}

func utf8Valid(s string) bool {
	return strings.ToValidUTF8(s, "�") == s
}

func (d *Dispatcher) claimStart(repo string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.reg.now()
	if d.starting || (!d.started.IsZero() && now.Sub(d.started) < StartGap) {
		return limited("wait", "Wait a few seconds between starts.")
	}
	d.starting = true
	d.startRepo = repo
	d.started = now
	return nil
}

func (d *Dispatcher) endStart() {
	d.mu.Lock()
	d.starting = false
	d.startRepo = ""
	d.mu.Unlock()
}

func (d *Dispatcher) admission(r *run, control bool) Admission {
	name := r.display
	if name == "" {
		name = r.spec.Name
	}
	var signIn *SignIn
	if r.signIn.Method != "" {
		s := r.signIn
		signIn = &s
	}
	return Admission{ID: r.id, Name: name, Kind: "background", Agent: r.spec.ID, Cwd: r.cwd, Project: r.repo, StartedAt: d.reg.now(), Control: control, SignIn: signIn,
		Lineage: lineageOf(r), Box: r.box, Branch: r.branch, Doubled: r.doubled, Docs: r.docsID()}
}

func (r *run) docsID() string {
	if sidPattern.MatchString(r.docs) {
		return r.docs
	}
	return r.sid
}

func launched(a Admission) Admission {
	a.Mode = launchMode
	return a
}

func (d *Dispatcher) claude(args ...string) []string {
	name := "claude"
	if i := slices.IndexFunc(d.cfg.Agents, func(a AgentSpec) bool { return a.Method == MethodPlugin && len(a.Command) > 0 }); i >= 0 {
		name = d.cfg.Agents[i].Command[0]
	}
	return append([]string{name}, args...)
}

func (d *Dispatcher) startClaude(ctx context.Context, r *run, model string) error {
	sid := d.scratchFreeSID(r)
	r.docs = sid
	argv := slices.Clone(r.spec.Command)
	if d.cfg.PluginDir != "" {
		argv = append(argv, "--plugin-dir="+d.cfg.PluginDir)
	}
	switch {
	case r.pack.Choice == PackNone:
	case r.pack.Path != "":
		argv = append(argv, "--plugin-dir="+r.pack.Path)
	case d.cfg.Workflow != nil:
		if dir, commit, err := d.cfg.Workflow(); err == nil {
			argv = append(argv, "--plugin-dir="+dir)
			r.stamp.workflow = commit
		} else {
			shared.For(ctx, d.cfg.Logger).Warn("dispatch", "event", "workflow-plugin-missing")
		}
	}
	if settings := d.claudeSettings(r); settings != "" {
		argv = append(argv, "--settings="+settings)
	}
	argv = append(argv, "--permission-mode="+launchMode, "--session-id", sid)
	if r.display != "" {
		argv = append(argv, "--name="+r.display)
	}
	if model != "" {
		argv = append(argv, "--model="+model)
	}
	if r.effort != "" {
		argv = append(argv, "--effort="+r.effort)
	}
	r.claudeArgv = argv
	pending := *r
	pending.sid = sid
	d.reg.Admit(sid, launched(d.admission(&pending, true)))
	d.observeStart(sid, r.check)
	d.reg.makeScreenshots(sid)
	d.reg.makeDocs(r.docs, r.box)
	d.reg.makeScratch(r.docs, r.box)
	res, err := d.host(r).Run(ctx, Command{Argv: argv, Dir: r.cwd, Pass: r.spec.Pass, Timeout: claudeRun})
	if err != nil {
		d.reg.Forget(sid)
		return conflict("claude", "agent-host couldn't run claude --bg: "+hostMessage(err))
	}
	out := output(res)
	id := ""
	if m := backgrounded.FindStringSubmatch(out); m != nil {
		id = m[1]
	}
	if id == "" && res.Code != 0 {
		d.reg.Forget(sid)
		msg := firstLine(out)
		if msg == "" {
			msg = "claude --bg failed."
		}
		if strings.Contains(strings.ToLower(msg), "not trusted") {
			return notTrusted(r.repo, r.clone, r.box)
		}
		return conflict("claude", msg)
	}
	found, foundID := d.findClaude(ctx, d.host(r), r.cwd, id, sid)
	if found == "" || foundID == "" {
		d.reg.Forget(sid)
		return conflict("claude", "claude --bg finished, but the session's container doesn't list the session it started.")
	}
	if found != sid {
		if d.sidTaken(found, r.box) {
			d.reg.Forget(sid)
			return conflict("claude", "The session's container listed a session id that another session already has, so the engine doesn't take it.")
		}
		d.reg.forget(sid, false)
		sid = found
	}
	r.id = foundID
	r.sid = sid
	r.running = true
	r.pidPendingSince = d.reg.now()
	d.booted(r)
	d.reg.Admit(sid, launched(d.admission(r, true)))
	d.observeStart(sid, r.check)
	d.mu.Lock()
	d.runs[sid] = r
	d.mu.Unlock()
	d.saveRuns()
	return nil
}

func (d *Dispatcher) sidTaken(sid, box string) bool {
	d.mu.Lock()
	r := d.runs[sid]
	d.mu.Unlock()
	if r != nil {
		return r.box != box
	}
	return d.reg.admittedElsewhere(sid, box)
}

type listed struct {
	SessionID string
	ID        string
	Kind      string
	Cwd       string
	StartedAt float64
	Pid       float64
}

func (d *Dispatcher) listClaude(ctx context.Context, host Host, dir string) ([]listed, error) {
	res, err := host.Run(ctx, Command{Argv: d.claude("agents", "--json", "--all"), Dir: dir, Timeout: claudeList})
	if err != nil || res.Code != 0 {
		return nil, errNoSessions
	}
	var list []listed
	if json.Unmarshal([]byte(res.Stdout), &list) != nil {
		return nil, errNoSessions
	}
	return list, nil
}

func (d *Dispatcher) findClaude(ctx context.Context, host Host, cwd, id, sid string) (string, string) {
	for attempt := range findAttempts {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ""
			case <-time.After(time.Second):
			}
		}
		list, err := d.listClaude(ctx, host, cwd)
		if err != nil {
			continue
		}
		for _, a := range list {
			if !shortID.MatchString(a.ID) || !sidPattern.MatchString(a.SessionID) {
				continue
			}
			if a.SessionID == sid && (id == "" || a.ID == id) {
				return a.SessionID, a.ID
			}
			if id != "" && a.ID == id && a.Kind == "background" && a.Cwd == cwd && strings.HasPrefix(a.SessionID, id+"-") {
				return a.SessionID, a.ID
			}
		}
	}
	return "", ""
}

func (d *Dispatcher) mcpFor(b *bridge, spec AgentSpec, box string) *MCPServer {
	if d.cfg.MCPCommand == "" {
		return nil
	}
	env := map[string]string{"SF_PUSH_TOKEN": b.pushToken()}
	if d.cfg.DevChecks && spec.ID == DevAgentID {
		if key := d.reg.CheckOnly(b.sid, box); key != "" {
			env[DevCheckVariable] = key
		}
	}
	if d.cfg.PluginSocket != "" {
		env["SF_PLUGIN_SOCKET"] = d.cfg.PluginSocket
	}
	if folder := ScreenshotFolder(BoxFolder(d.cfg.Workspace, box), b.sid); folder != "" {
		d.reg.makeScreenshots(b.sid)
		env[ScreenshotsVariable] = folder
	}
	if folder := DocsFolder(BoxFolder(d.cfg.Workspace, box), b.sid); folder != "" {
		d.reg.makeDocs(b.sid, box)
		env[DocsVariable] = folder
	}
	return &MCPServer{Name: PushServer, Command: d.cfg.MCPCommand, Args: []string{"mcp"}, Env: env}
}

func (d *Dispatcher) startStdio(ctx context.Context, r *run) error {
	if r.sid == "" {
		r.sid = newSID()
	}
	d.reg.Admit(r.sid, d.admission(r, false))
	d.observeStart(r.sid, r.check)
	d.mu.Lock()
	d.runs[r.sid] = r
	d.mu.Unlock()
	d.saveRuns()
	b, err := newBridge(d.reg, d.cfg.Logger, r.sid, r.cwd, r.box, d.cfg.Version)
	if err != nil {
		d.reg.Ended(r.sid, false)
		return conflict("register", "The engine couldn't open the session's conversation.")
	}
	opener := d.cfg.Stdio
	if opener == nil {
		b.fail("This engine can't speak that agent's protocol.")
		d.reg.Ended(r.sid, false)
		return conflict("protocol", "This engine can't speak that agent's protocol.")
	}
	host := d.host(r)
	p, err := host.Start(ctx, Command{Argv: r.spec.Command, Dir: r.cwd, Pass: r.spec.Pass})
	if err != nil {
		msg := "agent-host couldn't start " + r.spec.Name + ": " + hostMessage(err)
		b.fail(msg)
		d.reg.Ended(r.sid, false)
		return conflict("start", msg)
	}
	octx, cancel := context.WithTimeout(ctx, openFor)
	sess, err := opener.Open(octx, p, Launch{Cwd: r.cwd, AskingModes: r.spec.AskingModes, MCP: d.mcpFor(b, r.spec, r.box), Version: d.cfg.Version})
	cancel()
	if err != nil {
		_ = p.Close()
		sctx, scancel := context.WithTimeout(context.Background(), stopFor)
		_ = host.Stop(sctx, p.ID())
		scancel()
		msg := r.spec.Name + " didn't start: " + clip(err.Error(), 1000)
		b.fail(msg)
		d.reg.Ended(r.sid, false)
		return conflict("agent", msg)
	}
	d.mu.Lock()
	r.process = p
	r.bridge = b
	r.running = true
	r.stopping = false
	d.mu.Unlock()
	b.start(sess, func(byEngine bool) { d.ended(r, b, p, byEngine) })
	return nil
}

func (d *Dispatcher) ended(r *run, b *bridge, p Process, byEngine bool) {
	d.mu.Lock()
	current := r.bridge == b
	stopping := r.stopping
	d.mu.Unlock()
	lost := current && !stopping && !byEngine && !d.exitedItself(r, p.ID())
	d.mu.Lock()
	current = current && r.bridge == b
	if current {
		r.running = false
		r.stopping = false
		r.bridge = nil
		r.process = nil
	}
	d.mu.Unlock()
	_ = p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), stopFor)
	_ = d.host(r).Stop(ctx, p.ID())
	cancel()
	switch {
	case current && lost:
		d.lost(r.sid, b.cutTurn())
	case current && !stopping && !byEngine && r.spec.ID == DevAgentID:
		d.reg.EndedDone(r.sid)
		d.cfg.Logger.Info("agent session", "event", "ended", "done", true)
	case current:
		d.reg.Ended(r.sid, false)
		d.cfg.Logger.Info("agent session", "event", "ended")
	}
	if current && !stopping && !byEngine {
		go d.idleBox(r)
	}
}

func (d *Dispatcher) claim(sid string) (*run, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r := d.runs[sid]
	if r == nil {
		return nil, conflict("not started here", "Only a session this engine started can be stopped, woken or deleted from here.")
	}
	if r.busy {
		return nil, limited("busy", "Wait for the last action on this session to finish.")
	}
	r.busy = true
	return r, nil
}

func (d *Dispatcher) release(r *run) {
	d.mu.Lock()
	r.busy = false
	d.mu.Unlock()
}

func (d *Dispatcher) Stop(ctx context.Context, sid string) error {
	return d.stopAs(ctx, sid, ByPage, "")
}

func (d *Dispatcher) stopAs(ctx context.Context, sid, by, why string) error {
	r, err := d.claim(sid)
	if err != nil {
		return err
	}
	defer d.release(r)
	if r.box == "" {
		return oldLayout()
	}
	if d.liveBox(r.box) {
		_ = d.ConfirmBox(ctx, r.box)
	}
	if !d.liveBox(r.box) {
		err := d.stopGone(sid, r)
		if err == nil {
			d.lifecycle(ctx, AuditSessionStop, r, by, ResultStopped, stopReason(why, "its container wasn't running"))
		}
		return err
	}
	err = d.stopRun(ctx, sid, r)
	var e *Error
	if err != nil && (!errors.As(err, &e) || e.Reason != "not running") {
		return err
	}
	d.waitStopped(r)
	live := d.liveBox(r.box)
	d.stopBox(ctx, r.box)
	d.reg.WaitSignIn(sid, false)
	if err != nil && live {
		d.lifecycle(ctx, AuditSessionStop, r, by, ResultStopped, stopReason(why, "the agent had already stopped; its container was stopped"))
		return nil
	}
	if err == nil {
		d.lifecycle(ctx, AuditSessionStop, r, by, ResultStopped, why)
	}
	return err
}

func stopReason(why, also string) string {
	if why == "" {
		return also
	}
	return why + "; " + also
}

func (d *Dispatcher) stopGone(sid string, r *run) error {
	d.mu.Lock()
	running, b := r.running, r.bridge
	r.running = false
	d.mu.Unlock()
	if !running {
		return conflict("not running", "This session already stopped.")
	}
	if b != nil {
		b.close()
	}
	d.reg.Ended(sid, false)
	d.cfg.Logger.Info("agent session", "event", "stopped-without-container")
	return nil
}

func (d *Dispatcher) stopRun(ctx context.Context, sid string, r *run) error {
	d.mu.Lock()
	running, b, p := r.running, r.bridge, r.process
	if running && b != nil {
		r.stopping = true
	}
	d.mu.Unlock()
	if !running {
		return conflict("not running", "This session already stopped.")
	}
	if r.spec.Method == MethodPlugin {
		res, err := d.host(r).Run(ctx, Command{Argv: d.claude("stop", r.id), Dir: r.cwd, Timeout: claudeRun})
		if err != nil || res.Code != 0 {
			shared.For(ctx, d.cfg.Logger).Warn("agent session", "event", "claude-stop-failed", "box", r.box)
			d.stopBox(ctx, r.box)
		}
		d.mu.Lock()
		r.running = false
		d.mu.Unlock()
		d.reg.Ended(sid, false)
		return nil
	}
	if b != nil {
		b.close()
	}
	if p != nil {
		_ = d.host(r).Stop(ctx, p.ID())
	}
	return nil
}

func (d *Dispatcher) claimWake() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.reg.now()
	if !d.woken.IsZero() && now.Sub(d.woken) < WakeGap {
		return limited("wait", "Wait a few seconds and try again.")
	}
	d.woken = now
	return nil
}

func (d *Dispatcher) Wake(ctx context.Context, sid string, consent Consent) error {
	return d.wakeFor(ctx, sid, consent, ByPage, "")
}

func (d *Dispatcher) WakeAtReset(ctx context.Context, sid string) error {
	return d.wakeFor(shared.WithRequest(ctx, shared.NewRequest()), sid, Consent{}, ByReset, "woken at the usage reset the person asked for")
}

func (d *Dispatcher) wakeFor(ctx context.Context, sid string, consent Consent, by, why string) error {
	if err := d.historyOnly(sid); err != nil {
		return err
	}
	r, err := d.claim(sid)
	if err != nil {
		return err
	}
	defer d.release(r)
	d.mu.Lock()
	running := r.running
	d.mu.Unlock()
	if running {
		return conflict("not stopped", "This session is still running.")
	}
	id := d.cfg.Identity.Current()
	if !id.Set() {
		return noIdentity()
	}
	if err := d.claimWake(); err != nil {
		return err
	}
	d.gateBox(r)
	d.mu.Lock()
	waiting := !r.waitAt.IsZero() && d.reg.now().Sub(r.waitAt) < PendingBoxFor
	d.mu.Unlock()
	if !waiting && d.liveBox(r.box) {
		d.stopBox(ctx, r.box)
	}
	if err := d.wakeBox(ctx, r.box); err != nil {
		d.mu.Lock()
		d.woken = time.Time{}
		d.mu.Unlock()
		if !d.liveBox(r.box) {
			d.ungateBox(r)
		}
		return err
	}
	woke := false
	defer func() {
		if !woke {
			d.stopBox(ctx, r.box)
			d.reg.WaitSignIn(sid, false)
			d.dropOnce(r.box)
			d.mu.Lock()
			r.homeAsk, r.homeClear, r.waitAt = "", false, time.Time{}
			d.mu.Unlock()
			if !d.liveBox(r.box) {
				d.ungateBox(r)
			}
		}
	}()
	d.syncClone(r)
	if err := d.wakeWorktree(ctx, r, id); err != nil {
		d.mu.Lock()
		d.woken = time.Time{}
		d.mu.Unlock()
		return err
	}
	check, err := d.startCheck(ctx, r.box)
	if err != nil {
		return err
	}
	d.mu.Lock()
	r.check = check
	d.mu.Unlock()
	if err := d.homeGate(ctx, r, consent); err != nil {
		d.mu.Lock()
		d.woken = time.Time{}
		d.mu.Unlock()
		digest, asked := homeAskedDigest(err)
		if asked {
			d.reg.HoldHome(sid, true)
		}
		if asked && by == ByReset {
			shared.For(ctx, d.cfg.Logger).Info("limits", "event", "wake-at-reset-held")
			return err
		}
		if woke = d.waitInWake(r, err); woke && asked {
			d.mu.Lock()
			r.homeAsk, r.homeClear = digest, false
			d.mu.Unlock()
		}
		return err
	}
	d.mu.Lock()
	r.homeAsk, r.homeClear, r.homeGated = "", true, false
	d.mu.Unlock()
	d.reg.HoldHome(sid, false)
	if err := d.admit(ctx, r, consent); err != nil {
		d.mu.Lock()
		d.woken = time.Time{}
		d.mu.Unlock()
		woke = d.waitInWake(r, err)
		return err
	}
	d.reg.WaitSignIn(sid, false)
	if err := d.host(r).SetIdentity(ctx, r.repo, r.name, id); err != nil {
		d.mu.Lock()
		d.woken = time.Time{}
		d.mu.Unlock()
		shared.For(ctx, d.cfg.Logger).Warn("wake", "event", "identity-not-set")
		return conflict("identity", "agent-host couldn't set the name and email agents commit with in the session's worktree, so the session didn't wake: "+hostMessage(err))
	}
	d.mu.Lock()
	r.homeClear, r.waitAt = false, time.Time{}
	d.mu.Unlock()
	if d.cfg.Work != nil && !d.cfg.Work.Has(sid) {
		d.cfg.Work.Begin(sid, r.repo, r.typ)
	}
	if r.spec.Method == MethodPlugin {
		d.reg.makeDocs(r.docsID(), r.box)
		d.reg.makeScratch(r.docsID(), r.box)
		d.reg.makeScreenshots(sid)
		res, err := d.host(r).Run(ctx, Command{Argv: d.claude("respawn", r.id), Dir: r.cwd, Pass: r.spec.Pass, Timeout: claudeRun})
		if err != nil {
			return conflict("claude", hostMessage(err))
		}
		if res.Code != 0 {
			return conflict("claude", output(res))
		}
		d.mu.Lock()
		r.running = true
		r.pidPendingSince = d.reg.now()
		r.rmDone = false
		d.mu.Unlock()
		d.reg.Admit(sid, d.admission(r, true))
		d.observeStart(sid, r.check)
		d.saveRuns()
		d.woke(r)
		d.booted(r)
		woke = true
		d.homeServers(r)
		if w := d.workOf(); w != nil {
			w.SweepAfterWake(sid)
		}
		d.lifecycle(ctx, AuditSessionWake, r, by, ResultWoken, why)
		return nil
	}
	d.mu.Lock()
	r.rmDone = false
	d.mu.Unlock()
	err = d.startStdio(ctx, r)
	if err == nil {
		woke = true
		d.woke(r)
		d.homeServers(r)
		d.lifecycle(ctx, AuditSessionWake, r, by, ResultWoken, why)
	}
	return err
}

func offerOf(out string) *Offer {
	if m := discardOffer.FindStringSubmatch(out); m != nil {
		return &Offer{Flag: "discard", Value: m[1]}
	}
	if m := forceOffer.FindStringSubmatch(out); m != nil {
		return &Offer{Flag: "force", Value: m[1]}
	}
	return nil
}

func (d *Dispatcher) heldOffer(r *run, given *Offer) bool {
	return given != nil && r.offer != nil && *given == *r.offer && d.reg.now().Sub(r.offerAt) < OfferLife
}

func (d *Dispatcher) setOffer(r *run, o *Offer) {
	d.mu.Lock()
	r.offer = o
	r.offerAt = d.reg.now()
	d.mu.Unlock()
}

func (d *Dispatcher) Remove(ctx context.Context, sid string, given *Offer) (*Offer, error) {
	r, err := d.claim(sid)
	if err != nil {
		return nil, err
	}
	defer d.release(r)
	d.mu.Lock()
	running := r.running
	held := d.heldOffer(r, given)
	asideOK := held && given.Flag == "worktree" && r.aside != "" && given.Value == r.aside
	d.mu.Unlock()
	chose := held
	if running {
		return nil, conflict("running", saidStopFirst)
	}
	if r.history || r.box == "" {
		d.forgetHistory(sid)
		d.forgetCopy(sid)
		d.lifecycle(ctx, AuditSessionDelete, r, ByPage, ResultDeleted, "a session kept as history only: its record was forgotten")
		return nil, nil
	}
	if given != nil && !held {
		return nil, conflict("offer", "That choice is no longer the one offered. Try Delete again.")
	}
	boxGone, woken, dropped := false, false, false
	if err := d.wakeBox(ctx, r.box); err != nil {
		var e *Error
		if !errors.As(err, &e) || e.Reason != "box unknown" {
			return nil, err
		}
		shared.For(ctx, d.cfg.Logger).Warn("boxes", "event", "unknown-at-delete", "box", r.box)
		boxGone = true
	} else {
		woken = true
	}
	defer func() {
		if woken && !dropped {
			d.stopBox(ctx, r.box)
		}
	}()
	host := d.host(r)
	treeGone := boxGone
	if !treeGone {
		linked, lerr := d.worktreeLinked(r)
		treeGone = lerr == nil && !linked
	}
	unapproved := !treeGone && !r.doubled && !r.rmDone && r.spec.Method == MethodPlugin && d.homeUnapproved(ctx, r)
	if (treeGone || r.doubled || unapproved) && !r.rmDone {
		lead := goneLead
		switch {
		case unapproved:
			lead = homeLead
		case r.doubled && !treeGone:
			lead = doubledLead
		}
		if o, msg := d.goneOffer(r, lead); o != nil && (!held || *given != *o) {
			d.setOffer(r, o)
			return o, conflict("unpushed", msg)
		}
		d.rmFinished(r)
		held = false
	}
	if r.spec.Method == MethodPlugin && !r.rmDone {
		args := d.claude("rm", r.id)
		if held && (given.Flag == "discard" || given.Flag == "force") {
			name := "--discard-unpushed=" + given.Value
			if given.Flag == "force" {
				name = "--force-remove-worktree=" + given.Value
			}
			args = append(args, name)
		}
		res, err := host.Run(ctx, Command{Argv: args, Dir: r.cwd, Timeout: claudeRun})
		if err != nil {
			return nil, conflict("claude", hostMessage(err))
		}
		if res.Code != 0 {
			out := output(res)
			o := offerOf(out)
			d.setOffer(r, o)
			if out == "" {
				out = "claude rm failed."
			}
			return o, conflict("claude", out)
		}
		d.rmFinished(r)
		held = false
	}
	force := held && given.Flag == "worktree"
	aside, err := host.RemoveWorktree(ctx, r.repo, r.name, force)
	d.tellAside(r, aside, "while the session was being deleted")
	if err != nil && !boxGone {
		var h *HostError
		if errors.As(err, &h) && h.Code == HostDirty {
			o := &Offer{Flag: "worktree", Value: r.name}
			d.setOffer(r, o)
			return o, conflict("dirty", "The worktree "+r.name+" has changes that aren't committed. Choose to delete it anyway to discard them; the branch "+r.branch+" goes with the session's container, apart from what was pushed.")
		}
		shared.For(ctx, d.cfg.Logger).Warn("delete", "event", "worktree-not-removed", "worktree", r.name)
	}
	d.mu.Lock()
	folder := r.aside
	d.mu.Unlock()
	if folder != "" && !asideOK {
		o := &Offer{Flag: "worktree", Value: folder}
		d.setOffer(r, o)
		return o, conflict("set aside", "agent-host found the worktree "+r.name+" broken and moved it, with its files, to "+folder+" in the session's folder, so files that weren't committed are there. Deleting the session removes that folder too. Choose to delete it anyway, or wake the session first to look at them.")
	}
	if !d.dropBox(r.box, true) {
		return nil, conflict("box", "The engine removed the worktree, but the box helper couldn't remove the session's container and home. Try Delete again.")
	}
	dropped = true
	d.mu.Lock()
	delete(d.runs, sid)
	d.mu.Unlock()
	d.saveRuns()
	d.reg.Forget(sid)
	if d.cfg.Work != nil {
		d.cfg.Work.Forget(sid)
	}
	d.workForget(sid)
	d.forgetCopy(sid)
	d.lifecycle(ctx, AuditSessionDelete, r, ByPage, ResultDeleted, deleteReason(given, chose))
	return nil, nil
}

func deleteReason(given *Offer, held bool) string {
	if given == nil || !held {
		return ""
	}
	switch given.Flag {
	case "discard":
		return "the person chose to discard the commits that weren't pushed"
	case "force":
		return "the person chose to remove the worktree by force"
	case "worktree":
		return "the person chose to delete the worktree with what it held"
	}
	return ""
}

func (d *Dispatcher) rmFinished(r *run) {
	d.mu.Lock()
	r.rmDone = true
	d.mu.Unlock()
	d.setOffer(r, nil)
	d.saveRuns()
}

func (d *Dispatcher) UseBranchState(read func(WorkspaceSession) (string, string, string)) {
	d.mu.Lock()
	d.branchState = read
	d.mu.Unlock()
}

const (
	goneLead    = "The session's worktree is gone, so the agent can't check its branch before the session is deleted. "
	doubledLead = "Another session on this engine holds this session's branch, so this one is read-only and its agent can't check the branch before the session is deleted. "
	homeLead    = "The agent's user settings in this session's container changed since its last start and weren't approved, so the engine doesn't start Claude Code to check the branch before the session is deleted. "
)

func (d *Dispatcher) goneOffer(r *run, lead string) (*Offer, string) {
	d.mu.Lock()
	read := d.branchState
	s := WorkspaceSession{SID: r.sid, Name: r.display, Repo: r.repo, Path: r.clone, Branch: r.branch, Tree: r.name, Box: r.box}
	d.mu.Unlock()
	head, state, why := "", TreeUnknown, "The engine can't read this session's branch here."
	if read != nil {
		head, state, why = read(s)
	}
	if state == TreeMerged || state == TreePushed {
		return nil, ""
	}
	value := r.name
	if sha1Hex.MatchString(head) {
		value = head[:12] + "@" + r.name
	}
	if state == TreeUnpushed {
		then := " Push them first, or press the button again to discard them and delete the session."
		if lead == doubledLead {
			then = " Only this session's clone may hold them, and this session can't push or wake; press the button again to discard them and delete it, or start a session from its commit first to keep them."
		}
		return &Offer{Flag: "discard", Value: value}, lead + "The engine's own read finds commits on the branch " + r.branch + ", at " + shortSHA(head) + ", that aren't pushed or on main." + then
	}
	return &Offer{Flag: "discard", Value: value}, lead + why + " Press the button again to delete the session anyway; its branch goes with the session's container, apart from what was pushed."
}

func (d *Dispatcher) Review(ctx context.Context, sid string, costOK, every bool) error {
	if err := (PageOffers{Info: d.reg.Session}).Review(sid); err != nil {
		return err
	}
	scope := ScopeUnfinished
	if every {
		scope = ScopeEvery
	}
	return d.reviewAs(ctx, sid, costOK, false, scope)
}

func (d *Dispatcher) reviewAs(ctx context.Context, sid string, costOK, auto bool, scope ReviewScope) error {
	if err := d.historyOnly(sid); err != nil {
		return err
	}
	if d.cfg.Reviews == nil {
		return conflict("no reviewer", "This engine runs no reviewer containers, so it can't run reviews.")
	}
	if why := d.automaticHeldIf(auto, sid); why != "" {
		return conflict("limits", why)
	}
	t, ok := d.PushTarget(sid)
	if !ok {
		return conflict("not started here", "Only a session this engine started can be reviewed from here.")
	}
	rt := ReviewTarget{SID: sid, Repo: t.Repo, Name: t.Name, Branch: t.Branch, CostOK: costOK, Auto: auto, Scope: scope}
	if w := d.cfg.Work; w != nil {
		rt.Plan = func(ctx context.Context, base, head string) ([]workflow.Requirement, error) {
			required := w.Reviewers(ctx, sid, t, base, head)
			return required, ctx.Err()
		}
		rt.Rounds = w.Rounds(sid, t.Name)
		rt.Pushes = w.Pushes(sid, t.Name)
	}
	d.reviewGiven(&rt, sid, t)
	return d.cfg.Reviews.Start(ctx, rt)
}

func (d *Dispatcher) Override(ctx context.Context, sid, agent, commit, reason string) error {
	if err := d.historyOnly(sid); err != nil {
		return err
	}
	if d.cfg.Reviews == nil {
		return conflict("no reviewer", "This engine runs no reviewer containers, so it has no verdicts to override.")
	}
	t, ok := d.PushTarget(sid)
	if !ok {
		return conflict("not started here", "Only a session this engine started has verdicts to override here.")
	}
	return d.cfg.Reviews.Override(sid, ReviewTarget{SID: sid, Repo: t.Repo, Name: t.Name, Branch: t.Branch, Req: shared.RequestOf(ctx)}, agent, commit, reason)
}

func (d *Dispatcher) SessionsOf(repo string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, r := range d.runs {
		if r.repo == repo {
			n++
		}
	}
	if n == 0 && d.starting && d.startRepo == repo {
		n = 1
	}
	return n
}

func (d *Dispatcher) PushTarget(sid string) (PushTarget, bool) { return d.targetOf(sid, false) }

func (d *Dispatcher) HeldTarget(sid string) (PushTarget, bool) { return d.targetOf(sid, true) }

func (d *Dispatcher) targetOf(sid string, doubled bool) (PushTarget, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r := d.runs[sid]
	if r == nil || r.doubled != doubled || r.box == "" || r.clone == "" {
		return PushTarget{}, false
	}
	failed, why := d.unsyncedAt(r.box)
	return PushTarget{SID: sid, Repo: r.clone, Name: r.repo, Branch: r.branch, Project: r.repoPath, SyncFailed: failed, SyncWhy: why}, true
}

type quiet struct{}

func (quiet) Error(string, ...any) {}
func (quiet) Fatal(error)          {}
func (quiet) Info(string, ...any)  {}
func (quiet) Warn(string, ...any)  {}

func (d *Dispatcher) admit(ctx context.Context, r *run, consent Consent) error {
	if d.cfg.Packs != nil && (r.pack.Choice == "" || r.pack.Path == "" && PackDigest.MatchString(r.pack.Choice)) {
		var sp SessionPack
		var err error
		if r.pack.Choice == "" {
			sp, err = d.cfg.Packs.SessionPack(r.repo)
		} else {
			sp, err = d.cfg.Packs.SessionPackFor(r.pack.Choice)
		}
		if err != nil {
			return conflict("pack", clip("The project's pack isn't ready for a session, so the engine doesn't start one: "+err.Error(), maxOutput))
		}
		d.mu.Lock()
		r.pack = sp
		d.mu.Unlock()
	}
	if err := d.startup(r, consent); err != nil {
		return err
	}
	if r.spec.Method == MethodPlugin {
		if err := d.KeyRouted(ctx, BoxTarget(r.box)); err != nil {
			return err
		}
	}
	s, err := d.signIn(ctx, r)
	if err != nil {
		return err
	}
	d.mu.Lock()
	r.signIn = s
	d.mu.Unlock()
	want := d.expectFor(r.spec, r.box, s.Method)
	return judge(r.spec.Name, s, want, r.spec.StepIn(r.box), consent.CostOK, "the session")
}

func (d *Dispatcher) signIn(ctx context.Context, r *run) (SignIn, error) {
	if r.spec.Status == nil || len(r.spec.Command) == 0 {
		return SignIn{Method: SignInUnknown, CheckedAt: d.reg.now()}, nil
	}
	checker, ok := d.host(r).(SignInChecker)
	if !ok {
		return SignIn{}, conflict("no sign-in check", "The engine can't check "+r.spec.Name+"'s sign-in in the session's container, so it doesn't start it: "+hostMessage(d.hostDown(r)))
	}
	argv := append([]string{r.spec.Command[0]}, r.spec.Status.Args...)
	sctx, cancel := context.WithTimeout(ctx, signInFor)
	defer cancel()
	s, err := checker.SignIn(sctx, Command{Argv: argv, Dir: r.cwd, Pass: r.spec.Pass, Timeout: signInFor}, r.spec.Status.Kind)
	if err != nil {
		shared.For(ctx, d.cfg.Logger).Warn("sign-in check", "event", "failed")
		return SignIn{}, conflict("sign-in check", "The engine couldn't check "+r.spec.Name+"'s sign-in, so the session doesn't start: "+hostMessage(err))
	}
	s.CheckedAt = d.reg.now()
	shared.For(ctx, d.cfg.Logger).Info("sign-in check", "agent", r.spec.ID, "method", string(s.Method))
	return s, nil
}

func (d *Dispatcher) Expect(agent string, x Expect) error {
	if !ValidExpect(x) {
		return invalid("expect", "Expect a subscription, an API key or any sign-in.")
	}
	if _, ok := findAgent(d.cfg.Agents, agent); !ok && (agent != ReviewerAgent || d.cfg.Reviews == nil) {
		return invalid("agent", "Software Factory doesn't know that agent.")
	}
	if err := d.cfg.Expected.Set(agent, x); err != nil {
		return conflict("state", "The engine couldn't save what you expect.")
	}
	d.cfg.Logger.Info("sign-in expected", "agent", agent, "expect", string(x))
	return nil
}

func (d *Dispatcher) Expectations() map[string]Expect { return d.cfg.Expected.All() }

func (r *Registry) worktreeReaders() (Heads, WorktreeReader) {
	r.mu.Lock()
	b := r.bookLocked()
	r.mu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.watch == nil {
		return nil, nil
	}
	return b.watch.Heads, b.watch.Reader
}

func (d *Dispatcher) branchFree(branch string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range d.runs {
		if r.branch == branch {
			return conflict("branch held", "Another session works on the branch "+branch+", and two sessions never hold one branch.")
		}
	}
	return nil
}

func (d *Dispatcher) waitStopped(r *run) {
	until := d.reg.now().Add(stopSettle)
	for {
		d.mu.Lock()
		running := r.running
		d.mu.Unlock()
		if !running || !d.reg.now().Before(until) {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}
