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
	Remove(ctx context.Context, sid string, offer *Offer) (*Offer, error)
	Expect(agent string, x Expect) error
	Expectations() map[string]Expect
}

type DispatcherConfig struct {
	Agents       []AgentSpec
	Host         Host
	Openers      map[Method]Opener
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
	SignIns      SignInChecker
	Expected     *Expected
	Work         *Work
	Workflow     func() (string, error)
	Workspace    string
	Runs         RunStore
	Versions     func(agent string) (version, workflowCommit string)
	Note         func(sid, text string)
	Audit        Audit
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
	sid        string
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
}

type Dispatcher struct {
	cfg        DispatcherConfig
	checks     checks
	reg        *Registry
	mu         sync.Mutex
	runs       map[string]*run
	started    time.Time
	woken      time.Time
	starting   bool
	approvalMu sync.Mutex
	saveMu     sync.Mutex
}

var (
	_ Starter       = (*Dispatcher)(nil)
	_ ReviewStarter = (*Dispatcher)(nil)
)

func NewDispatcher(reg *Registry, cfg DispatcherConfig) *Dispatcher {
	if cfg.Logger == nil {
		cfg.Logger = quiet{}
	}
	return &Dispatcher{cfg: cfg, reg: reg, runs: map[string]*run{}}
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
	spec, ok := findAgent(d.cfg.Agents, q.Agent)
	if !ok {
		return Started{}, explained(invalid("agent"), "Software Factory doesn't know that agent.")
	}
	if !spec.Enabled {
		return Started{}, explained(conflict("agent off"), spec.Off)
	}
	if spec = d.installed(spec); !spec.Enabled {
		return Started{}, explained(conflict("not installed"), spec.Off)
	}
	if q.Model != "" && (spec.Method != MethodPlugin || !slices.Contains(ClaudeModels, q.Model)) {
		return Started{}, explained(invalid("model"), "That agent can't be started with that model from the page.")
	}
	if utf16Len(q.Name) > maxName || controlChars.MatchString(q.Name) || !utf8Valid(q.Name) {
		return Started{}, explained(invalid("name"), "A name has at most 80 characters.")
	}
	if d.cfg.Host == nil || d.cfg.Repositories == nil {
		return Started{}, explained(conflict("no agent-host"), "This engine has no agent-host to start sessions in.")
	}
	repos, err := d.cfg.Repositories.List()
	if err != nil {
		return Started{}, explained(conflict("workspace"), "The engine couldn't read the workspace's repositories.")
	}
	i := slices.IndexFunc(repos, func(r Repository) bool { return r.Path == q.Path })
	if i < 0 {
		return Started{}, explained(notFound("project"), "That project is no longer in the list.")
	}
	repo := repos[i]
	if d.cfg.Work != nil {
		if err := d.cfg.Work.Check(repo.Name, spec.Method == MethodPlugin, q.Type); err != nil {
			return Started{}, err
		}
	}
	if err := d.claimStart(); err != nil {
		return Started{}, err
	}
	defer d.endStart()
	check, err := d.startCheck(ctx)
	if err != nil {
		return Started{}, err
	}
	base, err := d.cfg.Repositories.Fresh(ctx, repo.Name)
	if err != nil {
		if e, ok := errors.AsType[*Error](err); ok {
			return Started{}, e
		}
		return Started{}, explained(conflict("fetch"), "The engine couldn't fetch the project's main branch, so no session started.")
	}
	short := randomHex(4)
	r := &run{spec: spec, repo: repo.Name, repoPath: repo.Path, base: base, name: "sf-" + short, branch: "sf/" + short, id: short, display: strings.TrimSpace(q.Name), check: check, typ: q.Type, at: d.reg.now()}
	r.stamp = d.stampOf(spec.ID, time.Time{})
	cwd, err := d.cfg.Host.AddWorktree(ctx, repo.Name, r.name, r.branch, base)
	if err != nil {
		d.cfg.Logger.Warn("dispatch", "event", "worktree-failed")
		return Started{}, explained(conflict("worktree"), "agent-host couldn't create the session's worktree: "+hostMessage(err))
	}
	r.cwd = cwd
	if err := d.admit(ctx, r, q.Consent); err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), stopFor)
		_ = d.cfg.Host.RemoveWorktree(cleanup, r.repo, r.name, true)
		cancel()
		d.mu.Lock()
		d.started = time.Time{}
		d.mu.Unlock()
		return Started{}, err
	}
	if spec.Method == MethodPlugin {
		err = d.startClaude(ctx, r, q.Model)
	} else {
		err = d.startStdio(ctx, r)
	}
	if err != nil && r.sid == "" {
		cleanup, cancel := context.WithTimeout(context.Background(), stopFor)
		_ = d.cfg.Host.RemoveWorktree(cleanup, r.repo, r.name, true)
		cancel()
	}
	if d.cfg.Work != nil && r.sid != "" {
		d.cfg.Work.Begin(r.sid, r.repo, spec.Method == MethodPlugin, q.Type)
	}
	if err != nil {
		return Started{SID: r.sid, ID: r.id}, err
	}
	return Started{SID: r.sid, ID: r.id}, nil
}

