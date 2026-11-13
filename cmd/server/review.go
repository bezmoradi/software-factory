package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/bezmoradi/software-factory/internal/adapters/agenthost"
	"github.com/bezmoradi/software-factory/internal/adapters/git/files"
	"github.com/bezmoradi/software-factory/internal/adapters/git/remote"
	"github.com/bezmoradi/software-factory/internal/adapters/git/state"
	"github.com/bezmoradi/software-factory/internal/adapters/http/page"
	"github.com/bezmoradi/software-factory/internal/adapters/install/npm"
	"github.com/bezmoradi/software-factory/internal/adapters/sandbox/docker"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

type ReviewConfig struct {
	Host   string
	Dir    string
	SeenAs string
	Proxy  string
	Dev    string
}

func parseReview(cfg *PageConfig, getenv func(string) string) error {
	rc := ReviewConfig{Proxy: "egress-review:3128"}
	if v := getenv("SF_REVIEWER_HOST"); v != "" && v != "off" {
		if _, _, err := net.SplitHostPort(v); err != nil {
			return fmt.Errorf("SF_REVIEWER_HOST must be host:port or off, got %q", v)
		}
		rc.Host = v
	}
	if v := getenv("SF_REVIEWER_PROXY"); v != "" {
		if _, _, err := net.SplitHostPort(v); err != nil {
			return fmt.Errorf("SF_REVIEWER_PROXY must be host:port, got %q", v)
		}
		rc.Proxy = v
	}
	for _, p := range []struct {
		key    string
		target *string
	}{{"SF_REVIEWS", &rc.Dir}, {"SF_REVIEWER_REVIEWS", &rc.SeenAs}, {"SF_DEV_REVIEWER", &rc.Dev}} {
		v := getenv(p.key)
		if v == "" {
			continue
		}
		if !filepath.IsAbs(v) || strings.ContainsAny(v, "\x00\n") {
			return fmt.Errorf("%s must be an absolute path", p.key)
		}
		*p.target = filepath.Clean(v)
	}
	if rc.Host != "" && rc.Dir == "" {
		return errors.New("set SF_REVIEWS, the engine's path to the reviews volume, when SF_REVIEWER_HOST is set")
	}
	if rc.SeenAs == "" {
		rc.SeenAs = rc.Dir
	}
	if rc.Host != "" && cfg.Install.Dir == "" {
		return errors.New("reviews run knackpack from the agents volume, so SF_AGENTS_DIR can't be off while SF_REVIEWER_HOST is set")
	}
	cfg.Review = rc
	return nil
}

func plugins(cfg InstallConfig, logger shared.Logger) (*npm.Plugins, error) {
	if cfg.Dir == "" {
		return nil, nil
	}
	return npm.NewPlugins(npm.PluginConfig{Dir: cfg.Dir, Fetch: remote.Remote{}, Trees: files.Reader{}, Logger: logger})
}

func reviewSandbox(cfg PageConfig, host session.Host, hosts func() []string) (session.Sandbox, error) {
	if !cfg.Sandbox.On {
		return nil, nil
	}
	sb := cfg.Sandbox
	return docker.New(docker.Config{
		Host: host, Probe: []string{cfg.AgentBinary, "sandbox-probe"}, Dir: cfg.Review.SeenAs,
		Proxy: cfg.Review.Proxy, Status: sb.Status, Allow: sb.Allow, Deny: sb.Deny, Outside: sb.Outside, Repos: hosts,
	})
}

func reviewer(base context.Context, cfg PageConfig, reg *session.Registry, source session.PluginSource, repos *session.Repos, expected *session.Expected, logger shared.Logger) (*session.Reviews, error) {
	if cfg.Review.Host == "" || source == nil || repos == nil {
		return nil, nil
	}
	client, err := agenthost.NewClientFor(cfg.Review.Host, agenthost.ReviewRunTimeout)
	if err != nil {
		return nil, err
	}
	key, err := writeHostKey(cfg.Review.Dir)
	if err != nil {
		return nil, err
	}
	client.WithKey(key)
	return reviewsWith(base, cfg, reg, source, client, repos, gitHostsOf(repos), expected, logger)
}

const hostKeyFile = ".agent-host-key"

func writeHostKey(dir string) (string, error) {
	if dir == "" {
		return "", nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	key := hex.EncodeToString(b)
	tmp := filepath.Join(dir, hostKeyFile+".new")
	if err := os.WriteFile(tmp, []byte(key+"\n"), 0o644); err != nil {
		return "", fmt.Errorf("review: write the reviewer's key: %w", err)
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		return "", fmt.Errorf("review: write the reviewer's key: %w", err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, hostKeyFile)); err != nil {
		return "", fmt.Errorf("review: write the reviewer's key: %w", err)
	}
	return key, nil
}

var newReviewSandbox = reviewSandbox

func reviewsWith(base context.Context, cfg PageConfig, reg *session.Registry, source session.PluginSource, host session.Host, mains session.Mains, hosts func() []string, expected *session.Expected, logger shared.Logger) (*session.Reviews, error) {
	sb, err := newReviewSandbox(cfg, host, hosts)
	if err != nil {
		return nil, err
	}
	command := "claude"
	if cfg.Review.Dev != "" {
		command = cfg.Review.Dev
		logger.Warn("development reviewer switched on; reviews run a fake, not Claude Code", "variable", "SF_DEV_REVIEWER")
	}
	rc := session.ReviewsConfig{
		Host: host, Snapshots: files.Reviews{Dir: cfg.Review.Dir}, Records: state.Reviews{Dir: cfg.StateDir}, Plugins: source, Mains: mains, History: files.Reader{},
		Dir: cfg.Review.SeenAs, Command: command, Base: base, Show: reg.SetReview, Logger: logger,
		Expected: expected, ShowSignIn: func(si session.SignIn) { reg.Notify(page.ReviewerFrame(si)) }, Audit: state.NewAudit(cfg.StateDir),
	}
	if checker, ok := host.(session.SignInChecker); ok {
		rc.SignIns = checker
	}
	rc.PluginCommit, rc.ClaudeCode = pinnedProvenance(cfg.Review.Dev != "")
	if sb != nil {
		rc.Sandbox = sb
	} else {
		logger.Warn("sandbox check switched off; reviews don't start", "variable", "SF_SANDBOX_CHECK")
	}
	return session.NewReviews(rc)
}
