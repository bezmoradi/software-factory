package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bezmoradi/software-factory/internal/adapters/agenthost"
	"github.com/bezmoradi/software-factory/internal/adapters/engines/acp"
	"github.com/bezmoradi/software-factory/internal/adapters/engines/codex"
	"github.com/bezmoradi/software-factory/internal/adapters/git/files"
	gitpush "github.com/bezmoradi/software-factory/internal/adapters/git/push"
	"github.com/bezmoradi/software-factory/internal/adapters/git/remote"
	"github.com/bezmoradi/software-factory/internal/adapters/git/state"
	"github.com/bezmoradi/software-factory/internal/adapters/http/page"
	"github.com/bezmoradi/software-factory/internal/adapters/install/npm"
	"github.com/bezmoradi/software-factory/internal/core/push"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const Version = "0.1.0"

type PageConfig struct {
	Host         string
	Port         int
	PublicPort   int
	PageName     string
	AgentAddress string
	StateDir     string
	AgentHost    string
	Workspace    string
	PluginDir    string
	PluginSocket string
	AgentBinary  string
	DevACPAgent  string
	DevGitCA     string
	Sandbox      SandboxConfig
	Install      InstallConfig
	Review       ReviewConfig
}

func ParsePageConfig(getenv func(string) string) (PageConfig, error) {
	cfg := PageConfig{Host: "127.0.0.1", Port: 7799, AgentAddress: "127.0.0.1:8081"}
	if v := getenv("SF_PAGE_HOST"); v != "" {
		if v != "127.0.0.1" && v != "0.0.0.0" {
			return cfg, fmt.Errorf("SF_PAGE_HOST must be 127.0.0.1 or, inside the Docker edition's container, 0.0.0.0; got %q", v)
		}
		cfg.Host = v
	}
	if v := getenv("PORT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 65535 {
			return cfg, fmt.Errorf("PORT must be a port number, got %q", v)
		}
		cfg.Port = n
	}
	if v := getenv("SF_PUBLIC_PORT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 65535 {
			return cfg, fmt.Errorf("SF_PUBLIC_PORT must be a port number, got %q", v)
		}
		cfg.PublicPort = n
	}
	if v := getenv("SF_PAGE_NAME"); v != "" {
		if net.ParseIP(v) != nil || strings.ContainsAny(v, ":/ \x00\n") {
			return cfg, fmt.Errorf("SF_PAGE_NAME must be a host name, such as engine-page, got %q", v)
		}
		cfg.PageName = v
	}
	if v := getenv("SF_AGENT_ADDRESS"); v != "" {
		if v == "off" {
			cfg.AgentAddress = ""
		} else if _, _, err := net.SplitHostPort(v); err != nil {
			return cfg, fmt.Errorf("SF_AGENT_ADDRESS must be host:port or off, got %q", v)
		} else {
			cfg.AgentAddress = v
		}
	}
	dir, err := stateDir(getenv)
	if err != nil {
		return cfg, err
	}
	cfg.StateDir = dir
	if err := parseStarts(&cfg, getenv); err != nil {
		return cfg, err
	}
	if cfg.Install, err = parseInstall(getenv); err != nil {
		return cfg, err
	}
	if err := parseReview(&cfg, getenv); err != nil {
		return cfg, err
	}
	return cfg, parseSandbox(&cfg, getenv)
}

