package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"path/filepath"

	"github.com/bezmoradi/software-factory/internal/adapters/egress"
	"github.com/bezmoradi/software-factory/internal/adapters/sandbox/docker"
	"github.com/bezmoradi/software-factory/internal/core/session"
)

const DefaultOutsideHost = "example.com"

type SandboxConfig struct {
	On      bool
	Proxy   string
	Status  string
	Allow   []string
	Deny    []string
	Outside string
	Dir     string
}

func parseSandbox(cfg *PageConfig, getenv func(string) string) error {
	sb := SandboxConfig{Proxy: "egress:3128", Status: "egress-control:3129", Outside: DefaultOutsideHost, Dir: cfg.Workspace}
	switch v := getenv("SF_SANDBOX_CHECK"); v {
	case "", "on":
		sb.On = cfg.AgentHost != ""
	case "off":
	default:
		return fmt.Errorf("SF_SANDBOX_CHECK must be on or off, got %q", v)
	}
	for _, p := range []struct {
		key    string
		target *string
	}{{"SF_EGRESS_PROXY", &sb.Proxy}, {"SF_EGRESS_STATUS", &sb.Status}} {
		if v := getenv(p.key); v != "" {
			if _, _, err := net.SplitHostPort(v); err != nil {
				return fmt.Errorf("%s must be host:port, got %q", p.key, v)
			}
			*p.target = v
		}
	}
	if v := getenv("SF_SANDBOX_OUTSIDE"); v != "" {
		sb.Outside = v
	}
	if v := getenv("SF_AGENT_WORKSPACE"); v != "" {
		if !filepath.IsAbs(v) {
			return fmt.Errorf("SF_AGENT_WORKSPACE must be an absolute path in the agent container")
		}
		sb.Dir = filepath.Clean(v)
	}
	policy, err := egress.NewPolicy(list(getenv("EGRESS_ALLOW")), list(getenv("EGRESS_DENY")))
	if err != nil {
		return err
	}
	sb.Allow, sb.Deny = policy.Allow(), policy.Deny()
	if sb.On && cfg.AgentBinary == "" {
		return fmt.Errorf("the sandbox check runs %s sandbox-probe in the agent container, so SF_AGENT_BINARY can't be off while SF_SANDBOX_CHECK is on", "software-factory")
	}
	cfg.Sandbox = sb
	return nil
}

func gitHostsOf(repos *session.Repos) func() []string {
	if repos == nil {
		return nil
	}
	return repos.GitHosts
}

func sandboxChecker(cfg PageConfig, host session.Host, hosts func() []string) (session.Sandbox, error) {
	if !cfg.Sandbox.On {
		return nil, nil
	}
	sb := cfg.Sandbox
	return docker.New(docker.Config{
		Host: host, Probe: []string{cfg.AgentBinary, "sandbox-probe"}, Dir: sb.Dir,
		Proxy: sb.Proxy, Status: sb.Status, Allow: sb.Allow, Deny: sb.Deny, Outside: sb.Outside, Repos: hosts,
	})
}

func ServeProbe(ctx context.Context, args []string, out io.Writer) error {
	cfg, err := docker.ParseProbeArgs(args)
	if err != nil {
		return err
	}
	rep := docker.Probe(ctx, cfg, &net.Dialer{})
	return json.NewEncoder(out).Encode(rep)
}
