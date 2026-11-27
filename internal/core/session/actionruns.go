package session

import (
	"context"
	"errors"
	"net/netip"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	MaxActionRuns     = 2
	MaxActionOutput   = 64 << 10
	MaxSessionRuns    = 8
	MaxKeptRuns       = 40
	AuditAction       = "action"
	ActionProxy       = "http://" + PortProxy
	actionNoteRoom    = 700
	actionPoll        = 2 * time.Second
	actionSlack       = 30 * time.Second
	actionStartFor    = 4 * time.Minute
	actionCallFor     = 30 * time.Second
	actionRemoveFor   = 90 * time.Second
	actionStateTries  = 5
	actionWithheld    = "[withheld]"
	actionSecretLeast = 12
	actionRunBytes    = 8
	actionReadPoll    = 250 * time.Millisecond
	actionReadFor     = time.Minute
	actionCopySlack   = 5 * time.Minute
)

const ActionStopped = "stopped"

type ActionInfo struct {
	Run    string
	Subnet netip.Prefix
	Recipe string
}

type ActionState struct {
	Run        string
	State      string
	ExitCode   int
	OOMKilled  bool
	Error      string
	StartedAt  time.Time
	FinishedAt time.Time
}

type ActionHost interface {
	MakeAction(ctx context.Context, run, box string) (ActionInfo, error)
	StartAction(ctx context.Context, run string) error
	ActionState(ctx context.Context, run string) (ActionState, error)
	ActionLog(ctx context.Context, run string) (string, bool, error)
	RemoveAction(ctx context.Context, run string) error
	ListActions(ctx context.Context) ([]BoxInfo, error)
}

type ActionFile struct {
	Path string
	Mode uint32
	Text string
}

type ActionSpec struct {
	Argv      []string
	Dir       string
	Env       []string
	Files     []ActionFile
	Askpass   bool
	AgentKey  string
	AgentUser string
}

type ActionFiles interface {
	Write(run string, s ActionSpec) error
	Withdraw(run string) error
	Remove(run string) error
	Prune(keep func(run string) bool) error
}

type ActionNet interface {
	OpenAction(ctx context.Context, run string, subnet netip.Prefix, list EgressList) error
	CloseAction(ctx context.Context, run string)
}

type ActionsConfig struct {
	Store ActionStore
	Host  ActionHost
	Files ActionFiles
	Net   ActionNet
	Base  context.Context
}

type ActionRun struct {
	ID         string
	SID        string
	Project    string
	Action     string
	Name       string
	Argv       []string
	Command    string
	Target     string
	Credential string
	CredID     string
	Files      bool
	Limit      time.Duration
	By         string
	Allowed    string
	Grant      string
	Started    time.Time
	Ended      time.Time
	State      string
	Code       int
	Output     string
	Cut        bool
	Reason     string
}

type actionLive struct {
	stop chan struct{}
	once sync.Once
	why  string
}

func (l *actionLive) end(why string) {
	l.once.Do(func() {
		l.why = why
		close(l.stop)
	})
}

type actionGrant struct {
	ID         string
	SID        string
	Project    string
	Action     string
	Name       string
	Credential string
	Argv       []string
	Expires    time.Time
	Made       time.Time
}

type actionKey struct {
	sid, action, credential, grant string
}

type ActionGrantView struct {
	ID      string
	Name    string
	Command string
	Until   time.Time
}

type ActionRunView struct {
	ActionRun
	Running bool
	Said    string
}

var ActionExpiries = []GrantChoice{{Value: ActionOnce, Label: "Once, this run only"}, {Value: "hour", Label: "1 hour"}, {Value: "day", Label: "1 day"}}

const ActionOnce = "once"

var actionLasts = map[string]string{ActionOnce: "for this run only", "hour": "for 1 hour", "day": "for 1 day"}

func ActionWhere(c Credential) string {
	s := c.Secret
	switch c.Type {
	case CredentialToken:
		return c.Name + "'s token as $" + s.Variable
	case CredentialVars:
		return c.Name + "'s variables"
	case CredentialAWS:
		return "a short-lived copy of " + c.Name + "'s keys"
	case CredentialFile:
		return c.Name + "'s file at ~/" + s.Path
	case CredentialSSH:
		if s.Password != "" {
			return c.Name + "'s password"
		}
		return c.Name + "'s key, which the command can't read"
	}
	return c.Name + "'s secret"
}

func agentKeyAction(c Credential) bool {
	return c.Type == CredentialSSH && c.Secret.Password == ""
}

func actionLeakWords(c Credential, target string) string {
	if agentKeyAction(c) {
		return "use " + c.Name + "'s key at " + target + " for anything the key allows"
	}
	return "send " + c.Name + "'s secret to " + target + " or print it past the output check"
}