func parseStarts(cfg *PageConfig, getenv func(string) string) error {
	cfg.PluginDir = "/opt/software-factory/plugin"
	cfg.PluginSocket = agenthost.DefaultSocket
	cfg.AgentBinary = "/usr/local/bin/software-factory"
	if v := getenv("SF_AGENT_HOST"); v != "" && v != "off" {
		if _, _, err := net.SplitHostPort(v); err != nil {
			return fmt.Errorf("SF_AGENT_HOST must be host:port or off, got %q", v)
		}
		cfg.AgentHost = v
	}
	if v := getenv("SF_WORKSPACE"); v != "" {
		if !filepath.IsAbs(v) {
			return errors.New("SF_WORKSPACE must be an absolute path")
		}
		cfg.Workspace = filepath.Clean(v)
	}
	if cfg.AgentHost != "" && cfg.Workspace == "" {
		return errors.New("set SF_WORKSPACE, the engine's path to the workspace, when SF_AGENT_HOST is set")
	}
	paths := []struct {
		key    string
		target *string
		off    bool
	}{
		{"SF_PLUGIN_DIR", &cfg.PluginDir, false},
		{agenthost.SocketVariable, &cfg.PluginSocket, false},
		{"SF_AGENT_BINARY", &cfg.AgentBinary, true},
		{"SF_DEV_ACP_AGENT", &cfg.DevACPAgent, false},
		{"SF_DEV_GIT_CA", &cfg.DevGitCA, false},
	}
	for _, p := range paths {
		v := getenv(p.key)
		switch {
		case v == "":
		case v == "off" && p.off:
			*p.target = ""
		case !filepath.IsAbs(v) || strings.ContainsAny(v, "\x00\n"):
			return fmt.Errorf("%s must be an absolute path in the agent container", p.key)
		default:
			*p.target = filepath.Clean(v)
		}
	}
	if len(cfg.PluginSocket) > agenthost.MaxSocketPath {
		return fmt.Errorf("%s must be at most %d bytes", agenthost.SocketVariable, agenthost.MaxSocketPath)
	}
	return nil
}

type engineParts struct {
	dispatcher *session.Dispatcher
	installer  *npm.Installer
	plugins    *npm.Plugins
	reviews    *session.Reviews
	repos      *session.Repos
	pushes     *session.Pushes
	work       *session.Work
	forges     *session.Forges
}

func (p engineParts) pushing() session.PushStarter {
	if p.pushes == nil {
		return nil
	}
	return p.pushes
}

func (p engineParts) requests() push.Requests {
	if p.pushes == nil {
		return nil
	}
	return p.pushes
}

func (p engineParts) reviewing() session.ReviewStarter {
	if p.dispatcher == nil || p.reviews == nil {
		return nil
	}
	return p.dispatcher
}

func (p engineParts) starter() session.Starter {
	if p.dispatcher == nil {
		return nil
	}
	return p.dispatcher
}

func (p engineParts) reviewerSignIn() func() (session.SignIn, bool) {
	if p.reviews == nil {
		return nil
	}
	return p.reviews.SignIn
}

func (p engineParts) logins() session.LoginPage {
	if p.dispatcher == nil {
		return nil
	}
	return p.dispatcher
}

func (p engineParts) admin() session.RepoAdmin {
	if p.repos == nil {
		return nil
	}
	return p.repos
}

func repositories(base context.Context, cfg PageConfig, reg *session.Registry, logger shared.Logger) *session.Repos {
	if cfg.Workspace == "" {
		return nil
	}
	if cfg.DevGitCA != "" {
		logger.Warn("development git CA trusted for clones and fetches", "variable", "SF_DEV_GIT_CA")
	}
	return session.NewRepos(session.ReposConfig{
		Workspace: cfg.Workspace,
		Records:   state.Repos{Dir: cfg.StateDir},
		Tokens:    state.NewTokens(cfg.StateDir),
		Remote:    remote.Remote{Workspace: cfg.Workspace, CAFile: cfg.DevGitCA},
		Commits:   files.Reader{},
		Notify:    reg.Notify,
		Base:      base,
		Logger:    logger,
	})
}

var newSandbox = sandboxChecker