func utf8Valid(s string) bool {
	return strings.ToValidUTF8(s, "�") == s
}

func (d *Dispatcher) claimStart() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.reg.now()
	if d.starting || (!d.started.IsZero() && now.Sub(d.started) < StartGap) {
		return explained(limited("wait"), "Wait a few seconds between starts.")
	}
	d.starting = true
	d.started = now
	return nil
}

func (d *Dispatcher) endStart() {
	d.mu.Lock()
	d.starting = false
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
	return Admission{ID: r.id, Name: name, Kind: "background", Agent: r.spec.ID, Cwd: r.cwd, Project: r.repo, StartedAt: d.reg.now(), Control: control, SignIn: signIn}
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
	sid := newSID()
	argv := slices.Clone(r.spec.Command)
	if d.cfg.PluginDir != "" {
		argv = append(argv, "--plugin-dir="+d.cfg.PluginDir)
	}
	if d.cfg.Workflow != nil {
		if dir, err := d.cfg.Workflow(); err == nil {
			argv = append(argv, "--plugin-dir="+dir)
		} else {
			d.cfg.Logger.Warn("dispatch", "event", "workflow-plugin-missing")
		}
	}
	argv = append(argv, "--permission-mode="+launchMode, "--session-id", sid)
	if r.display != "" {
		argv = append(argv, "--name="+r.display)
	}
	if model != "" {
		argv = append(argv, "--model="+model)
	}
	r.claudeArgv = argv
	pending := *r
	pending.sid = sid
	d.reg.Admit(sid, launched(d.admission(&pending, true)))
	d.observeStart(sid, r.check)
	res, err := d.cfg.Host.Run(ctx, Command{Argv: argv, Dir: r.cwd, Pass: r.spec.Pass, Timeout: claudeRun})
	if err != nil {
		d.reg.Forget(sid)
		return explained(conflict("claude"), "agent-host couldn't run claude --bg: "+hostMessage(err))
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
			return notTrusted(r.repo, r.repoPath)
		}
		return explained(conflict("claude"), msg)
	}
	found, foundID := d.findClaude(ctx, r.cwd, id, sid)
	if found == "" || foundID == "" {
		d.reg.Forget(sid)
		return explained(conflict("claude"), "claude --bg finished, but the agent container's list doesn't show the session it started. Check the agent container's sessions before starting it again.")
	}
	if found != sid {
		d.reg.Forget(sid)
		sid = found
	}
	r.id = foundID
	r.sid = sid
	r.running = true
	d.reg.Admit(sid, launched(d.admission(r, true)))
	d.observeStart(sid, r.check)
	d.mu.Lock()
	d.runs[sid] = r
	d.mu.Unlock()
	d.saveRuns()
	return nil
}

type listed struct {
	SessionID string
	ID        string
	Kind      string
	Cwd       string
	StartedAt float64
}

func (d *Dispatcher) listClaude(ctx context.Context, dir string) ([]listed, error) {
	res, err := d.cfg.Host.Run(ctx, Command{Argv: d.claude("agents", "--json", "--all"), Dir: dir, Timeout: claudeList})
	if err != nil || res.Code != 0 {
		return nil, errNoSessions
	}
	var list []listed
	if json.Unmarshal([]byte(res.Stdout), &list) != nil {
		return nil, errNoSessions
	}
	return list, nil
}