func actionRisk(a Action, c Credential, expires string) AccessRisk {
	r := AccessRisk{Scope: GrantSession, Expires: expires, Level: RiskMedium}
	var accept []string
	runs := "Runs in a container of its own with " + ActionWhere(c) + "; the agent sees only the output, with secrets withheld."
	r.Says = append(r.Says, runs)
	if a.Files {
		r.Level = RiskHigh
		r.Says = append(r.Says, "It reads this session's files at "+ActionWork+", which the agent wrote, so they can make it "+actionLeakWords(c, a.Target)+".")
		accept = append(accept, "files the agent wrote can steer what the command does with "+c.Name)
	}
	if WorktreeProgram(a.Program) {
		r.Level = RiskHigh
		r.Says = append(r.Says, a.Program+" comes from this session's files, so the agent decides what runs and can "+actionLeakWords(c, a.Target)+".")
		accept = append(accept, "the agent decides what runs with "+c.Name+"'s secret")
	}
	if c.Delivery() == DeliveryHanded {
		r.Level = RiskHigh
		r.Says = append(r.Says, "The command gets "+c.Name+"'s secret and can do anything the secret allows at "+a.Target+".")
		accept = append(accept, "the command can do anything "+c.Name+" allows at "+a.Target)
	}
	for _, t := range c.Tags {
		switch t {
		case TagWrite:
			r.Level = RiskHigh
			r.Says = append(r.Says, c.Name+" is tagged write: the command can change things at "+a.Target+".")
			accept = append(accept, "the command can change things at "+a.Target)
		case TagProduction:
			r.Level = RiskHigh
			r.Says = append(r.Says, c.Name+" is tagged production: what the command does reaches production.")
			accept = append(accept, "what the command does reaches production")
		}
	}
	for _, e := range lanEntries([]string{a.Target}) {
		h, n, _ := SplitPortTarget(e)
		r.Level = RiskHigh
		r.Says = append(r.Says, h+" is on your network: the command can reach whatever answers on port "+strconv.Itoa(n)+".")
		accept = append(accept, "the command can reach "+e)
	}
	if c.Type == CredentialSSH && c.Secret.HostKey == "" {
		r.Says = append(r.Says, "No host key is saved with "+c.Name+", so ssh trusts the first key "+targetHost(a.Target)+" shows.")
	}
	if len(a.Slots) > 0 {
		names := make([]string, 0, len(a.Slots))
		for _, s := range a.Slots {
			names = append(names, "{"+s.Name+"}")
		}
		r.Says = append(r.Says, "The agent chose "+hostList(names)+" within your patterns; the command line above is exactly what runs.")
	}
	if expires != ActionOnce {
		r.Says = append(r.Says, "The agent can run it again with these exact arguments without a card; taking the grant back stops a run.")
	}
	if len(accept) > 0 {
		r.Accept = "I accept that " + strings.Join(accept, ", and that ")
	}
	r.Label = riskLabels[r.Level]
	return r
}

func actionRisks(a Action, c Credential) []AccessRisk {
	out := make([]AccessRisk, 0, len(ActionExpiries))
	for _, e := range ActionExpiries {
		out = append(out, actionRisk(a, c, e.Value))
	}
	return out
}

func actionCardKey(action string, argv []string) string {
	return "action:" + action + ":" + strings.Join(argv, "\x00")
}

func (g *Grants) actionCardLocked(c *accessCard) (AccessCard, bool) {
	a, ok := g.actionByID(c.action)
	if !ok {
		return AccessCard{}, false
	}
	cred, ok := g.credentialByID(a.Credential)
	if !ok || g.credsBroken {
		return AccessCard{}, false
	}
	title := "The agent asks to run " + a.Name + ". Run it?"
	return AccessCard{ID: c.id, Kind: AccessAction, Action: a.Name, Command: QuotedCommand(c.argv), Argv: slices.Clone(c.argv), Target: a.Target, Credential: cred.Name, Files: a.Files, Minutes: a.Minutes,
		Title: title, Asked: c.asked, Risks: actionRisks(a, cred), Scopes: []GrantChoice{GrantScopes[0]}, Expiries: ActionExpiries}, true
}

func (g *Grants) checkArgs(a Action, given map[string]string) (map[string]string, error) {
	for k := range given {
		if !slices.ContainsFunc(a.Slots, func(s ActionSlot) bool { return s.Name == k }) {
			return nil, invalid("args", a.Name+" takes no argument named "+CleanLine(clip(k, 40), 40))
		}
	}
	out := map[string]string{}
	for _, s := range a.Slots {
		v, ok := given[s.Name]
		if !ok {
			return nil, invalid("args", a.Name+" needs the argument "+s.Name)
		}
		re, err := anchored(s.Pattern)
		switch {
		case err != nil:
			return nil, conflict("args", "the pattern saved for "+s.Name+" doesn't compile any more")
		case len(v) > s.Max || !plainArg(v, MaxSlotValue):
			return nil, invalid("args", s.Name+" must be one line of at most "+strconv.Itoa(s.Max)+" bytes")
		case strings.HasPrefix(v, "-"):
			return nil, invalid("args", s.Name+" can't start with -, so it can't add an option")
		case strings.HasPrefix(v, "/") || strings.HasPrefix(v, "~"):
			return nil, invalid("args", s.Name+" can't start with / or ~, so it names no path outside the worktree")
		case slices.Contains(strings.Split(v, "/"), ".."):
			return nil, invalid("args", s.Name+" can't hold a .. part, so it names no path outside the worktree")
		case secretText(v):
			return nil, invalid("args", s.Name+" looks like a token or a key")
		case !re.MatchString(v):
			return nil, invalid("args", s.Name+" doesn't match the pattern the person saved for it: "+s.Pattern)
		}
		out[s.Name] = v
	}
	return out, nil
}

func (g *Grants) actionGrantFor(sid, action string, argv []string, now time.Time) (actionGrant, bool) {
	for _, x := range g.actionGrants {
		if x.SID == sid && x.Action == action && slices.Equal(x.Argv, argv) && now.Before(x.Expires) {
			return x, true
		}
	}
	return actionGrant{}, false
}

func agentErrWords(err error) string {
	if e, ok := errors.AsType[*Error](err); ok && e.Message != "" {
		return lowerFirst(strings.TrimSuffix(e.Message, "."))
	}
	return "Software Factory couldn't start it"
}

