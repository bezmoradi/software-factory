package session

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	LoginReviewers = "reviewers"
	boxTarget      = "box:"
	maxLoginCode   = 512
	minAPIKey      = 20
	maxAPIKey      = 512
	loginFor       = 2 * time.Minute
)

type LoginHost interface {
	Login(ctx context.Context, c Command, step, code string) (string, error)
}

type KeyHost interface {
	APIKey(ctx context.Context, box, step, key string) error
}

type LoginState struct {
	Box             string
	Agents          *SignIn
	Reviewers       *SignIn
	AgentsExpect    Expect
	ReviewersExpect Expect
	Each            []AgentLogin
	ReviewersBusy   bool
}

const StepWait = 10 * time.Second

type stepWaitKey struct{}

func WithStepWait(ctx context.Context, wait time.Duration) context.Context {
	return context.WithValue(ctx, stepWaitKey{}, time.Now().Add(wait))
}

func StepDeadline(ctx context.Context) (time.Time, bool) {
	t, ok := ctx.Value(stepWaitKey{}).(time.Time)
	return t, ok
}

type keyLock struct {
	once sync.Once
	ch   chan struct{}
}

func (l *keyLock) c() chan struct{} {
	l.once.Do(func() { l.ch = make(chan struct{}, 1) })
	return l.ch
}

func (l *keyLock) Lock()   { l.c() <- struct{}{} }
func (l *keyLock) Unlock() { <-l.c() }

func (l *keyLock) lockBy(ctx context.Context) bool {
	select {
	case l.c() <- struct{}{}:
		return true
	default:
	}
	d, ok := StepDeadline(ctx)
	if !ok {
		l.Lock()
		return true
	}
	t := time.NewTimer(time.Until(d))
	defer t.Stop()
	select {
	case l.c() <- struct{}{}:
		return true
	case <-t.C:
	case <-ctx.Done():
	}
	return false
}

func keyStepBusy() *Error {
	return explained(limited("reviews"), "Another sign-in or key step for the reviewers is still running, so this one didn't start. Try again in a moment.")
}

func stepCtx(ctx context.Context, target string) context.Context {
	if target != LoginReviewers {
		return ctx
	}
	if _, set := StepDeadline(ctx); set {
		return ctx
	}
	return WithStepWait(ctx, StepWait)
}

type AgentLogin struct {
	ID     string
	Name   string
	Step   string
	Code   bool
	SignIn *SignIn
}

func BoxTarget(box string) string { return boxTarget + box }

func TargetBox(target string) (string, bool) {
	box, ok := strings.CutPrefix(target, boxTarget)
	if !ok || !ValidBox(box) {
		return "", false
	}
	return box, true
}

func ValidLoginTarget(target string) bool {
	_, ok := TargetBox(target)
	return ok || target == LoginReviewers
}

const loginCodeShape = "Paste the code exactly as claude.com shows it."

func ValidLoginCode(code string) bool {
	if code == "" || len(code) > maxLoginCode {
		return false
	}
	for _, r := range code {
		if r <= ' ' || r > '~' {
			return false
		}
	}
	return true
}

func validLoginLink(link string) bool {
	u, err := url.Parse(link)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" {
		return false
	}
	switch u.Hostname() {
	case "claude.com", "claude.ai":
	default:
		return false
	}
	return strings.HasSuffix(u.Path, "/oauth/authorize")
}

func loginStep(ctx context.Context, host LoginHost, cmd Command, step, code string) (string, error) {
	if host == nil {
		return "", explained(conflict("login"), "This engine can't reach that container, so it can't sign Claude Code in there.")
	}
	switch step {
	case "start", "cancel":
	case "code":
		if !ValidLoginCode(code) {
			return "", explained(invalid("code"), loginCodeShape)
		}
	default:
		return "", explained(invalid("step"), "")
	}
	lctx, cancel := context.WithTimeout(ctx, loginFor)
	defer cancel()
	link, err := host.Login(lctx, cmd, step, code)
	if busy := reviewersBusy(err); busy != nil {
		return "", busy
	}
	if err != nil {
		return "", explained(conflict("login"), hostMessage(err))
	}
	if step == "start" && !validLoginLink(link) {
		_, _ = host.Login(lctx, cmd, "cancel", "")
		return "", explained(conflict("login"), "Claude Code's login showed a link that isn't a claude.com or claude.ai sign-in, so the page doesn't open it.")
	}
	return link, nil
}

type boxLogin struct {
	box  string
	spec AgentSpec
	host Host
	dir  string
}

