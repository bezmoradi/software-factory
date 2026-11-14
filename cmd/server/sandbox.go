package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"

	"github.com/bezmoradi/software-factory/internal/adapters/egress"
	"github.com/bezmoradi/software-factory/internal/adapters/git/state"
	"github.com/bezmoradi/software-factory/internal/adapters/sandbox/docker"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const DefaultOutsideHost = "example.com"

type SandboxConfig struct {
	On        bool
	Proxy     string
	Status    string
	KeyFile   string
	Allow     []string
	Deny      []string
	Outside   string
	Dir       string
	Allowlist *session.Allowlist
	TLS       *tls.Config
}

func parseSandbox(cfg *PageConfig, getenv func(string) string) error {
	sb := SandboxConfig{Proxy: "egress:3128", Status: "egress-control:3129", Outside: DefaultOutsideHost, Dir: cfg.Workspace}
	switch v := getenv("SF_SANDBOX_CHECK"); v {
	case "", "on":
		sb.On = cfg.AgentHost != ""
	case "off":
		if dockerEdition(cfg) {
			return errors.New("SF_SANDBOX_CHECK=off is only for development outside Docker, and this engine runs as the Docker edition (SF_PAGE_HOST=0.0.0.0 or SF_PAGE_NAME is set), so it doesn't start: remove SF_SANDBOX_CHECK from the engine's environment")
		}
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
	if v := getenv("SF_EGRESS_KEY_FILE"); v != "" {
		if !filepath.IsAbs(v) || strings.ContainsAny(v, "\x00\n") {
			return errors.New("SF_EGRESS_KEY_FILE must be an absolute path in the engine's container")
		}
		sb.KeyFile = filepath.Clean(v)
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

func dockerEdition(cfg *PageConfig) bool {
	return cfg.Host == "0.0.0.0" || cfg.PageName != ""
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
		Proxy: sb.Proxy, Status: sb.Status, Side: AgentsSide, Allow: sb.Allow, Deny: sb.Deny, Outside: sb.Outside, Repos: hosts, Lists: listsOf(sb), TLS: sb.TLS,
	})
}

func listsOf(sb SandboxConfig) docker.Lists {
	if sb.Allowlist == nil {
		return nil
	}
	return sb.Allowlist
}

func egressLists(cfg PageConfig, repos *session.Repos, logger shared.Logger) (*session.Allowlist, *tls.Config, error) {
	sb := cfg.Sandbox
	if !sb.On || sb.KeyFile == "" {
		return nil, nil, nil
	}
	tc, err := docker.WriteControlTLS(controlFile(sb.KeyFile, ControlCertName), controlFile(sb.KeyFile, ControlTLSKeyName), sb.Status)
	if err != nil {
		return nil, nil, err
	}
	key, err := docker.WriteControlKey(sb.KeyFile)
	if err != nil {
		return nil, nil, err
	}
	control, err := docker.NewControl(sb.Status, key, tc)
	if err != nil {
		return nil, nil, err
	}
	allow, err := session.NewAllowlist(session.AllowlistConfig{
		Store: state.NewEgress(cfg.StateDir), Control: control, Base: sb.Allow, Deny: sb.Deny, Outside: sb.Outside,
		Repos: gitHostsOf(repos), Audit: state.NewAudit(cfg.StateDir), Logger: logger,
	})
	return allow, tc, err
}

func (p engineParts) egressPage() session.EgressPage {
	if p.allowlist == nil {
		return nil
	}
	return p.allowlist
}

func ServeProbe(ctx context.Context, args []string, out io.Writer) error {
	cfg, err := docker.ParseProbeArgs(args)
	if err != nil {
		return err
	}
	rep := docker.Probe(ctx, cfg, &net.Dialer{})
	return json.NewEncoder(out).Encode(rep)
}