func (g *Grants) RunAction(ctx context.Context, sid, name string, given map[string]string) AccessAnswer {
	refuse := func(why string) AccessAnswer { return AccessAnswer{Result: AccessRefused, Reason: why} }
	t, ok := g.cfg.Session(sid)
	if !ok || !t.Live {
		return refuse("only a running session this engine started can run an action")
	}
	if g.actionsOff() != nil {
		return refuse("this engine runs no actions; tell the person what you need and why")
	}
	g.mu.Lock()
	if g.actionsBroken || g.credsBroken {
		g.mu.Unlock()
		return refuse("the engine can't read its actions or credentials, so it runs none; tell the person")
	}
	a, found := g.actionByName(name)
	names := make([]string, 0, len(g.actions))
	for _, x := range g.actions {
		names = append(names, x.Name)
	}
	cred, credOK := g.credentialByID(a.Credential)
	g.mu.Unlock()
	if found && (!credOK || !boundTo(cred, a.Target)) {
		return refuse("the action " + a.Name + " can't run: its credential is gone or no longer bound to its target; tell the person")
	}
	if git := g.actionGitHost(a.Target); found && git != "" {
		return refuse("the action " + a.Name + " can't run: " + actionGitWords(a.Target, git) + "; tell the person")
	}
	if !found {
		if len(names) == 0 {
			return refuse("no action is saved; ask the person to save one on Software Factory's Credentials page")
		}
		return refuse("no action named " + CleanLine(clip(name, 40), 40) + " is saved; the saved ones are " + hostList(names))
	}
	values, err := g.checkArgs(a, given)
	if err != nil {
		return refuse(agentErrWords(err))
	}
	argv := a.argv(values)
	now := g.cfg.Now()
	key := actionCardKey(a.ID, argv)
	g.mu.Lock()
	gr, granted := g.actionGrantFor(sid, a.ID, argv, now)
	g.mu.Unlock()
	if granted {
		run, err := g.startRun(ctx, t, a, argv, AskedByAgent, "the grant you gave at its card, "+untilWords(gr.Expires), gr.ID)
		if err != nil {
			return refuse(agentErrWords(err))
		}
		return AccessAnswer{Result: AccessHeld, Reason: "Software Factory runs " + a.Name + " now under the grant the person gave, as run " + run.ID + ": " + run.Command + "; its output and exit code reach you with your next message"}
	}
	g.mu.Lock()
	if at, recent := g.recentlyRefusedLocked(sid, key, now); recent {
		g.mu.Unlock()
		return refuse("the person didn't run " + a.Name + " with these arguments less than 10 minutes ago, so don't ask again before " + at.Add(AccessRefusedFor).UTC().Format(time.RFC3339) + "; tell them why you need it instead")
	}
	for _, c := range g.cards[sid] {
		if c.kind != AccessAction {
			continue
		}
		g.mu.Unlock()
		if c.key() == key {
			return AccessAnswer{Result: AccessHeld, Reason: "your request to run " + a.Name + " is still waiting for the person on Software Factory's page; the result reaches you with your next message"}
		}
		return refuse("this session already has an action waiting for the person; wait for its answer, which reaches you with your next message")
	}
	if !g.actionAsks.allow(sid, now) {
		g.mu.Unlock()
		return refuse("this session asked to run actions " + strconv.Itoa(AccessAsks) + " times in the last hour, the most Software Factory takes; tell the person what you need")
	}
	list := g.cards[sid]
	if len(list) >= MaxAccessCards {
		list = list[1:]
	}
	g.cards[sid] = append(list, &accessCard{id: newGrantID(), kind: AccessAction, action: a.ID, argv: argv, asked: now})
	g.mu.Unlock()
	g.show(sid)
	g.cfg.Logger.Info("actions", "event", "asked")
	return AccessAnswer{Result: AccessHeld, Reason: "Software Factory's page shows the person the command line " + QuotedCommand(argv) + " to accept; if they do, it runs, and its output and exit code reach you with your next message. Go on with other work, or end your turn if you can't go on without it"}
}

func (g *Grants) allowActionCard(ctx context.Context, t GrantTarget, card *accessCard, q AccessChoice) (string, error) {
	if !grantChoice(ActionExpiries, q.Expires) {
		return "", onField("expires", invalid("expires", "Choose once, 1 hour or 1 day."))
	}
	g.mu.Lock()
	a, ok := g.actionByID(card.action)
	c, cok := g.credentialByID(a.Credential)
	g.mu.Unlock()
	if !ok || !cok {
		return "", notFound("action", "That action or its credential isn't there any more. Reload the page.")
	}
	if r := actionRisk(a, c, q.Expires); r.Accept != "" && !q.Confirmed {
		return "", onField("confirmed", invalid("confirm", "Tick the box to accept the risk first."))
	}
	allowed := "you, at its card"
	grantID := ""
	now := g.cfg.Now()
	var gr actionGrant
	if q.Expires != ActionOnce {
		gr = actionGrant{ID: newGrantID(), SID: t.SID, Project: t.Project, Action: a.ID, Name: a.Name, Credential: a.Credential, Argv: slices.Clone(card.argv), Expires: now.Add(expiryChoices[q.Expires]).Truncate(time.Second), Made: now.Truncate(time.Second)}
		grantID, allowed = gr.ID, "the grant you gave at its card, "+untilWords(gr.Expires)
	}
	run, err := g.startRun(ctx, t, a, card.argv, AskedByAgent, allowed, grantID)
	if err != nil {
		return "", err
	}
	g.mu.Lock()
	if gr.ID != "" {
		g.actionGrants = append(slices.DeleteFunc(g.actionGrants, func(x actionGrant) bool {
			return x.SID == gr.SID && x.Action == gr.Action && slices.Equal(x.Argv, gr.Argv)
		}), gr)
	}
	g.noteAnsweredLocked(t.SID, card.key(), now)
	g.mu.Unlock()
	g.show(t.SID)
	if gr.ID != "" {
		g.auditAction(ctx, t, ResultGranted, ByPage, a.Name+" with "+run.Command+" against "+a.Target+" using "+c.Name+", "+actionLasts[q.Expires]+", asked by the agent")
		return "Running " + a.Name + " now; the agent can run it again with these arguments " + actionLasts[q.Expires] + " without a card.", nil
	}
	return "Running " + a.Name + " now, this once.", nil
}