func starter(base, sends context.Context, cfg PageConfig, reg *session.Registry, logger shared.Logger) (engineParts, error) {
	repos := repositories(base, cfg, reg, logger)
	if cfg.AgentHost == "" {
		return engineParts{repos: repos}, nil
	}
	client, err := agenthost.NewClient(cfg.AgentHost)
	if err != nil {
		return engineParts{}, err
	}
	sandbox, err := newSandbox(cfg, client, gitHostsOf(repos))
	if err != nil {
		return engineParts{}, err
	}
	if sandbox == nil {
		logger.Warn("sandbox check switched off; sessions start Held", "variable", "SF_SANDBOX_CHECK")
	}
	inst, err := installer(cfg.Install, logger)
	if err != nil {
		return engineParts{}, err
	}
	var installs session.Installs
	if inst != nil {
		installs = inst
	}
	pins, err := plugins(cfg.Install, logger)
	if err != nil {
		return engineParts{}, err
	}
	var source session.PluginSource
	if pins != nil {
		source = pins
	}
	expected := &session.Expected{Store: state.NewExpectations(cfg.StateDir)}
	reviews, err := reviewer(base, cfg, reg, source, repos, expected, logger)
	if err != nil {
		return engineParts{}, err
	}
	agents := session.Agents()
	if cfg.DevACPAgent != "" {
		agents = append(agents, session.DevelopmentAgent(cfg.DevACPAgent))
		logger.Warn("development ACP agent switched on", "variable", "SF_DEV_ACP_AGENT")
	}
	var d *session.Dispatcher
	work, err := workflowParts(cfg, reg, repos, reviews, pins, func(sid string) (session.PushTarget, bool) { return d.PushTarget(sid) }, logger)
	if err != nil {
		return engineParts{}, err
	}
	d = session.NewDispatcher(reg, session.DispatcherConfig{
		Agents:       agents,
		Host:         client,
		Openers:      map[session.Method]session.Opener{session.MethodACP: acp.Opener{Logger: logger}, session.MethodCodex: codex.Opener{Logger: logger}},
		Repositories: repos,
		PluginDir:    cfg.PluginDir,
		PluginSocket: cfg.PluginSocket,
		MCPCommand:   cfg.AgentBinary,
		Version:      Version,
		Sandbox:      sandbox,
		Installs:     installs,
		Reviews:      reviews,
		Startup:      files.Startup{},
		Approvals:    state.NewApprovals(cfg.StateDir),
		SignIns:      client,
		Expected:     expected,
		Work:         work,
		Workflow:     knackpackDir(pins),
		Workspace:    cfg.Workspace,
		Runs:         state.NewSessions(cfg.StateDir),
		Versions:     agentVersions,
		Note:         reg.NoteFor("restart"),
		Audit:        state.NewAudit(cfg.StateDir),
		Logger:       logger,
	})
	if repos != nil {
		repos.UseSessions(d.SessionsOf)
		repos.OnHosts(func() { d.Recheck(base) })
	}
	pushes, err := pusher(sends, cfg, reg, d, repos, reviews, work, logger)
	if err != nil {
		return engineParts{}, err
	}
	forged, err := forges(sends, cfg, reg, d, repos, work, logger)
	if err != nil {
		return engineParts{}, err
	}
	commentAfterPush(pushes, forged)
	return engineParts{dispatcher: d, installer: inst, plugins: pins, reviews: reviews, repos: repos, pushes: pushes, work: work, forges: forged}, nil
}

func pusher(base context.Context, cfg PageConfig, reg *session.Registry, d *session.Dispatcher, repos *session.Repos, reviews *session.Reviews, work *session.Work, logger shared.Logger) (*session.Pushes, error) {
	if repos == nil {
		return nil, nil
	}
	client, err := remote.Remote{CAFile: cfg.DevGitCA}.HTTPClient()
	if err != nil {
		return nil, err
	}
	pc := session.PushesConfig{
		Pusher: gitpush.Pusher{Reader: files.Reader{}, Client: client}, Heads: files.Reviews{}, History: files.Reader{}, Changes: files.Reader{},
		Repos: repos, Audit: state.NewAudit(cfg.StateDir), Targets: d.PushTarget, Level: reg.Level, Show: reg.SetPush, Base: base, Logger: logger,
		Tell: tellSession(reg), Note: reg.NoteFor("push"), Denials: state.NewDenials(cfg.StateDir), Waiting: state.NewWaiting(cfg.StateDir),
	}
	if work != nil {
		pc.Work = work
		pc.AutoFindings = work.AutoFindings
	}
	if reviews == nil {
		return session.NewPushes(pc)
	}
	pc.Reviews = reviews
	pc.StartReview = d.AgentReview
	pc.RetryReview = d.Retry
	pc.ReviewerSignIn, pc.SessionSignIn = reviews.SignIn, d.SessionSignIn
	p, err := session.NewPushes(pc)
	if err != nil {
		return nil, err
	}
	reviews.OnDone(p.ReviewDone)
	return p, nil
}