func (d *Dispatcher) recordedBox(box string) (BoxRecord, error) {
	if !ValidBox(box) {
		return BoxRecord{}, explained(invalid("target"), "")
	}
	list, err := d.loadBoxes()
	if err != nil {
		return BoxRecord{}, explained(conflict("state"), "The engine couldn't read its record of the sessions' containers.")
	}
	i := slices.IndexFunc(list, func(b BoxRecord) bool { return b.Box == box })
	if i < 0 || d.cfg.BoxStore == nil {
		return BoxRecord{}, explained(notFound("box"), "That session's container isn't one this engine records.")
	}
	return list[i], nil
}

func (d *Dispatcher) boxLogin(ctx context.Context, target string) (boxLogin, error) {
	b, err := d.boxKeyLogin(ctx, target)
	if err == nil && d.homeAsking(b.box) {
		return boxLogin{}, explained(conflict("settings question"), "The session's wake is checking or waiting for your answer about the agent's changed settings, and Claude Code would load them, so the engine runs nothing in the container until you answer.")
	}
	return b, err
}

func (d *Dispatcher) boxKeyLogin(ctx context.Context, target string) (boxLogin, error) {
	box, ok := TargetBox(target)
	if !ok {
		return boxLogin{}, explained(invalid("target"), "")
	}
	rec, err := d.recordedBox(box)
	if err != nil {
		return boxLogin{}, err
	}
	spec, ok := findAgent(d.cfg.Agents, rec.Agent)
	if !ok {
		return boxLogin{}, explained(conflict("agent"), "That session's container runs an agent this engine doesn't know.")
	}
	if !d.liveBox(box) {
		return boxLogin{}, explained(conflict("box stopped"), "The session's container isn't running. Start or wake the session again to sign it in.")
	}
	if err := d.ConfirmBox(ctx, box); err != nil {
		return boxLogin{}, err
	}
	return boxLogin{box: box, spec: spec, host: d.hostFor(box), dir: BoxFolder(d.cfg.Workspace, box)}, nil
}

func (d *Dispatcher) Login(ctx context.Context, target, step, code string) (string, *SignIn, error) {
	if step == "code" && !ValidLoginCode(code) {
		return "", nil, explained(invalid("code"), loginCodeShape)
	}
	var cmd Command
	var host LoginHost
	switch target {
	case LoginReviewers:
		if d.cfg.Reviews == nil {
			return "", nil, explained(conflict("login"), "This engine runs no reviewer containers.")
		}
		ctx = stepCtx(ctx, target)
		d.cfg.Reviews.signInChanging()
		h, ok := d.cfg.Reviews.cfg.Host.(LoginHost)
		if !ok {
			return "", nil, explained(conflict("login"), "This engine can't sign Claude Code in for the reviewers.")
		}
		cmd = d.cfg.Reviews.loginCommand()
		host = h
	default:
		b, err := d.boxLogin(ctx, target)
		if err != nil {
			return "", nil, err
		}
		h, isLogin := b.host.(LoginHost)
		if !b.spec.codeSignIn() {
			return "", nil, explained(conflict("login"), b.spec.Name+" signs in with its own command in the session's container: "+b.spec.StepIn(b.box)+".")
		}
		if !isLogin {
			return "", nil, explained(conflict("login"), "This engine can't sign Claude Code in to the session's container.")
		}
		cmd = Command{Argv: []string{b.spec.Command[0], "auth", "login"}, Dir: b.dir, Pass: b.spec.Pass, Timeout: loginFor}
		host = h
	}
	link, err := loginStep(ctx, host, cmd, step, code)
	shared.For(ctx, d.cfg.Logger).Info("claude login", "target", target, "step", step, "ok", err == nil)
	if target == LoginReviewers && step == "code" && !isBusy(err) {
		d.cfg.Reviews.SignInChanged()
	}
	if err != nil || step != "code" {
		return link, nil, err
	}
	cctx := ctx
	if target == LoginReviewers {
		cctx = WithStepWait(ctx, signInFor)
	}
	if !d.keyMu.lockBy(cctx) {
		return "", nil, signedInKeyKept()
	}
	err = d.removeKeyLocked(cctx, target, false)
	d.keyMu.Unlock()
	if isBusy(err) {
		return "", nil, signedInKeyKept()
	}
	if err != nil {
		return "", nil, err
	}
	return "", d.finished(ctx, target, ExpectSubscription), nil
}

func isBusy(err error) bool {
	e, ok := errors.AsType[*Error](err)
	return ok && e.Kind == Limited && e.Reason == "reviews"
}

func signedInKeyKept() *Error {
	return explained(limited("reviews"), "Claude Code signed the reviewers in, but every reviewer container stayed busy, so the engine couldn't remove the API key saved before, which Claude Code still prefers. Press Remove key once a review finishes.")
}

func ValidAPIKey(key string) bool {
	if len(key) < minAPIKey || len(key) > maxAPIKey {
		return false
	}
	for _, r := range key {
		if r <= ' ' || r > '~' || r == '\'' {
			return false
		}
	}
	return true
}