func (g *Grants) refuseActionCard(ctx context.Context, t GrantTarget, c *accessCard) string {
	name := "the action"
	g.mu.Lock()
	if a, ok := g.actionByID(c.action); ok {
		name = a.Name
	}
	g.mu.Unlock()
	g.auditAction(ctx, t, ResultNotGranted, ByPage, name+" with "+QuotedCommand(c.argv)+", which the agent asked to run")
	g.cfg.Note(t.SID, "The person didn't run "+name+" with "+QuotedCommand(c.argv)+" on Software Factory's page. Don't ask for it again for 10 minutes; if you still need it, say why in your reply.")
	return "Didn't run " + name + ". The agent can't ask for it again with these arguments for 10 minutes."
}

func (g *Grants) StartAction(ctx context.Context, sid, id string, given map[string]string) (string, error) {
	t, err := g.target(sid)
	if err != nil {
		return "", err
	}
	if err := g.actionsOff(); err != nil {
		return "", err
	}
	if !t.Live {
		return "", conflict("stopped", saidActionStopped)
	}
	g.mu.Lock()
	if g.actionsBroken {
		g.mu.Unlock()
		return "", actionsUnread()
	}
	a, ok := g.actionByID(id)
	g.mu.Unlock()
	if !ok {
		return "", notFound("action", "That action isn't there any more. Reload the page.")
	}
	values, err := g.checkArgs(a, given)
	if err != nil {
		msg := "Check the arguments."
		if e, ok := errors.AsType[*Error](err); ok && e.Message != "" {
			msg = strings.ToUpper(e.Message[:1]) + e.Message[1:] + "."
		}
		return "", onField("args", invalid("args", clip(msg, 60)))
	}
	run, err := g.startRun(ctx, t, a, a.argv(values), AskedByPerson, "you, from the Access dialog", "")
	if err != nil {
		return "", err
	}
	return "Running " + a.Name + ": " + clip(run.Command, 200), nil
}

func (g *Grants) StopAction(ctx context.Context, sid, run string) (string, error) {
	if _, err := g.target(sid); err != nil {
		return "", err
	}
	g.mu.Lock()
	l, live := g.liveRuns[run]
	owned := slices.ContainsFunc(g.runs, func(r ActionRun) bool { return r.ID == run && r.SID == sid })
	g.mu.Unlock()
	if !live || !owned {
		return "", conflict("not running", "That action isn't running.")
	}
	l.end("you stopped it")
	shared.For(ctx, g.cfg.Logger).Info("actions", "event", "stop-asked")
	return "Stopping the run. Its container is removed within a few seconds.", nil
}

func (g *Grants) ActionResult(sid, id string) AccessAnswer {
	g.mu.Lock()
	defer g.mu.Unlock()
	i := slices.IndexFunc(g.runs, func(r ActionRun) bool { return r.ID == id && r.SID == sid })
	if i < 0 {
		return AccessAnswer{Result: AccessRefused, Reason: "this session has no action run " + CleanLine(clip(id, 20), 20) + " that Software Factory still keeps; it keeps the latest " + strconv.Itoa(MaxSessionRuns) + " runs of a session until the engine restarts"}
	}
	r := g.runs[i]
	if _, live := g.liveRuns[id]; live {
		return AccessAnswer{Result: AccessHeld, Reason: r.Name + " is still running; its output and exit code reach you with your next message after it ends"}
	}
	text := runWords(r)
	switch {
	case r.Output == "":
		text += " It printed nothing."
	case r.Cut:
		text += " The last 64 KiB of its output, stdout and stderr together:\n\n" + r.Output
	default:
		text += " Its output, stdout and stderr together:\n\n" + r.Output
	}
	return AccessAnswer{Result: ActionDone, Reason: text}
}

const ActionDone = "done"

func runWords(r ActionRun) string {
	took := r.Ended.Sub(r.Started).Round(time.Second).String()
	w := "Software Factory ran the action " + r.Name + " (" + r.Command + ") against " + r.Target
	switch r.State {
	case RunPassed, RunFailed:
		w += ": it exited with code " + strconv.Itoa(r.Code) + " after " + took + "."
	case RunTimedOut:
		w += ": it didn't finish within " + minutes(r.Limit) + ", so it was stopped."
	case ActionStopped:
		w += ": it was stopped after " + took + ", because " + r.Reason + "."
	default:
		w += ": it couldn't run. " + r.Reason
	}
	return w
}

func (g *Grants) runningLocked() int {
	return len(g.liveRuns)
}

const saidActionStopped = "Actions run for a running session. Wake it first."

func (g *Grants) startRun(ctx context.Context, t GrantTarget, a Action, argv []string, by, allowed, grant string) (ActionRun, error) {
	if err := g.actionsOff(); err != nil {
		return ActionRun{}, err
	}
	if !t.Live {
		return ActionRun{}, conflict("stopped", saidActionStopped)
	}
	if a.Files && t.Worktree == "" {
		return ActionRun{}, conflict("worktree", "The engine doesn't know this session's worktree, so it runs no action with the session's files.")
	}
	g.mu.Lock()
	c, ok := g.credentialByID(a.Credential)
	if !ok || g.credsBroken {
		g.mu.Unlock()
		return ActionRun{}, conflict("credential", "The action's credential is gone, so it can't run.")
	}
	if !boundTo(c, a.Target) {
		g.mu.Unlock()
		return ActionRun{}, conflict("bound", c.Name+" isn't bound to "+a.Target+" any more, so the action can't run.")
	}
	if git := g.actionGitHost(a.Target); git != "" {
		g.mu.Unlock()
		return ActionRun{}, conflict("git host", upperFirst(actionGitWords(a.Target, git))+", so the action can't run. Save it with another target.")
	}
	if g.runningLocked() >= MaxActionRuns {
		g.mu.Unlock()
		return ActionRun{}, limited("actions", "Two actions are running, the most Software Factory runs at once. Wait for one to end.")
	}
	run := ActionRun{ID: randomHex(actionRunBytes), SID: t.SID, Project: t.Project, Action: a.ID, Name: a.Name, Argv: slices.Clone(argv), Command: QuotedCommand(argv), Target: a.Target,
		Credential: c.Name, CredID: c.ID, Files: a.Files, Limit: time.Duration(a.Minutes) * time.Minute, By: by, Allowed: allowed, Grant: grant, Started: g.cfg.Now(), State: RunRunning}
	live := &actionLive{stop: make(chan struct{})}
	g.liveRuns[run.ID] = live
	g.runs = append(g.runs, run)
	g.mu.Unlock()
	shared.For(ctx, g.cfg.Logger).Info("actions", "event", "started", "run", run.ID, "by", by)
	base := g.cfg.Actions.Base
	if base == nil {
		base = context.Background()
	}
	go g.execute(shared.WithRequest(base, shared.RequestOf(ctx)), run, a, c, t, live)
	return run, nil
}

