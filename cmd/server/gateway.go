package server

import (
	"context"
	"maps"
	"path/filepath"
	"slices"

	"github.com/bezmoradi/software-factory/internal/adapters/agenthost"
	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	BaseURLVariable   = "ANTHROPIC_BASE_URL"
	NoTrafficVariable = "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"
)

type gatewayHost struct {
	*agenthost.Client
	url      func() string
	commands []string
}

type agentHost interface {
	session.Host
	session.SignInChecker
}

func withGateway(c *agenthost.Client, allow *session.Allowlist, side string, commands ...string) agentHost {
	if allow == nil {
		return c
	}
	return gatewayHost{Client: c, url: func() string { return allow.BaseURL(side) }, commands: append([]string{"claude"}, commands...)}
}

func (g gatewayHost) claudeCode(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	return slices.Contains(g.commands, argv[0]) || filepath.Base(argv[0]) == "claude"
}

func (g gatewayHost) with(c session.Command) session.Command {
	u := g.url()
	if u == "" || !g.claudeCode(c.Argv) {
		return c
	}
	env := maps.Clone(c.Env)
	if env == nil {
		env = map[string]string{}
	}
	env[BaseURLVariable] = u
	env[NoTrafficVariable] = "1"
	c.Env = env
	return c
}

func (g gatewayHost) Run(ctx context.Context, c session.Command) (session.RunResult, error) {
	return g.Client.Run(ctx, g.with(c))
}

func (g gatewayHost) RunStreamed(ctx context.Context, c session.Command, progress func(session.RunProgress)) (session.StreamedRun, error) {
	return g.Client.RunStreamed(ctx, g.with(c), progress)
}

func (g gatewayHost) Start(ctx context.Context, c session.Command) (session.Process, error) {
	return g.Client.Start(ctx, g.with(c))
}

func (g gatewayHost) SignIn(ctx context.Context, c session.Command, kind string) (session.SignIn, error) {
	return g.Client.SignIn(ctx, g.with(c), kind)
}