func stateDir(getenv func(string) string) (string, error) {
	if v := getenv("SF_STATE_DIR"); v != "" {
		if !filepath.IsAbs(v) {
			return "", fmt.Errorf("SF_STATE_DIR must be an absolute path")
		}
		return filepath.Clean(v), nil
	}
	if v := getenv("XDG_STATE_HOME"); filepath.IsAbs(v) {
		return filepath.Join(v, "software-factory"), nil
	}
	home := getenv("HOME")
	if !filepath.IsAbs(home) {
		return "", errors.New("set SF_STATE_DIR, XDG_STATE_HOME or HOME to an absolute path")
	}
	return filepath.Join(home, ".local", "state", "software-factory"), nil
}

func httpServer(handler http.Handler, base context.Context) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       time.Minute,
		MaxHeaderBytes:    16 << 10,
		BaseContext:       func(net.Listener) context.Context { return base },
	}
}

func Serve(ctx context.Context, logger shared.Logger) error {
	cfg, err := ParsePageConfig(os.Getenv)
	if err != nil {
		return err
	}
	if err := page.EnsureStateDir(cfg.StateDir); err != nil {
		return err
	}
	lock, err := page.LockState(cfg.StateDir)
	if err != nil {
		return fmt.Errorf("state folder lock: %w", err)
	}
	defer func() { _ = lock.Close() }()
	pageLn, err := net.Listen("tcp", net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)))
	if err != nil {
		return fmt.Errorf("page: listen: %w", err)
	}
	bound := pageLn.Addr().(*net.TCPAddr).Port
	public := cfg.PublicPort
	if public == 0 {
		public = bound
	}
	reg := session.New(Version, nil)
	if err := useUploads(reg, os.Getenv, logger); err != nil {
		_ = pageLn.Close()
		return err
	}
	clones, stopClones := context.WithCancel(context.Background())
	defer stopClones()
	sends, stopSends := context.WithCancel(context.Background())
	defer stopSends()
	parts, err := starter(clones, sends, cfg, reg, logger)
	if err != nil {
		_ = pageLn.Close()
		return err
	}
	srv, err := page.New(page.Config{PublicPort: public, StateDir: cfg.StateDir, Version: Version, Out: os.Stdout, Starter: parts.starter(), Reviews: parts.reviewing(), Repos: parts.admin(), Push: parts.requests(), Pushes: parts.pushing(), ReviewerSignIn: parts.reviewerSignIn(), Work: parts.workPage(), Workspace: cfg.Workspace, Audit: parts.auditPage(cfg), Logins: parts.logins(), Forge: parts.forging()}, reg, logger)
	if err != nil {
		_ = pageLn.Close()
		return err
	}
	if parts.dispatcher != nil {
		parts.dispatcher.Restore()
	}
	go parts.settle(sends, state.NewAudit(cfg.StateDir))

	base, cancelBase := context.WithCancel(context.Background())
	defer cancelBase()
	ctx, stop := context.WithCancel(ctx)
	defer stop()

	var agentLn net.Listener
	if cfg.AgentAddress != "" {
		agentLn, err = net.Listen("tcp", cfg.AgentAddress)
		if err != nil {
			_ = pageLn.Close()
			return fmt.Errorf("agent listener: %w", err)
		}
	}
	pageListener, err := pageOnly(ctx, pageLn, cfg, agentLn)
	if err != nil {
		_ = pageLn.Close()
		if agentLn != nil {
			_ = agentLn.Close()
		}
		return err
	}

	servers := []*http.Server{}
	errs := make(chan error, 2)
	pageServer := httpServer(srv.Page(), base)
	servers = append(servers, pageServer)
	go func() {
		errs <- pageServer.Serve(page.LimitListener(pageListener, page.PageConns, nil))
	}()
	agentAddress := "off"
	if agentLn != nil {
		agentAddress = agentLn.Addr().String()
		agentServer := httpServer(srv.Agent(), base)
		servers = append(servers, agentServer)
		go func() {
			errs <- agentServer.Serve(page.LimitListener(agentLn, page.AgentConns, nil))
		}()
	}
	go srv.Run(ctx)
	go page.HoldLock(ctx, lock)
	if parts.dispatcher != nil {
		go func() {
			lctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			parts.dispatcher.StopLeftovers(lctx)
		}()
		go parts.dispatcher.Watch(ctx, session.CheckEvery)
		go parts.dispatcher.WatchStates(ctx, session.StatesEvery)
	}
	if parts.reviews != nil {
		go func() {
			sctx, cancel := context.WithTimeout(ctx, time.Minute)
			defer cancel()
			_, _ = parts.reviews.CheckSignIn(sctx)
		}()
	}
	if parts.installer != nil && cfg.Install.AtStart {
		go func() {
			if err := parts.installer.InstallMissing(ctx, nil); err != nil {
				logger.Warn("agent install", "event", "not-installed", "fix", "docker compose exec engine software-factory install-agents")
			}
			if parts.plugins != nil {
				if err := parts.plugins.InstallMissing(ctx, nil); err != nil {
					logger.Warn("plugin install", "event", "not-installed", "fix", "docker compose exec engine software-factory install-agents")
				}
			}
		}()
	}

	agentHost := cfg.AgentHost
	if agentHost == "" {
		agentHost = "off"
	}
	reviewerHost := cfg.Review.Host
	if parts.reviews == nil {
		reviewerHost = "off"
	}
	logger.Info("engine listening", "page", pageLn.Addr().String(), "public_port", public, "agent", agentAddress, "agent_host", agentHost, "reviewer", reviewerHost, "version", Version)
	if !srv.Built() {
		logger.Warn("the page isn't built into this engine", "fix", "npm --prefix web run build")
	}
	srv.PrintLink()

	var serveErr error
	select {
	case serveErr = <-errs:
	case <-ctx.Done():
	}
	stop()
	parts.closeSends()
	stopClones()
	reg.DetachAll()
	parts.waitSends(pushGrace)
	stopSends()
	parts.waitSends(sendsAfter)
	cancelBase()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, s := range servers {
		_ = s.Shutdown(shutdownCtx)
	}
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	return nil
}