func (g *Grants) spec(ctx context.Context, run ActionRun, a Action, c Credential, t GrantTarget) (ActionSpec, []string, error) {
	s := ActionSpec{Argv: run.Argv, Dir: ActionHome, Env: []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=" + ActionHome, "LANG=C.UTF-8", "TERM=dumb",
		"HTTPS_PROXY=" + ActionProxy, "HTTP_PROXY=" + ActionProxy, "https_proxy=" + ActionProxy, "http_proxy=" + ActionProxy, "NO_PROXY=" + "localhost,127.0.0.1,::1", "no_proxy=" + "localhost,127.0.0.1,::1",
	}}
	if a.Files {
		s.Dir = path.Join(ActionWork, "worktrees", t.Worktree)
	}
	sec := c.Secret
	secrets := c.secretTexts()
	switch c.Type {
	case CredentialToken:
		s.Env = append(s.Env, sec.Variable+"="+sec.Value)
	case CredentialVars:
		for _, v := range sec.Vars {
			s.Env = append(s.Env, v.Name+"="+v.Value)
		}
	case CredentialFile:
		mode := uint32(0o600)
		if sec.Mode == FileModeShared {
			mode = 0o644
		}
		s.Files = append(s.Files, ActionFile{Path: sec.Path, Mode: mode, Text: sec.Contents})
	case CredentialAWS:
		cctx, cancel := context.WithTimeout(ctx, awsCallFor)
		now := g.cfg.Now()
		cp, err := g.cfg.AWS.Copy(cctx, AWSCopyAsk{KeyID: sec.KeyID, SecretKey: sec.SecretKey, Role: sec.Role, Region: sec.Region, Name: awsSessionName + run.ID, Duration: copyDuration(now.Add(run.Limit+actionCopySlack), now)})
		cancel()
		if err != nil {
			return ActionSpec{}, nil, conflict("aws", "AWS didn't give a short-lived copy of "+c.Name+"'s keys.")
		}
		secrets = append(secrets, cp.KeyID, cp.SecretKey, cp.Token)
		s.Files = append(s.Files,
			ActionFile{Path: ".aws/credentials", Mode: 0o600, Text: "[" + c.Name + "]\naws_access_key_id = " + cp.KeyID + "\naws_secret_access_key = " + cp.SecretKey + "\naws_session_token = " + cp.Token + "\n"},
			ActionFile{Path: ".aws/config", Mode: 0o600, Text: "[profile " + c.Name + "]\nregion = " + sec.Region + "\n"})
		s.Env = append(s.Env, "AWS_PROFILE="+c.Name, "AWS_REGION="+sec.Region, "AWS_DEFAULT_REGION="+sec.Region)
	case CredentialSSH:
		host, port, _ := SplitPortTarget(a.Target)
		lines := []string{"Host " + host, "  HostName " + host, "  Port " + strconv.Itoa(port), "  User " + sec.User,
			"  ProxyCommand nc -X connect -x " + PortProxy + " %h %p", "  UserKnownHostsFile ~/.ssh/known_hosts", "  ServerAliveInterval 30"}
		if sec.HostKey != "" {
			known := host
			if port != 22 {
				known = "[" + host + "]:" + strconv.Itoa(port)
			}
			lines = append(lines, "  StrictHostKeyChecking yes")
			s.Files = append(s.Files, ActionFile{Path: ".ssh/known_hosts", Mode: 0o600, Text: known + " " + sec.HostKey + "\n"})
		} else {
			lines = append(lines, "  StrictHostKeyChecking accept-new")
		}
		if agentKeyAction(c) {
			lines = append(lines, "  IdentityAgent "+ActionAgent, "  IdentityFile none", "  PreferredAuthentications publickey")
			s.AgentKey, s.AgentUser = sec.PrivateKey, sec.User
			s.Env = append(s.Env, "SSH_AUTH_SOCK="+ActionAgent)
		} else {
			lines = append(lines, "  IdentityAgent none", "  PreferredAuthentications password,keyboard-interactive", "  PubkeyAuthentication no")
			s.Env = append(s.Env, SSHPassVariable+"="+sec.Password, "SSH_ASKPASS_REQUIRE=force")
			s.Askpass = true
		}
		s.Files = append(s.Files, ActionFile{Path: ".ssh/config", Mode: 0o600, Text: strings.Join(lines, "\n") + "\n"})
	}
	return s, secrets, nil
}

func actionList(target string) EgressList {
	l := EgressList{Allow: []string{}, Ports: []string{}, Expires: map[string]time.Time{}}
	if portEntry(target) {
		l.Ports = []string{target}
	} else {
		l.Allow = []string{target}
	}
	return l
}