func (d *Dispatcher) KeyLogin(ctx context.Context, target, key string) (*SignIn, error) {
	if !ValidLoginTarget(target) {
		return nil, explained(invalid("target"), "")
	}
	if !ValidAPIKey(key) {
		return nil, explained(invalid("key"), "Paste the key exactly as the Claude Console or your gateway's team gives it: 20 to 512 characters, with no spaces or quotes.")
	}
	ctx = stepCtx(ctx, target)
	err := d.sendKey(ctx, target, key)
	shared.For(ctx, d.cfg.Logger).Info("claude api key", "target", target, "ok", err == nil)
	if err != nil {
		return nil, err
	}
	return d.finished(ctx, target, ExpectAPIKey), nil
}

func (d *Dispatcher) sendKey(ctx context.Context, target, key string) error {
	if _, err := d.keyHost(ctx, target); err != nil {
		return err
	}
	side := targetSide(target)
	release := d.holdGateway()
	defer release()
	if !d.keyMu.lockBy(ctx) {
		return keyStepBusy()
	}
	defer d.keyMu.Unlock()
	host, established := d.gatewayOf(side)
	if !established {
		return explained(conflict("gateway"), "The engine couldn't read egress.json, so it can't tell whether Claude Code in the "+d.containerWords(target)+" would send this key to a gateway or to Anthropic, and it doesn't save the key. Fix or move egress.json and restart the engine first.")
	}
	all, readable := d.keyRoutes()
	before := d.cfg.Expected.Of(d.expectAgentOf(target))
	if k, ok := all[target]; readable && ok && k.Saved {
		before = k.Before
	}
	if err := d.saveKeyRoute(target, KeyRoute{Saved: true, Gateway: host, Before: before}); err != nil {
		return explained(conflict("state"), "The engine couldn't record where the key goes, so it didn't save the key.")
	}
	return d.apiKey(ctx, target, "set", key)
}

func (d *Dispatcher) ClearKey(ctx context.Context, target string) error {
	if _, err := d.keyHost(ctx, target); err != nil {
		return err
	}
	ctx = stepCtx(ctx, target)
	if !d.keyMu.lockBy(ctx) {
		return keyStepBusy()
	}
	err := d.removeKeyLocked(ctx, target, true)
	d.keyMu.Unlock()
	shared.For(ctx, d.cfg.Logger).Info("claude api key", "target", target, "step", "clear", "ok", err == nil)
	return err
}

func (d *Dispatcher) ClearSide(ctx context.Context, side string) error {
	if side == SideReviewers {
		if _, err := d.keyHost(ctx, LoginReviewers); err != nil {
			return nil
		}
		return d.ClearKey(ctx, LoginReviewers)
	}
	all, _ := d.keyRoutes()
	var targets []string
	for t, k := range all {
		if box, ok := TargetBox(t); ok && k.Saved && d.liveBox(box) {
			targets = append(targets, t)
		}
	}
	slices.Sort(targets)
	for _, t := range targets {
		box, _ := TargetBox(t)
		if err := d.ConfirmBox(ctx, box); err != nil {
			if d.liveBox(box) {
				return err
			}
			continue
		}
		if err := d.ClearKey(ctx, t); err != nil {
			return err
		}
	}
	return nil
}

func (d *Dispatcher) keyHost(ctx context.Context, target string) (KeyHost, error) {
	if target == LoginReviewers {
		if d.cfg.Reviews != nil {
			if host, ok := d.cfg.Reviews.cfg.Host.(KeyHost); ok {
				return host, nil
			}
		}
		return nil, explained(conflict("login"), "This engine can't start a reviewer container, so it can't change the reviewers' API key.")
	}
	b, err := d.boxKeyLogin(ctx, target)
	if err != nil {
		return nil, err
	}
	host, ok := b.host.(KeyHost)
	if !ok || !b.spec.codeSignIn() {
		return nil, explained(conflict("login"), b.spec.Name+" in the session's container doesn't take an API key from the page.")
	}
	return host, nil
}

func (d *Dispatcher) apiKey(ctx context.Context, target, step, key string) error {
	host, err := d.keyHost(ctx, target)
	if err != nil {
		return err
	}
	reviewers := target == LoginReviewers && d.cfg.Reviews != nil
	if reviewers {
		d.cfg.Reviews.signInChanging()
	}
	kctx, cancel := context.WithTimeout(ctx, signInFor)
	defer cancel()
	box, isBox := TargetBox(target)
	if isBox {
		err = d.homeAfterKey(kctx, box, func() error { return host.APIKey(kctx, box, step, key) })
	} else {
		err = host.APIKey(kctx, box, step, key)
	}
	busy := reviewersBusy(err)
	if reviewers && busy == nil {
		d.cfg.Reviews.SignInChanged()
	}
	if busy != nil {
		return busy
	}
	if err != nil {
		return explained(conflict("login"), hostMessage(err))
	}
	return nil
}