func pageOnly(ctx context.Context, pageLn net.Listener, cfg PageConfig, agentLn net.Listener) (net.Listener, error) {
	if ip := net.ParseIP(cfg.Host); ip == nil || !ip.IsUnspecified() {
		return pageLn, nil
	}
	fail := func(why string) error {
		return errors.New("SF_PAGE_HOST=0.0.0.0 needs SF_PAGE_NAME, a name that resolves to the engine's own address on the network the page is published from, such as engine-page on the outside network, so the page can refuse every other network: " + why)
	}
	if cfg.PageName == "" {
		return nil, fail("SF_PAGE_NAME isn't set")
	}
	lookup, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupNetIP(lookup, "ip", cfg.PageName)
	if err != nil || len(addrs) == 0 {
		return nil, fail(cfg.PageName + " doesn't resolve")
	}
	own, err := localAddrs()
	if err != nil {
		return nil, fail("the engine's own addresses can't be read")
	}
	var refused netip.Addr
	if agentLn != nil {
		if bound, ok := agentLn.Addr().(*net.TCPAddr); ok {
			refused, _ = netip.AddrFromSlice(bound.IP)
			refused = refused.Unmap()
		}
	}
	allowed := []netip.Addr{}
	for _, a := range addrs {
		a = a.Unmap()
		switch {
		case a.IsUnspecified() || a.IsLoopback():
			return nil, fail(cfg.PageName + " resolves to " + a.String() + ", which isn't the outside network's address")
		case refused.IsValid() && a == refused:
			return nil, fail(cfg.PageName + " resolves to the agent listener's address")
		case own[a]:
			allowed = append(allowed, a)
		}
	}
	if len(allowed) == 0 {
		return nil, fail(cfg.PageName + " doesn't resolve to an address of this engine")
	}
	return page.AllowLocal(pageLn, allowed, nil), nil
}

func localAddrs() (map[netip.Addr]bool, error) {
	list, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	out := map[netip.Addr]bool{}
	for _, a := range list {
		if n, ok := a.(*net.IPNet); ok {
			if ip, ok := netip.AddrFromSlice(n.IP); ok {
				out[ip.Unmap()] = true
			}
		}
	}
	return out, nil
}