type runEnd struct {
	state  string
	code   int
	reason string
	output string
	cut    bool
}

func (g *Grants) execute(ctx context.Context, run ActionRun, a Action, c Credential, t GrantTarget, live *actionLive) {
	end := g.attempt(ctx, run, a, c, t, live)
	cfg := g.cfg.Actions
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), actionRemoveFor)
	defer cancel()
	if err := cfg.Host.RemoveAction(rctx, run.ID); err != nil {
		shared.For(ctx, g.cfg.Logger).Warn("actions", "event", "container-not-removed", "run", run.ID, "fix", "the engine's sweep removes it within 5 minutes")
	}
	cfg.Net.CloseAction(rctx, run.ID)
	if err := cfg.Files.Remove(run.ID); err != nil {
		shared.For(ctx, g.cfg.Logger).Warn("actions", "event", "files-not-removed", "run", run.ID)
	}
	g.finish(ctx, run.ID, end, t)
}

func runStopped(live *actionLive) (runEnd, bool) {
	select {
	case <-live.stop:
		return runEnd{state: ActionStopped, code: -1, reason: live.why}, true
	default:
	}
	return runEnd{}, false
}

func (g *Grants) attempt(ctx context.Context, run ActionRun, a Action, c Credential, t GrantTarget, live *actionLive) runEnd {
	cfg := g.cfg.Actions
	spec, secrets, err := g.spec(ctx, run, a, c, t)
	if err != nil {
		return runEnd{state: RunError, code: -1, reason: errorWords(err)}
	}
	if err := cfg.Files.Write(run.ID, spec); err != nil {
		return runEnd{state: RunError, code: -1, reason: "The engine couldn't write the run's files: " + cleanReason(err.Error())}
	}
	if end, ok := runStopped(live); ok {
		return end
	}
	box := ""
	if a.Files {
		box = t.Box
	}
	sctx, cancel := context.WithTimeout(ctx, actionStartFor)
	info, err := cfg.Host.MakeAction(sctx, run.ID, box)
	cancel()
	if err != nil {
		return runEnd{state: RunError, code: -1, reason: "The box helper didn't make the run's container: " + cleanReason(errText(err))}
	}
	if info.Run != run.ID || !info.Subnet.IsValid() {
		return runEnd{state: RunError, code: -1, reason: "The box helper's answer doesn't describe the run the engine asked for."}
	}
	octx, cancel := context.WithTimeout(ctx, actionCallFor)
	err = cfg.Net.OpenAction(octx, run.ID, info.Subnet, actionList(a.Target))
	cancel()
	if err != nil {
		return runEnd{state: RunError, code: -1, reason: "Egress didn't take the run's network, so it didn't start: " + cleanReason(err.Error())}
	}
	if end, ok := runStopped(live); ok {
		return end
	}
	sctx, cancel = context.WithTimeout(ctx, actionStartFor)
	err = cfg.Host.StartAction(sctx, run.ID)
	cancel()
	if err != nil {
		return runEnd{state: RunError, code: -1, reason: "The box helper didn't start the run's container: " + cleanReason(errText(err))}
	}
	if end, ok := g.withdrawSpec(ctx, run, live, secrets); !ok {
		return end
	}
	deadline := g.cfg.Now().Add(run.Limit + actionSlack)
	var st ActionState
	fails := 0
	for {
		timer := time.NewTimer(actionPoll)
		select {
		case <-live.stop:
			timer.Stop()
			end, _ := runStopped(live)
			return g.withOutput(ctx, run.ID, end, secrets)
		case <-ctx.Done():
			timer.Stop()
			return runEnd{state: ActionStopped, code: -1, reason: "the engine stopped"}
		case <-timer.C:
		}
		cctx, cancel := context.WithTimeout(ctx, actionCallFor)
		got, err := cfg.Host.ActionState(cctx, run.ID)
		cancel()
		if err != nil {
			if fails++; fails >= actionStateTries {
				return runEnd{state: RunError, code: -1, reason: "The box helper stopped answering about the run: " + cleanReason(errText(err))}
			}
		} else {
			fails, st = 0, got
			if !containerActive(got.State) {
				break
			}
		}
		if g.cfg.Now().After(deadline) || !st.StartedAt.IsZero() && g.cfg.Now().Sub(st.StartedAt) > run.Limit {
			return g.withOutput(ctx, run.ID, runEnd{state: RunTimedOut, code: -1, reason: "It didn't finish within " + minutes(run.Limit) + "."}, secrets)
		}
	}
	var end runEnd
	switch {
	case st.Run != run.ID || st.State != "exited":
		end = runEnd{state: RunError, code: -1, reason: "The container ended as " + clip(oneWord(st.State), 32) + ", not exited."}
	case st.OOMKilled:
		end = runEnd{state: RunFailed, code: st.ExitCode, reason: "It ran out of memory."}
	case st.Error != "":
		end = runEnd{state: RunError, code: st.ExitCode, reason: "Docker reported an error: " + cleanReason(st.Error)}
	case st.ExitCode != 0:
		end = runEnd{state: RunFailed, code: st.ExitCode}
	default:
		end = runEnd{state: RunPassed}
	}
	return g.withOutput(ctx, run.ID, end, secrets)
}