func (d *Dispatcher) reviewersChecked(ctx context.Context) *SignIn {
	r := d.cfg.Reviews
	base := shared.WithRequest(r.cfg.Base, shared.RequestOf(ctx))
	cctx, cancel := context.WithTimeout(WithStepWait(base, signInFor), signInFor)
	got := make(chan *SignIn, 1)
	r.wg.Go(func() {
		defer cancel()
		if s, err := r.CheckSignIn(cctx); err == nil {
			got <- &s
			return
		}
		got <- nil
	})
	t := time.NewTimer(StepWait)
	defer t.Stop()
	select {
	case s := <-got:
		return s
	case <-t.C:
		return nil
	}
}

func (d *Dispatcher) finished(ctx context.Context, target string, kind Expect) *SignIn {
	if target == LoginReviewers {
		_ = d.cfg.Expected.Set(ReviewerAgent, kind)
		return d.reviewersChecked(ctx)
	}
	b, err := d.boxLogin(ctx, target)
	if err != nil {
		return nil
	}
	_ = d.cfg.Expected.Set(BoxTarget(b.box), kind)
	s, _ := d.boxSignIn(ctx, b)
	return s
}

func (d *Dispatcher) LoginState(ctx context.Context, target string) (LoginState, error) {
	out := LoginState{ReviewersExpect: d.cfg.Expected.Of(ReviewerAgent), AgentsExpect: ExpectAny}
	if target != "" {
		b, err := d.boxLogin(ctx, target)
		if err != nil {
			return LoginState{}, err
		}
		spec := d.installed(b.spec)
		s, _ := d.boxSignIn(ctx, b)
		out.Box = b.box
		var method SignInMethod
		if s != nil {
			method = s.Method
		}
		out.AgentsExpect = d.expectFor(spec, b.box, method)
		if s != nil && spec.codeSignIn() {
			out.Agents = s
		}
		out.Each = []AgentLogin{{ID: spec.ID, Name: spec.Name, Step: spec.StepIn(b.box), Code: spec.codeSignIn(), SignIn: s}}
		return out, nil
	}
	if d.cfg.Reviews != nil {
		s, err := d.cfg.Reviews.KnownSignIn(ctx)
		out.ReviewersBusy = isBusy(err) || s.Busy
		if err == nil {
			out.Reviewers = &s
			if !s.Busy {
				out.ReviewersExpect = d.cfg.Expected.AdoptChecked(ReviewerAgent, s)
			}
		}
	}
	for _, a := range d.cfg.Agents {
		a = d.installed(a)
		if !a.Enabled || a.Status == nil || len(a.Command) == 0 {
			continue
		}
		out.Each = append(out.Each, AgentLogin{ID: a.ID, Name: a.Name, Step: a.StepIn(""), Code: a.codeSignIn()})
	}
	return out, nil
}

func (d *Dispatcher) expectFor(spec AgentSpec, box string, method SignInMethod) Expect {
	if x, ok := d.cfg.Expected.Lookup(BoxTarget(box)); ok && ValidBox(box) {
		return x
	}
	if spec.ID == "claude" && method != "" {
		return d.cfg.Expected.Adopt(spec.ID, method)
	}
	return d.cfg.Expected.Of(spec.ID)
}

func (d *Dispatcher) boxSignIn(ctx context.Context, b boxLogin) (*SignIn, error) {
	spec := b.spec
	checker, ok := b.host.(SignInChecker)
	if spec.Status == nil || len(spec.Command) == 0 || !ok {
		return nil, explained(conflict("sign-in check"), "")
	}
	sctx, cancel := context.WithTimeout(ctx, signInFor)
	defer cancel()
	s, err := checker.SignIn(sctx, Command{Argv: append([]string{spec.Command[0]}, spec.Status.Args...), Dir: b.dir, Pass: spec.Pass, Timeout: signInFor}, spec.Status.Kind)
	if err != nil {
		return nil, err
	}
	s.CheckedAt = d.reg.now()
	return &s, nil
}

func (r *Reviews) loginCommand() Command {
	return Command{Argv: []string{r.cfg.Command, "auth", "login"}, Dir: r.cfg.Dir, Pass: []string{"DISABLE_AUTOUPDATER"}, Timeout: loginFor}
}

type LoginPage interface {
	Trust(ctx context.Context, path, approve string) error
	Login(ctx context.Context, target, step, code string) (string, *SignIn, error)
	KeyLogin(ctx context.Context, target, key string) (*SignIn, error)
	ClearKey(ctx context.Context, target string) error
	LoginState(ctx context.Context, target string) (LoginState, error)
}

var _ LoginPage = (*Dispatcher)(nil)
