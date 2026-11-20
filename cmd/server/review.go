package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bezmoradi/software-factory/internal/adapters/git/files"
	"github.com/bezmoradi/software-factory/internal/adapters/git/remote"
	"github.com/bezmoradi/software-factory/internal/adapters/git/state"
	"github.com/bezmoradi/software-factory/internal/adapters/http/page"
	"github.com/bezmoradi/software-factory/internal/adapters/install/npm"
	"github.com/bezmoradi/software-factory/internal/adapters/sandbox/boxes"
	"github.com/bezmoradi/software-factory/internal/adapters/sandbox/docker"
	"github.com/bezmoradi/software-factory/internal/core/review"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const devReviewMinutes = "SF_DEV_REVIEW_MINUTES"

type ReviewConfig struct {
	Dir        string
	SeenAs     string
	Proxy      string
	Dev        string
	DevMinutes string
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
	rc.DevMinutes = getenv(devReviewMinutes)
	cfg.Review = rc
	return nil
}

func devReviewFor(v string, dev bool, logger shared.Logger) (time.Duration, error) {
	if v == "" {
		return 0, nil
	}
	if !dev {
		logger.Warn("review", "event", "dev-timeout-ignored", "why", devReviewMinutes+" shortens the reviewer runs' time limit, so the engine honours it only in a development image, and this image has no "+boxes.DevMarker)
		return 0, nil
	}
	most := int(session.ReviewFor/time.Minute) - 1
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > most || strconv.Itoa(n) != v {
		return 0, fmt.Errorf("%s must be a whole number of minutes from 1 to %d, got %q", devReviewMinutes, most, v)
	}
	logger.Warn(fmt.Sprintf("development switch: a reviewer run times out after %d minutes, not %d", n, int(session.ReviewFor/time.Minute)), "variable", devReviewMinutes)
	return time.Duration(n) * time.Minute, nil
}

func plugins(cfg InstallConfig, logger shared.Logger) (*npm.Plugins, error) {
	if cfg.Dir == "" {
		return nil, nil
	}
	tracked, fetch, err := knackpackSource(cfg, developmentImage(boxes.DevMarker), logger)
	if err != nil {
		return nil, err
	}
	return npm.NewPlugins(npm.PluginConfig{Dir: cfg.Dir, Plugins: []npm.Tracked{tracked}, Fetch: fetch, Trees: files.Reader{}, Logger: logger})
}

func knackpackSource(cfg InstallConfig, dev bool, logger shared.Logger) (npm.Tracked, remote.Remote, error) {
	k := npm.Knackpack
	if cfg.DevKnackpack == "" {
		return k, remote.Remote{}, nil
	}
	if !dev {
		logger.Warn("plugin install", "event", "dev-source-ignored", "why", devKnackpackURL+" installs knackpack from another repository, so the engine honours it only in a development image, and this image has no "+boxes.DevMarker)
		return k, remote.Remote{}, nil
	}
	u, err := url.Parse(cfg.DevKnackpack)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Path == "" || u.Path == "/" {
		return k, remote.Remote{}, errors.New(devKnackpackURL + " must be an https URL with a path and no user, query or fragment, such as https://gitserver.test:8443/public/knackpack.git")
	}
	logger.Warn("development switch: knackpack follows the main branch of another repository, not "+npm.Knackpack.URL, "variable", devKnackpackURL)
	k.URL = cfg.DevKnackpack
	return k, remote.Remote{CAFile: cfg.DevGitCA}, nil
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

func reviewerAgents(cfg PageConfig, repos *session.Repos, packs *session.Packs, logger shared.Logger) (*session.ReviewerAgents, error) {
	if repos == nil {
		return nil, nil
	}
	rc := session.ReviewerAgentsConfig{Store: state.NewReviewerAgents(cfg.StateDir), Repos: repos, Audit: state.NewAudit(cfg.StateDir), Logger: logger}
	if packs != nil {
		rc.Packs = packs
	}
	return session.NewReviewerAgents(rc)
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
		Host: host, Snapshots: files.Reviews{Dir: cfg.Review.Dir}, Records: state.Reviews{Dir: cfg.StateDir}, Replies: state.Replies{Dir: cfg.StateDir}, Plugins: source, Mains: mains, History: files.Reader{},
		Dir: cfg.Review.SeenAs, Command: command, Base: base, Show: reg.SetReview, Logger: logger,
		Expected: expected, ShowSignIn: func(si session.SignIn) { reg.Notify(page.ReviewerFrame(si)) }, Audit: state.NewAudit(cfg.StateDir),
	}
	timeout, err := devReviewFor(cfg.Review.DevMinutes, developmentImage(boxes.DevMarker), logger)
	if err != nil {
		return nil, err
	}
	rc.Timeout = timeout
	if checker, ok := host.(session.SignInChecker); ok {
		rc.SignIns = checker
	}
	rc.Walkthroughs = state.NewWalkthroughs(cfg.StateDir)
	if agentFiles != nil {
		rc.AgentFiles = agentFiles
	}
	rc.ClaudeCode = pinnedClaudeCode(cfg.Review.Dev != "")
	rc.Minimum = review.Minimum{ClaudeCode: npm.MinimumReviewerClaudeCode, RefusedPlugins: npm.RefusedKnackpack}
	if sb != nil {
		rc.Sandbox = sb
	} else {
		logger.Warn("sandbox check switched off; reviews don't start", "variable", "SF_SANDBOX_CHECK")
	}
	return session.NewReviews(rc)
}