func (g *Grants) withdrawSpec(ctx context.Context, run ActionRun, live *actionLive, secrets []string) (runEnd, bool) {
	cfg := g.cfg.Actions
	deadline := g.cfg.Now().Add(actionReadFor)
	for {
		timer := time.NewTimer(actionReadPoll)
		select {
		case <-live.stop:
			timer.Stop()
			end, _ := runStopped(live)
			return g.withOutput(ctx, run.ID, end, secrets), false
		case <-ctx.Done():
			timer.Stop()
			return runEnd{state: ActionStopped, code: -1, reason: "the engine stopped"}, false
		case <-timer.C:
		}
		cctx, cancel := context.WithTimeout(ctx, actionCallFor)
		text, _, lerr := cfg.Host.ActionLog(cctx, run.ID)
		cancel()
		if lerr == nil && strings.HasPrefix(text, ActionReadMark+"\n") {
			if err := cfg.Files.Withdraw(run.ID); err != nil {
				return runEnd{state: RunError, code: -1, reason: "The engine couldn't take the run's command and secret back from its files, so the command didn't start: " + cleanReason(err.Error())}, false
			}
			return runEnd{}, true
		}
		cctx, cancel = context.WithTimeout(ctx, actionCallFor)
		st, serr := cfg.Host.ActionState(cctx, run.ID)
		cancel()
		if serr == nil && !containerActive(st.State) {
			return runEnd{}, true
		}
		if g.cfg.Now().After(deadline) {
			return g.withOutput(ctx, run.ID, runEnd{state: RunError, code: -1, reason: "The run's container didn't read its command within " + minutes(actionReadFor) + "."}, secrets), false
		}
	}
}

func (g *Grants) withOutput(ctx context.Context, run string, end runEnd, secrets []string) runEnd {
	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), actionCallFor)
	text, cut, err := g.cfg.Actions.Host.ActionLog(lctx, run)
	cancel()
	if err != nil {
		if end.reason == "" {
			end.reason = "Its output couldn't be read."
		}
		return end
	}
	if !cut {
		text = strings.TrimPrefix(text, ActionReadMark+"\n")
	}
	end.output, end.cut = ScrubOutput(text, secrets, cut)
	return end
}

func ScrubOutput(text string, secrets []string, cut bool) (string, bool) {
	text = strings.ToValidUTF8(text, "�")
	if cut {
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		}
	}
	text = strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' && r != '\t' || r == 0x7f {
			return ' '
		}
		return r
	}, ansi.ReplaceAllString(text, ""))
	for _, s := range secrets {
		for line := range strings.SplitSeq(s, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.Contains(s, "\n") && len(line) < actionSecretLeast {
				continue
			}
			text = strings.ReplaceAll(text, line, actionWithheld)
		}
	}
	text = maskLines(text, true)
	if len(text) > MaxActionOutput {
		text, cut = lastBytes(text, MaxActionOutput), true
	}
	return strings.TrimRight(text, "\n "), cut
}

func (g *Grants) finish(ctx context.Context, id string, end runEnd, t GrantTarget) {
	now := g.cfg.Now()
	g.mu.Lock()
	delete(g.liveRuns, id)
	i := slices.IndexFunc(g.runs, func(r ActionRun) bool { return r.ID == id })
	if i < 0 {
		g.mu.Unlock()
		return
	}
	r := &g.runs[i]
	r.State, r.Code, r.Reason, r.Output, r.Cut, r.Ended = end.state, end.code, end.reason, end.output, end.cut, now
	done := *r
	g.pruneRunsLocked()
	g.mu.Unlock()
	outcome := done.State
	if done.State == RunPassed || done.State == RunFailed {
		outcome = "exit code " + strconv.Itoa(done.Code)
	}
	reason := done.Name + " ran " + done.Command + " against " + done.Target + " using " + done.Credential + ", allowed by " + done.Allowed + "; " + outcome
	if done.State == ActionStopped || done.State == RunError || done.State == RunTimedOut {
		reason += " (" + clip(done.Reason, 200) + ")"
	}
	g.auditAction(ctx, t, "ran", ByEngine, reason)
	shared.For(ctx, g.cfg.Logger).Info("actions", "event", "ended", "run", id, "state", done.State, "code", done.Code)
	if done.By == AskedByAgent {
		g.cfg.Note(done.SID, agentRunNote(done))
	}
}

func agentRunNote(r ActionRun) string {
	head := runWords(r) + " "
	tail := " Call factory_action with run " + r.ID + " for all of its output that Software Factory kept."
	switch {
	case r.Output == "":
		return head + "It printed nothing."
	case len(r.Output) <= actionNoteRoom:
		return head + "Its output:\n\n" + r.Output
	}
	return head + "The end of its output:\n\n…" + lastBytes(r.Output, actionNoteRoom) + "\n\n" + tail
}

func (g *Grants) pruneRunsLocked() {
	count := map[string]int{}
	keep := make([]ActionRun, 0, len(g.runs))
	for _, r := range slices.Backward(g.runs) {
		_, live := g.liveRuns[r.ID]
		if !live && (count[r.SID] >= MaxSessionRuns || len(keep) >= MaxKeptRuns) {
			continue
		}
		count[r.SID]++
		keep = append(keep, r)
	}
	slices.Reverse(keep)
	g.runs = keep
}

func (g *Grants) auditAction(ctx context.Context, t GrantTarget, result, by, reason string) {
	if g.cfg.Audit == nil {
		return
	}
	e := AuditEntry{Time: g.cfg.Now(), Kind: AuditAction, By: by, Result: result, Reason: reason, Session: t.SID, Repository: t.Project, Branch: t.Branch, Box: t.Box, Req: shared.RequestOf(ctx)}
	if err := g.cfg.Audit.Append(e); err != nil {
		shared.For(ctx, g.cfg.Logger).Warn("actions", "event", "audit-not-written")
	}
}

