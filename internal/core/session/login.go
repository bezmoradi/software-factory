package session

import (
	"context"
	"net/url"
	"strings"
	"time"
)

const (
	LoginAgents    = "agents"
	LoginReviewers = "reviewers"
	maxLoginCode   = 512
	minAPIKey      = 20
	maxAPIKey      = 512
	loginFor       = 2 * time.Minute
)

type LoginHost interface {
	Login(ctx context.Context, c Command, step, code string) (string, error)
}

type KeyHost interface {
	APIKey(ctx context.Context, step, key string) error
}

type LoginState struct {
	Agents          *SignIn
	Reviewers       *SignIn
	AgentsExpect    Expect
	ReviewersExpect Expect
	Each            []AgentLogin
}

type AgentLogin struct {
	ID     string
	Name   string
	Step   string
	SignIn *SignIn
}

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
			return "", explained(invalid("code"), "Paste the code exactly as claude.com shows it.")
		}
	default:
		return "", explained(invalid("step"), "")
	}
	lctx, cancel := context.WithTimeout(ctx, loginFor)
	defer cancel()
	link, err := host.Login(lctx, cmd, step, code)
	if err != nil {
		return "", explained(conflict("login"), hostMessage(err))
	}
	if step == "start" && !validLoginLink(link) {
		_, _ = host.Login(lctx, cmd, "cancel", "")
		return "", explained(conflict("login"), "Claude Code's login showed a link that isn't a claude.com or claude.ai sign-in, so the page doesn't open it.")
	}
	return link, nil
}

func (d *Dispatcher) claudeSpec() (AgentSpec, bool) {
	spec, ok := findAgent(d.cfg.Agents, "claude")
	return spec, ok && spec.Enabled && len(spec.Command) > 0
}

func (d *Dispatcher) Login(ctx context.Context, target, step, code string) (string, *SignIn, error) {
	var cmd Command
	var host LoginHost
	switch target {
	case LoginAgents:
		spec, ok := d.claudeSpec()
		h, isLogin := d.cfg.Host.(LoginHost)
		if !ok || !isLogin || d.cfg.Workspace == "" {
			return "", nil, explained(conflict("login"), "This engine can't sign Claude Code in to the agent container.")
		}
		cmd = Command{Argv: []string{spec.Command[0], "auth", "login"}, Dir: d.cfg.Workspace, Pass: spec.Pass, Timeout: loginFor}
		host = h
	case LoginReviewers:
		if d.cfg.Reviews == nil {
			return "", nil, explained(conflict("login"), "This engine has no reviewer container.")
		}
		h, ok := d.cfg.Reviews.cfg.Host.(LoginHost)
		if !ok {
			return "", nil, explained(conflict("login"), "This engine can't sign Claude Code in to the reviewer container.")
		}
		cmd = d.cfg.Reviews.loginCommand()
		host = h
	default:
		return "", nil, explained(invalid("target"), "")
	}
	link, err := loginStep(ctx, host, cmd, step, code)
	d.cfg.Logger.Info("claude login", "target", target, "step", step, "ok", err == nil)
	if err != nil || step != "code" {
		return link, nil, err
	}
	if err := d.apiKey(ctx, target, "clear", ""); err != nil {
		return "", nil, err
	}
	return "", d.finished(ctx, target, ExpectSubscription), nil
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
	if !ValidAPIKey(key) {
		return nil, explained(invalid("key"), "Paste the API key exactly as the Claude Console shows it, with no spaces.")
	}
	err := d.apiKey(ctx, target, "set", key)
	d.cfg.Logger.Info("claude api key", "target", target, "ok", err == nil)
	if err != nil {
		return nil, err
	}
	return d.finished(ctx, target, ExpectAPIKey), nil
}

func (d *Dispatcher) apiKey(ctx context.Context, target, step, key string) error {
	var host KeyHost
	var ok bool
	switch target {
	case LoginAgents:
		host, ok = d.cfg.Host.(KeyHost)
	case LoginReviewers:
		if d.cfg.Reviews != nil {
			host, ok = d.cfg.Reviews.cfg.Host.(KeyHost)
		}
	default:
		return explained(invalid("target"), "")
	}
	if !ok {
		return explained(conflict("login"), "This engine can't reach that container, so it can't change Claude Code's API key there.")
	}
	kctx, cancel := context.WithTimeout(ctx, signInFor)
	defer cancel()
	if err := host.APIKey(kctx, step, key); err != nil {
		return explained(conflict("login"), hostMessage(err))
	}
	return nil
}

func (d *Dispatcher) finished(ctx context.Context, target string, kind Expect) *SignIn {
	if target == LoginReviewers {
		_ = d.cfg.Expected.Set(ReviewerAgent, kind)
		if s, err := d.cfg.Reviews.CheckSignIn(ctx); err == nil {
			return &s
		}
		return nil
	}
	_ = d.cfg.Expected.Set("claude", kind)
	s, _ := d.agentSignIn(ctx)
	return s
}

func (d *Dispatcher) LoginState(ctx context.Context) LoginState {
	out := LoginState{AgentsExpect: d.cfg.Expected.Of("claude"), ReviewersExpect: d.cfg.Expected.Of(ReviewerAgent)}
	out.Agents, _ = d.agentSignIn(ctx)
	if out.Agents != nil {
		out.AgentsExpect = d.cfg.Expected.Adopt("claude", out.Agents.Method)
	}
	if d.cfg.Reviews != nil {
		if s, err := d.cfg.Reviews.CheckSignIn(ctx); err == nil {
			out.Reviewers = &s
			out.ReviewersExpect = d.cfg.Expected.Adopt(ReviewerAgent, s.Method)
		}
	}
	for _, a := range d.cfg.Agents {
		a = d.installed(a)
		if !a.Enabled || a.Status == nil || len(a.Command) == 0 {
			continue
		}
		l := AgentLogin{ID: a.ID, Name: a.Name, Step: a.Status.Step, SignIn: out.Agents}
		if a.ID != "claude" {
			l.SignIn, _ = d.specSignIn(ctx, a)
		}
		out.Each = append(out.Each, l)
	}
	return out
}

func (d *Dispatcher) agentSignIn(ctx context.Context) (*SignIn, error) {
	spec, ok := d.claudeSpec()
	if !ok {
		return nil, explained(conflict("sign-in check"), "")
	}
	return d.specSignIn(ctx, spec)
}

func (d *Dispatcher) specSignIn(ctx context.Context, spec AgentSpec) (*SignIn, error) {
	if spec.Status == nil || len(spec.Command) == 0 || d.cfg.SignIns == nil || d.cfg.Workspace == "" {
		return nil, explained(conflict("sign-in check"), "")
	}
	sctx, cancel := context.WithTimeout(ctx, signInFor)
	defer cancel()
	s, err := d.cfg.SignIns.SignIn(sctx, Command{Argv: append([]string{spec.Command[0]}, spec.Status.Args...), Dir: d.cfg.Workspace, Pass: spec.Pass, Timeout: signInFor}, spec.Status.Kind)
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
	Trust(ctx context.Context, path string) error
	Login(ctx context.Context, target, step, code string) (string, *SignIn, error)
	KeyLogin(ctx context.Context, target, key string) (*SignIn, error)
	LoginState(ctx context.Context) LoginState
}

var _ LoginPage = (*Dispatcher)(nil)
