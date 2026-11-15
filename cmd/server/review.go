package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/bezmoradi/software-factory/internal/adapters/git/files"
	"github.com/bezmoradi/software-factory/internal/adapters/git/remote"
	"github.com/bezmoradi/software-factory/internal/adapters/git/state"
	"github.com/bezmoradi/software-factory/internal/adapters/http/page"
	"github.com/bezmoradi/software-factory/internal/adapters/install/npm"
	"github.com/bezmoradi/software-factory/internal/adapters/sandbox/boxes"
	"github.com/bezmoradi/software-factory/internal/adapters/sandbox/docker"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

type ReviewConfig struct {
	Dir    string
	SeenAs string
	Proxy  string
	Dev    string
}

func parseReview(cfg *PageConfig, getenv func(string) string) error {
	rc := ReviewConfig{Proxy: "egress-review:3128", SeenAs: boxes.ReviewsAt}
	if v := getenv("SF_REVIEWER_PROXY"); v != "" {
		if _, _, err := net.SplitHostPort(v); err != nil {
			return fmt.Errorf("SF_REVIEWER_PROXY must be host:port, got %q", v)
		}
		rc.Proxy = v
	}
	for _, p := range []struct {
		key    string
		target *string
	}{{"SF_REVIEWS", &rc.Dir}, {"SF_DEV_REVIEWER", &rc.Dev}} {
		v := getenv(p.key)
		if v == "" {
			continue
		}
		if !filepath.IsAbs(v) || strings.ContainsAny(v, "\x00\n") {
			return fmt.Errorf("%s must be an absolute path", p.key)
		}
		*p.target = filepath.Clean(v)
	}
	if rc.Dir != "" && cfg.Install.Dir == "" {
		return errors.New("reviews run knackpack from the agents volume, so SF_AGENTS_DIR can't be off while SF_REVIEWS is set")
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

func reviewSandbox(cfg PageConfig, hosts func() []string) (session.Sandbox, error) {
	if !cfg.Sandbox.On {
		return nil, nil
	}
	sb := cfg.Sandbox
	return docker.New(docker.Config{
		Probe: []string{cfg.AgentBinary, "sandbox-probe"},
		Proxy: cfg.Review.Proxy, Status: sb.Status, Side: ReviewersSide, Allow: sb.Allow, Deny: sb.Deny, Outside: sb.Outside, Repos: hosts, Lists: listsOf(sb), TLS: sb.TLS,
	})
}

func reviewerAgents(cfg PageConfig, repos *session.Repos, logger shared.Logger) (*session.ReviewerAgents, error) {
	if repos == nil {
		return nil, nil
	}
	return session.NewReviewerAgents(session.ReviewerAgentsConfig{Store: state.NewReviewerAgents(cfg.StateDir), Repos: repos, Audit: state.NewAudit(cfg.StateDir), Logger: logger})
}

func (p engineParts) reviewerChecksPage() session.ReviewerChecksPage {
	if p.reviews == nil {
		return nil
	}
	return p.reviews
}

func (p engineParts) reviewerAgentsPage() session.ReviewerAgentsPage {
	if p.agentFiles == nil {
		return nil
	}
	return p.agentFiles
}

func reviewer(base context.Context, cfg PageConfig, reg *session.Registry, source session.PluginSource, repos *session.Repos, expected *session.Expected, agentFiles *session.ReviewerAgents, makers boxes.ReviewerMaker, routes *boxRoutes, logger shared.Logger) (*session.Reviews, *reviewerHosts, error) {
	if cfg.Review.Dir == "" || makers == nil || routes == nil || source == nil || repos == nil {
		return nil, nil, nil
	}
	if err := os.Remove(filepath.Join(cfg.Review.Dir, oldHostKeyFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		logger.Warn("review", "event", "old-key-not-removed", "file", oldHostKeyFile)
	}
	hosts := newReviewerHosts(makers, routes, cfg.Sandbox.Allowlist, cfg.Review.Dev, logger)
	sb, err := reviewSandbox(cfg, gitHostsOf(repos))
	if err != nil {
		return nil, nil, err
	}
	hosts.sandbox = sb
	reviews, err := reviewsWith(base, cfg, reg, source, hosts, sb, repos, expected, agentFiles, logger)
	if err != nil {
		return nil, nil, err
	}
	return reviews, hosts, nil
}

const oldHostKeyFile = ".agent-host-key"

func reviewsWith(base context.Context, cfg PageConfig, reg *session.Registry, source session.PluginSource, host session.Host, sb session.Sandbox, mains session.Mains, expected *session.Expected, agentFiles *session.ReviewerAgents, logger shared.Logger) (*session.Reviews, error) {
	command := "claude"
	if cfg.Review.Dev != "" {
		command = cfg.Review.Dev
		logger.Warn("development reviewer switched on; reviews run a fake, not Claude Code", "variable", "SF_DEV_REVIEWER")
	}
	rc := session.ReviewsConfig{
		Host: host, Snapshots: files.Reviews{Dir: cfg.Review.Dir}, Records: state.Reviews{Dir: cfg.StateDir}, Replies: state.Replies{Dir: cfg.StateDir}, Checks: state.Checks{Dir: cfg.StateDir}, Plugins: source, Mains: mains, History: files.Reader{},
		Dir: cfg.Review.SeenAs, Command: command, Base: base, Show: reg.SetReview, Logger: logger,
		Expected: expected, ShowSignIn: func(si session.SignIn) { reg.Notify(page.ReviewerFrame(si)) }, Audit: state.NewAudit(cfg.StateDir),
	}
	if checker, ok := host.(session.SignInChecker); ok {
		rc.SignIns = checker
	}
	rc.Walkthroughs = state.NewWalkthroughs(cfg.StateDir)
	if agentFiles != nil {
		rc.AgentFiles = agentFiles
	}
	rc.PluginCommit, rc.ClaudeCode = pinnedProvenance(cfg.Review.Dev != "")
	rc.Minimum = reviewerMinimum(rc.PluginCommit)
	if sb != nil {
		rc.Sandbox = sb
	} else {
		logger.Warn("sandbox check switched off; reviews don't start", "variable", "SF_SANDBOX_CHECK")
	}
	return session.NewReviews(rc)
}