func (d *Dispatcher) findClaude(ctx context.Context, cwd, id, sid string) (string, string) {
	for attempt := range findAttempts {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ""
			case <-time.After(time.Second):
			}
		}
		list, err := d.listClaude(ctx, cwd)
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

func (d *Dispatcher) mcpFor(b *bridge) *MCPServer {
	if d.cfg.MCPCommand == "" {
		return nil
	}
	env := map[string]string{"SF_PUSH_TOKEN": b.pushToken()}
	if d.cfg.PluginSocket != "" {
		env["SF_PLUGIN_SOCKET"] = d.cfg.PluginSocket
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
	b, err := newBridge(d.reg, d.cfg.Logger, r.sid, r.cwd, d.cfg.Version)
	if err != nil {
		d.reg.Ended(r.sid, false)
		return explained(conflict("register"), "The engine couldn't open the session's conversation.")
	}
	opener := d.cfg.Openers[r.spec.Method]
	if opener == nil {
		b.fail("This engine can't speak that agent's protocol.")
		d.reg.Ended(r.sid, false)
		return explained(conflict("protocol"), "This engine can't speak that agent's protocol.")
	}
	p, err := d.cfg.Host.Start(ctx, Command{Argv: r.spec.Command, Dir: r.cwd, Pass: r.spec.Pass})
	if err != nil {
		msg := "agent-host couldn't start " + r.spec.Name + ": " + hostMessage(err)
		b.fail(msg)
		d.reg.Ended(r.sid, false)
		return explained(conflict("start"), msg)
	}
	octx, cancel := context.WithTimeout(ctx, openFor)
	sess, err := opener.Open(octx, p, Launch{Cwd: r.cwd, AskingModes: r.spec.AskingModes, MCP: d.mcpFor(b), Version: d.cfg.Version})
	cancel()
	if err != nil {
		_ = p.Close()
		sctx, scancel := context.WithTimeout(context.Background(), stopFor)
		_ = d.cfg.Host.Stop(sctx, p.ID())
		scancel()
		msg := r.spec.Name + " didn't start: " + clip(err.Error(), 1000)
		b.fail(msg)
		d.reg.Ended(r.sid, false)
		return explained(conflict("agent"), msg)
	}
	d.mu.Lock()
	r.process = p
	r.bridge = b
	r.running = true
	r.stopping = false
	d.mu.Unlock()
	b.start(sess, func() { d.ended(r, b, p) })
	return nil
}

func (d *Dispatcher) ended(r *run, b *bridge, p Process) {
	d.mu.Lock()
	current := r.bridge == b
	if current {
		r.running = false
		r.stopping = false
		r.bridge = nil
		r.process = nil
	}
	d.mu.Unlock()
	_ = p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), stopFor)
	_ = d.cfg.Host.Stop(ctx, p.ID())
	cancel()
	if current {
		d.reg.Ended(r.sid, false)
	}
	d.cfg.Logger.Info("agent session", "event", "ended")
}