func (g *Grants) endActionWork(ctx context.Context, match func(actionKey) bool, why string) int {
	g.mu.Lock()
	var taken []actionGrant
	g.actionGrants = slices.DeleteFunc(g.actionGrants, func(x actionGrant) bool {
		if match(actionKey{sid: x.SID, action: x.Action, credential: x.Credential, grant: x.ID}) {
			taken = append(taken, x)
			return true
		}
		return false
	})
	var stop []*actionLive
	for _, r := range g.runs {
		if l, live := g.liveRuns[r.ID]; live && match(actionKey{sid: r.SID, action: r.Action, credential: r.CredID, grant: r.Grant}) {
			stop = append(stop, l)
		}
	}
	shown := map[string]bool{}
	for sid, list := range g.cards {
		next := slices.DeleteFunc(slices.Clone(list), func(c *accessCard) bool {
			if c.kind != AccessAction {
				return false
			}
			a, exists := g.actionByID(c.action)
			return !exists || match(actionKey{sid: sid, action: c.action, credential: a.Credential})
		})
		if len(next) != len(list) {
			shown[sid] = true
			if len(next) == 0 {
				delete(g.cards, sid)
			} else {
				g.cards[sid] = next
			}
		}
	}
	g.mu.Unlock()
	for _, l := range stop {
		l.end(why)
	}
	for _, x := range taken {
		t, _ := g.cfg.Session(x.SID)
		if t.SID == "" {
			t = GrantTarget{SID: x.SID, Project: x.Project}
		}
		g.auditAction(ctx, t, ResultGrantEnded, ByEngine, x.Name+" with "+QuotedCommand(x.Argv)+", which ended because "+why)
	}
	for sid := range shown {
		g.show(sid)
	}
	return len(taken) + len(stop)
}

func (g *Grants) tendActions(ctx context.Context, targets map[string]GrantTarget, now time.Time) {
	g.mu.Lock()
	var expired []actionGrant
	g.actionGrants = slices.DeleteFunc(g.actionGrants, func(x actionGrant) bool {
		if !now.Before(x.Expires) {
			expired = append(expired, x)
			return true
		}
		return false
	})
	var stop []*actionLive
	var ended []string
	for _, r := range g.runs {
		l, live := g.liveRuns[r.ID]
		if !live {
			continue
		}
		switch {
		case !targets[r.SID].Live:
			stop, ended = append(stop, l), append(ended, "the session stopped")
		case r.Grant != "" && slices.ContainsFunc(expired, func(x actionGrant) bool { return x.ID == r.Grant }):
			stop, ended = append(stop, l), append(ended, "its grant expired")
		}
	}
	var gone []actionGrant
	g.actionGrants = slices.DeleteFunc(g.actionGrants, func(x actionGrant) bool {
		if !targets[x.SID].Live {
			gone = append(gone, x)
			return true
		}
		return false
	})
	g.mu.Unlock()
	for i, l := range stop {
		l.end(ended[i])
	}
	for _, x := range expired {
		g.auditAction(ctx, targets[x.SID], ResultExpired, ByEngine, x.Name+" with "+QuotedCommand(x.Argv)+", which expired")
	}
	for _, x := range gone {
		g.auditAction(ctx, targets[x.SID], ResultGrantEnded, ByEngine, x.Name+" with "+QuotedCommand(x.Argv)+", which ended when the session stopped")
	}
}

func (g *Grants) takeBackActions(ctx context.Context, sid string, pick func(actionGrant) bool) ([]actionGrant, int) {
	g.mu.Lock()
	var taken []actionGrant
	g.actionGrants = slices.DeleteFunc(g.actionGrants, func(x actionGrant) bool {
		if x.SID == sid && pick(x) {
			taken = append(taken, x)
			return true
		}
		return false
	})
	var stop []*actionLive
	for _, r := range g.runs {
		if l, live := g.liveRuns[r.ID]; live && r.Grant != "" && slices.ContainsFunc(taken, func(x actionGrant) bool { return x.ID == r.Grant }) {
			stop = append(stop, l)
		}
	}
	g.mu.Unlock()
	for _, l := range stop {
		l.end("you took its grant back")
	}
	t, _ := g.cfg.Session(sid)
	for _, x := range taken {
		g.auditAction(ctx, t, ResultTakenBack, ByPage, x.Name+" with "+QuotedCommand(x.Argv))
	}
	return taken, len(stop)
}

func (g *Grants) actionViewFor(t GrantTarget, now time.Time) ([]ActionGrantView, []ActionRunView) {
	grants := []ActionGrantView{}
	for _, x := range g.actionGrants {
		if x.SID == t.SID && now.Before(x.Expires) {
			grants = append(grants, ActionGrantView{ID: x.ID, Name: x.Name, Command: QuotedCommand(x.Argv), Until: x.Expires})
		}
	}
	runs := []ActionRunView{}
	for _, r := range slices.Backward(g.runs) {
		if r.SID != t.SID {
			continue
		}
		_, live := g.liveRuns[r.ID]
		v := ActionRunView{ActionRun: r, Running: live}
		if !live {
			v.Said = runWords(r)
		}
		runs = append(runs, v)
	}
	return grants, runs
}

func (g *Grants) SweepActions(ctx context.Context) {
	cfg := g.cfg.Actions
	if cfg.Host == nil {
		return
	}
	list, err := cfg.Host.ListActions(ctx)
	if err != nil {
		return
	}
	for _, c := range list {
		g.mu.Lock()
		_, ours := g.liveRuns[c.Box]
		g.mu.Unlock()
		if ours {
			continue
		}
		if err := cfg.Host.RemoveAction(ctx, c.Box); err != nil {
			g.cfg.Logger.Warn("actions", "event", "leftover-not-removed", "run", c.Box)
			continue
		}
		cfg.Net.CloseAction(ctx, c.Box)
		g.cfg.Logger.Info("actions", "event", "leftover-removed", "run", c.Box, "state", c.State)
	}
	if cfg.Files != nil {
		if err := cfg.Files.Prune(func(run string) bool {
			g.mu.Lock()
			defer g.mu.Unlock()
			_, ok := g.liveRuns[run]
			return ok
		}); err != nil {
			g.cfg.Logger.Warn("actions", "event", "files-not-pruned")
		}
	}
}

func (g *Grants) WatchActions(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		sctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		g.SweepActions(sctx)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