func (d *Dispatcher) claim(sid string) (*run, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r := d.runs[sid]
	if r == nil {
		return nil, explained(conflict("not started here"), "Only a session this engine started can be stopped, woken or deleted from here.")
	}
	if r.busy {
		return nil, explained(limited("busy"), "Wait for the last action on this session to finish.")
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
	r, err := d.claim(sid)
	if err != nil {
		return err
	}
	defer d.release(r)
	d.mu.Lock()
	running, b, p := r.running, r.bridge, r.process
	if running && b != nil {
		r.stopping = true
	}
	d.mu.Unlock()
	if !running {
		return explained(conflict("not running"), "This session already stopped.")
	}
	if r.spec.Method == MethodPlugin {
		res, err := d.cfg.Host.Run(ctx, Command{Argv: d.claude("stop", r.id), Dir: r.cwd, Timeout: claudeRun})
		if err != nil {
			return explained(conflict("claude"), hostMessage(err))
		}
		if res.Code != 0 {
			return explained(conflict("claude"), output(res))
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
		_ = d.cfg.Host.Stop(ctx, p.ID())
	}
	return nil
}

func (d *Dispatcher) claimWake() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.reg.now()
	if !d.woken.IsZero() && now.Sub(d.woken) < WakeGap {
		return explained(limited("wait"), "Wait a few seconds and try again.")
	}
	d.woken = now
	return nil
}

func (d *Dispatcher) Wake(ctx context.Context, sid string, consent Consent) error {
	r, err := d.claim(sid)
	if err != nil {
		return err
	}
	defer d.release(r)
	d.mu.Lock()
	running := r.running
	d.mu.Unlock()
	if running {
		return explained(conflict("not stopped"), "This session is still running.")
	}
	if err := d.claimWake(); err != nil {
		return err
	}
	check, err := d.startCheck(ctx)
	if err != nil {
		return err
	}
	d.mu.Lock()
	r.check = check
	d.mu.Unlock()
	if err := d.admit(ctx, r, consent); err != nil {
		d.mu.Lock()
		d.woken = time.Time{}
		d.mu.Unlock()
		return err
	}
	if d.cfg.Work != nil && !d.cfg.Work.Has(sid) {
		d.cfg.Work.Begin(sid, r.repo, r.spec.Method == MethodPlugin, r.typ)
	}
	if r.spec.Method == MethodPlugin {
		res, err := d.cfg.Host.Run(ctx, Command{Argv: d.claude("respawn", r.id), Dir: r.cwd, Pass: r.spec.Pass, Timeout: claudeRun})
		if err != nil {
			return explained(conflict("claude"), hostMessage(err))
		}
		if res.Code != 0 {
			return explained(conflict("claude"), output(res))
		}
		d.mu.Lock()
		r.running = true
		d.mu.Unlock()
		d.reg.Admit(sid, d.admission(r, true))
		d.observeStart(sid, r.check)
		d.woke(r)
		return nil
	}
	err = d.startStdio(ctx, r)
	if err == nil {
		d.woke(r)
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
	d.mu.Unlock()
	if running {
		return nil, explained(conflict("running"), "Stop the session before deleting it.")
	}
	if given != nil && !held {
		return nil, explained(conflict("offer"), "That choice is no longer the one offered. Try Delete again.")
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
		res, err := d.cfg.Host.Run(ctx, Command{Argv: args, Dir: r.cwd, Timeout: claudeRun})
		if err != nil {
			return nil, explained(conflict("claude"), hostMessage(err))
		}
		if res.Code != 0 {
			out := output(res)
			o := offerOf(out)
			d.setOffer(r, o)
			if out == "" {
				out = "claude rm failed."
			}
			return o, explained(conflict("claude"), out)
		}
		d.mu.Lock()
		r.rmDone = true
		d.mu.Unlock()
		d.setOffer(r, nil)
		held = false
	}
	force := held && given.Flag == "worktree"
	if err := d.cfg.Host.RemoveWorktree(ctx, r.repo, r.name, force); err != nil {
		var h *HostError
		if errors.As(err, &h) && h.Code == HostDirty {
			o := &Offer{Flag: "worktree", Value: r.name}
			d.setOffer(r, o)
			return o, explained(conflict("dirty"), "The worktree "+r.name+" has changes that aren't committed. Choose to delete it anyway to discard them; its branch "+r.branch+" stays.")
		}
		return nil, explained(conflict("worktree"), "agent-host couldn't remove the worktree: "+hostMessage(err))
	}
	d.mu.Lock()
	delete(d.runs, sid)
	d.mu.Unlock()
	d.saveRuns()
	d.reg.Forget(sid)
	if d.cfg.Work != nil {
		d.cfg.Work.Forget(sid)
	}
	return nil, nil
}

func (d *Dispatcher) Review(ctx context.Context, sid string, costOK bool) error {
	return d.reviewAs(ctx, sid, costOK, false)
}

func (d *Dispatcher) reviewAs(ctx context.Context, sid string, costOK, auto bool) error {
	if d.cfg.Reviews == nil {
		return explained(conflict("no reviewer"), "This engine has no reviewer container, so it can't run reviews.")
	}
	t, ok := d.PushTarget(sid)
	if !ok {
		return explained(conflict("not started here"), "Only a session this engine started can be reviewed from here.")
	}
	rt := ReviewTarget{SID: sid, Repo: t.Repo, Name: t.Name, Branch: t.Branch, CostOK: costOK, Auto: auto}
	if w := d.cfg.Work; w != nil {
		rt.Plan = func(base, head string) []workflow.Requirement { return w.Reviewers(sid, t, base, head) }
		rt.Rounds = w.Rounds(sid, t.Name)
	}
	return d.cfg.Reviews.Start(ctx, rt)
}

func (d *Dispatcher) Override(_ context.Context, sid, agent, commit, reason string) error {
	if d.cfg.Reviews == nil {
		return explained(conflict("no reviewer"), "This engine has no reviewer container, so it has no verdicts to override.")
	}
	t, ok := d.PushTarget(sid)
	if !ok {
		return explained(conflict("not started here"), "Only a session this engine started has verdicts to override here.")
	}
	return d.cfg.Reviews.Override(sid, ReviewTarget{SID: sid, Repo: t.Repo, Name: t.Name, Branch: t.Branch}, agent, commit, reason)
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
	return n
}

func (d *Dispatcher) PushTarget(sid string) (PushTarget, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r := d.runs[sid]
	if r == nil {
		return PushTarget{}, false
	}
	return PushTarget{SID: sid, Repo: r.repoPath, Name: r.repo, Branch: r.branch}, true
}

type quiet struct{}

func (quiet) Error(string, ...any) {}
func (quiet) Fatal(error)          {}
func (quiet) Info(string, ...any)  {}
func (quiet) Warn(string, ...any)  {}

func (d *Dispatcher) admit(ctx context.Context, r *run, consent Consent) error {
	if err := d.startup(r, consent); err != nil {
		return err
	}
	s, err := d.signIn(ctx, r)
	if err != nil {
		return err
	}
	d.mu.Lock()
	r.signIn = s
	d.mu.Unlock()
	want := d.cfg.Expected.Of(r.spec.ID)
	if r.spec.ID == "claude" {
		want = d.cfg.Expected.Adopt(r.spec.ID, s.Method)
	}
	return judge(r.spec.Name, s, want, d.step(r.spec), consent.CostOK, "the session")
}

func (d *Dispatcher) step(a AgentSpec) string {
	if a.Status != nil && a.Status.Step != "" {
		return a.Status.Step
	}
	return "its own sign-in in the agent container"
}

func (d *Dispatcher) signIn(ctx context.Context, r *run) (SignIn, error) {
	if r.spec.Status == nil || len(r.spec.Command) == 0 {
		return SignIn{Method: SignInUnknown, CheckedAt: d.reg.now()}, nil
	}
	if d.cfg.SignIns == nil {
		return SignIn{}, explained(conflict("no sign-in check"), "This engine can't check agents' sign-ins, so it doesn't start them.")
	}
	argv := append([]string{r.spec.Command[0]}, r.spec.Status.Args...)
	sctx, cancel := context.WithTimeout(ctx, signInFor)
	defer cancel()
	s, err := d.cfg.SignIns.SignIn(sctx, Command{Argv: argv, Dir: r.cwd, Pass: r.spec.Pass, Timeout: signInFor}, r.spec.Status.Kind)
	if err != nil {
		d.cfg.Logger.Warn("sign-in check", "event", "failed")
		return SignIn{}, explained(conflict("sign-in check"), "The engine couldn't check "+r.spec.Name+"'s sign-in, so the session doesn't start: "+hostMessage(err))
	}
	s.CheckedAt = d.reg.now()
	d.cfg.Logger.Info("sign-in check", "agent", r.spec.ID, "method", string(s.Method))
	return s, nil
}

func (d *Dispatcher) Expect(agent string, x Expect) error {
	if !ValidExpect(x) {
		return explained(invalid("expect"), "Expect a subscription, an API key or any sign-in.")
	}
	if _, ok := findAgent(d.cfg.Agents, agent); !ok && (agent != ReviewerAgent || d.cfg.Reviews == nil) {
		return explained(invalid("agent"), "Software Factory doesn't know that agent.")
	}
	if err := d.cfg.Expected.Set(agent, x); err != nil {
		return explained(conflict("state"), "The engine couldn't save what you expect.")
	}
	d.cfg.Logger.Info("sign-in expected", "agent", agent, "expect", string(x))
	return nil
}

func (d *Dispatcher) Expectations() map[string]Expect { return d.cfg.Expected.All() }
