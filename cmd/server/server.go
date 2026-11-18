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
	"slices"
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
	"github.com/bezmoradi/software-factory/internal/adapters/git/tokencheck"
	"github.com/bezmoradi/software-factory/internal/adapters/http/page"
	"github.com/bezmoradi/software-factory/internal/adapters/install/npm"
	"github.com/bezmoradi/software-factory/internal/adapters/sandbox/boxes"
	"github.com/bezmoradi/software-factory/internal/core/push"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	Version     = "0.1.1"
	keysAtStart = time.Minute
)

type PageConfig struct {
	Host         string
	Port         int
	PublicPort   int
	PageName     string
	AgentAddress string
	StateDir     string
	Helper       string
	HelperKey    string
	BoxRanges    []netip.Prefix
	Workspace    string
	PluginDir    string
	PluginSocket string
	AgentBinary  string
	DevACPAgent  string
	DevGitCA     string
	DevDocs      bool
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
	if err := parseDocs(&cfg, getenv); err != nil {
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
	if getenv("SF_AGENT_HOST") != "" {
		return errors.New("SF_AGENT_HOST is gone: each session runs in its own container, which the box helper makes; set SF_HELPER to the helper's host:port instead")
	}
	if v := getenv("SF_HELPER"); v != "" && v != "off" {
		if _, _, err := net.SplitHostPort(v); err != nil {
			return fmt.Errorf("SF_HELPER must be host:port or off, got %q", v)
		}
		cfg.Helper = v
	}
	cfg.HelperKey = DefaultHelperKeyFile
	if v := getenv("SF_HELPER_KEY_FILE"); v != "" {
		k, err := helperKeyFile(v)
		if err != nil {
			return err
		}
		cfg.HelperKey = k
	}
	if v := getenv("SF_WORKSPACE"); v != "" {
		if !filepath.IsAbs(v) {
			return errors.New("SF_WORKSPACE must be an absolute path")
		}
		cfg.Workspace = filepath.Clean(v)
	}
	if cfg.Helper != "" && cfg.Workspace == "" {
		return errors.New("set SF_WORKSPACE, the engine's path to the workspace, when SF_HELPER is set")
	}
	pool, err := boxes.ParseConfig(getenv)
	if err != nil {
		return err
	}
	cfg.BoxRanges = pool.Ranges
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
	routes     *boxRoutes
	dispatcher *session.Dispatcher
	installer  *npm.Installer
	plugins    *npm.Plugins
	reviews    *session.Reviews
	repos      *session.Repos
	pushes     *session.Pushes
	work       *session.Work
	forges     *session.Forges
	agentFiles *session.ReviewerAgents
	identity   *session.Identities
	allowlist  *session.Allowlist
	catalogs   *session.Catalogs
	boxes      session.Boxes

	reviewerHosts *reviewerHosts
}

func reachable(b session.Boxes) func(context.Context) error {
	if r, ok := b.(interface{ Reachable(context.Context) error }); ok {
		return r.Reachable
	}
	return nil
}

func (p engineParts) helperDown() func() bool {
	if p.routes == nil {
		return nil
	}
	return p.routes.helperDown
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

func (p engineParts) branching() session.BranchPage {
	if p.dispatcher == nil {
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
	var checker session.TokenChecker
	if client, err := (remote.Remote{CAFile: cfg.DevGitCA}).HTTPClient(); err == nil {
		checker = tokencheck.Checker{Client: client}
	} else {
		logger.Warn("repository", "event", "token-check-off", "err", err.Error())
	}
	return session.NewRepos(session.ReposConfig{
		Workspace: cfg.Workspace,
		Records:   state.Repos{Dir: cfg.StateDir},
		Tokens:    state.NewTokens(cfg.StateDir),
		Remote:    remote.Remote{Workspace: cfg.Workspace, CAFile: cfg.DevGitCA, OpenBox: files.OpenBox},
		Commits:   files.Reader{},
		Checker:   checker,
		Notify:    reg.Notify,
		Base:      base,
		Logger:    logger,
	})
}

var newSandbox = sandboxChecker

func starter(base, sends context.Context, cfg PageConfig, reg *session.Registry, uploads *page.UploadDir, logger shared.Logger) (engineParts, error) {
	repos := repositories(base, cfg, reg, logger)
	if cfg.Helper == "" {
		return engineParts{repos: repos}, nil
	}
	helper, err := HelperBoxes(cfg.Helper, cfg.HelperKey)
	if err != nil {
		return engineParts{}, err
	}
	allow, tc, control, err := egressLists(cfg, repos, logger)
	if err != nil {
		return engineParts{}, err
	}
	cfg.Sandbox.Allowlist, cfg.Sandbox.TLS = allow, tc
	routes := newBoxRoutes(reg, allow, control, cfg.BoxRanges, reachable(helper), logger)
	sandbox, err := newSandbox(cfg, gitHostsOf(repos))
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
	agentFiles, err := reviewerAgents(cfg, repos, logger)
	if err != nil {
		return engineParts{}, err
	}
	makers, _ := helper.(boxes.ReviewerMaker)
	reviews, reviewerHosts, err := reviewer(base, cfg, reg, source, repos, expected, agentFiles, makers, routes, logger)
	if err != nil {
		return engineParts{}, err
	}
	agents := session.Agents()
	if cfg.DevACPAgent != "" {
		agents = append(agents, session.DevelopmentAgent(cfg.DevACPAgent, cfg.DevDocs))
		logger.Warn("development ACP agent switched on", "variable", "SF_DEV_ACP_AGENT")
	}
	docs := docsInRepo(cfg, logger)
	identity := session.NewIdentities(state.NewIdentity(cfg.StateDir), logger)
	var d *session.Dispatcher
	catalogs := knackpackCatalog(pins, logger)
	work, err := workflowParts(cfg, docs, reg, repos, reviews, pins, catalogs, func(sid string) (session.PushTarget, bool) { return d.PushTarget(sid) }, agentFiles, logger)
	if err != nil {
		return engineParts{}, err
	}
	d = session.NewDispatcher(reg, session.DispatcherConfig{
		Agents:       agents,
		Boxes:        helper,
		BoxStore:     state.NewBoxes(cfg.StateDir),
		Routes:       routes,
		Folders:      boxFolders{workspace: cfg.Workspace, uploads: uploads},
		Clones:       remote.Remote{Workspace: cfg.Workspace, CAFile: cfg.DevGitCA, OpenBox: files.OpenBox},
		CloneReader:  files.Reader{},
		Openers:      map[session.Method]session.Opener{session.MethodACP: acp.Opener{Logger: logger}, session.MethodCodex: codex.Opener{Logger: logger, DeveloperInstructions: agenthost.MCPInstructions, RateWindows: reg.Limits().CodexWindows}},
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
		Expected:     expected,
		Work:         work,
		Workflow:     knackpackDir(pins),
		Workspace:    cfg.Workspace,
		Runs:         state.NewSessions(cfg.StateDir),
		Versions:     agentVersions,
		Note:         reg.NoteFor("restart"),
		Audit:        state.NewAudit(cfg.StateDir),
		Identity:     identity,
		DocsInRepo:   docs,
		KeyRoutes:    state.NewKeyRoutes(cfg.StateDir),
		Logger:       logger,
	})
	if allow != nil {
		allow.OnChange(func() {
			if reviews != nil {
				reviews.SignInChanged()
			}
			d.Recheck(base)
		})
		allow.UseKeys(d)
		d.UseGateways(allow)
	}
	if reviews != nil {
		reviews.UseKeyGate(func(ctx context.Context) error { return d.KeyRouted(ctx, session.SideReviewers) })
	}
	reg.CheckClaims(session.ClaimChecks{Targets: d.PushTarget, Heads: files.Reviews{}, Commits: files.Reader{}, Files: page.TurnFiles{Workspace: cfg.Workspace, OpenTree: files.OpenTree}})
	if repos != nil {
		repos.UseSessions(d.SessionsOf)
		repos.OnFetched(d.SyncClones)
		repos.OnHosts(func() { d.Recheck(base) })
	}
	pushes, err := pusher(sends, cfg, reg, d, repos, reviews, work, agentFiles, logger)
	if err != nil {
		return engineParts{}, err
	}
	forged, err := forges(sends, cfg, reg, d, repos, work, logger)
	if err != nil {
		return engineParts{}, err
	}
	commentAfterPush(pushes, forged)
	if pushes != nil {
		d.OnRemoved(pushes.ForgetDenials)
	}
	if reviews != nil {
		d.OnRemoved(reviews.Forget)
		d.OnRemoved(reviews.ForgetReplies)
	}
	d.UseForges(forged)
	noteCompactions(reg, work, logger)
	return engineParts{routes: routes, dispatcher: d, installer: inst, plugins: pins, reviews: reviews, repos: repos, pushes: pushes, work: work, forges: forged, agentFiles: agentFiles, identity: identity, allowlist: allow, catalogs: catalogs, boxes: helper, reviewerHosts: reviewerHosts}, nil
}

func pusher(base context.Context, cfg PageConfig, reg *session.Registry, d *session.Dispatcher, repos *session.Repos, reviews *session.Reviews, work *session.Work, agentFiles *session.ReviewerAgents, logger shared.Logger) (*session.Pushes, error) {
	if repos == nil {
		return nil, nil
	}
	client, err := remote.Remote{CAFile: cfg.DevGitCA}.HTTPClient()
	if err != nil {
		return nil, err
	}
	pc := session.PushesConfig{
		Pusher: gitpush.Pusher{Reader: files.Reader{}, Client: client}, Heads: files.Reviews{}, History: files.Reader{}, Changes: files.Reader{},
		Repos: repos, Audit: state.NewAudit(cfg.StateDir), Targets: d.PushTarget, Level: reg.PushLevel, Show: reg.SetPush, Base: base, Logger: logger,
		Tell: tellSession(reg), Note: reg.NoteFor("push"), Denials: state.NewDenials(cfg.StateDir), Waiting: state.NewWaiting(cfg.StateDir),
		TurnBase: reg.TurnBase, Branch: files.Reader{}, Checks: reg.AgentChecks,
	}
	if work != nil {
		pc.Work = work
		pc.AutoFindings = work.AutoFindings
	}
	if agentFiles != nil {
		pc.RoleAgents = agentFiles
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
	begun, err := startState(cfg, os.Getenv, logger)
	if err != nil {
		return err
	}
	if cfg.Helper != "" {
		moved, err := files.PrepareBoxes(cfg.Workspace)
		if moved != "" {
			logger.Warn("workspace", "event", "sessions-folder-moved-aside", "to", moved)
		}
		if err != nil {
			return fmt.Errorf("workspace: %w", err)
		}
	}
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
	reg.UseNotes(state.NewNotes(cfg.StateDir))
	uploads, err := useUploads(reg, os.Getenv, logger)
	if err != nil {
		_ = pageLn.Close()
		return err
	}
	clones, stopClones := context.WithCancel(context.Background())
	defer stopClones()
	sends, stopSends := context.WithCancel(context.Background())
	defer stopSends()
	parts, err := starter(clones, sends, cfg, reg, uploads, logger)
	if err != nil {
		_ = pageLn.Close()
		return err
	}
	shots := useScreenshots(reg, cfg.Workspace, parts.routes, parts.dispatcher, logger)
	updates := newUpdates(os.Getenv, cfg, logger)
	go updates.Watch(sends)
	srv, err := page.New(page.Config{PublicPort: public, StateDir: cfg.StateDir, Version: Version, Out: os.Stdout, Starter: parts.starter(), Reviews: parts.reviewing(), Repos: parts.admin(), Push: parts.requests(), Pushes: parts.pushing(), ReviewerSignIn: parts.reviewerSignIn(), Work: parts.workPage(), Workspace: cfg.Workspace, Audit: parts.auditPage(cfg), Logins: parts.logins(), Forge: parts.forging(), ReviewerAgents: parts.reviewerAgentsPage(), ReviewerChecks: parts.reviewerChecksPage(), Identity: parts.identities(), OpenTree: files.OpenTree, HelperDown: parts.helperDown(), Diagnostics: diagnosticsPage(parts, reg, updates, logger)}, reg, logger)
	if err != nil {
		_ = pageLn.Close()
		return err
	}
	srv.UseEgress(parts.egressPage())
	srv.UseUpdates(updates)
	srv.UseBranching(parts.branching())
	if parts.reviewing() != nil {
		srv.UseWholeReplies(parts.dispatcher)
	}
	useChangeFacts(sends, srv, reg, parts, cfg, logger)
	if parts.dispatcher != nil {
		kctx, cancelKeys := context.WithTimeout(ctx, keysAtStart)
		parts.dispatcher.ReconcileKeys(kctx)
		cancelKeys()
		parts.dispatcher.Restore()
		if parts.pushes != nil && !parts.dispatcher.RunsUnread() {
			parts.pushes.PruneDenials(parts.dispatcher.Known)
		}
	}
	if parts.dispatcher == nil || !parts.dispatcher.RunsUnread() {
		reg.DropStrayNotes()
	}
	parts.useHalt(reg, cfg, logger)
	parts.useLimits(reg, cfg, logger)
	go parts.settle(sends, state.NewAudit(cfg.StateDir))
	useAgentReports(sends, srv, parts, cfg, logger)
	useViews(srv, parts, cfg, reg, logger)
	useSavedPrompts(srv, parts, cfg, logger)
	usePulls(sends, srv, reg, parts, cfg, logger)
	useSessionWork(sends, srv, reg, parts, cfg, logger)
	useProjectViews(srv, parts, cfg, reg, logger)
	useKeep(srv, reg, parts, cfg, logger)
	useStarts(sends, srv, reg, parts, cfg, logger)
	restart := &restartHook{}
	useBackups(srv, parts, cfg, begun, restart, logger)
	useReviewExtras(srv, reg, parts, cfg, logger)

	base, cancelBase := context.WithCancel(context.Background())
	defer cancelBase()
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	restart.set(stop)

	var agentLn *page.BoxListener
	if cfg.AgentAddress != "" {
		ln, err := net.Listen("tcp", cfg.AgentAddress)
		if err != nil {
			_ = pageLn.Close()
			return fmt.Errorf("agent listener: %w", err)
		}
		agentLn = page.NewBoxListener(ln, dropCounter(logger, "agent-listener", "a session container's"))
		if parts.routes != nil {
			parts.routes.useListener(agentLn)
		}
	}
	pageListener, err := pageOnly(ctx, pageLn, cfg)
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
		agentServer.ConnContext = page.BoxContext
		servers = append(servers, agentServer)
		go func() {
			errs <- agentServer.Serve(page.LimitListener(agentLn, page.AgentConns, nil))
		}()
	}
	go srv.Run(ctx)
	go page.HoldLock(ctx, lock)
	if parts.routes != nil {
		go parts.routes.watch(ctx)
	}
	if parts.dispatcher != nil {
		go dropStrayScreenshots(ctx, parts.dispatcher, shots, logger)
		go func() {
			lctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			parts.dispatcher.StopLeftovers(lctx)
		}()
		go parts.dispatcher.Watch(ctx, session.CheckEvery)
		if parts.allowlist != nil {
			go parts.allowlist.Watch(ctx, session.ExpireWatch)
		}
		go parts.dispatcher.WatchStates(ctx, session.StatesEvery)
	}
	if parts.reviewerHosts != nil {
		go parts.reviewerHosts.watch(ctx)
	}
	if parts.reviews != nil {
		go func() {
			for range reviewerSignInTries {
				sctx, cancel := context.WithTimeout(ctx, time.Minute)
				_, err := parts.reviews.CheckSignIn(sctx)
				cancel()
				if err == nil || ctx.Err() != nil {
					return
				}
				t := time.NewTimer(reviewerSweepWait)
				select {
				case <-ctx.Done():
					t.Stop()
					return
				case <-t.C:
				}
			}
		}()
	}
	if parts.installer != nil && cfg.Install.AtStart {
		go func() {
			if err := parts.installer.InstallMissing(ctx, nil); err != nil {
				logger.Warn("agent install", "event", "not-installed", "fix", session.Compose()+" exec engine software-factory install-agents")
			}
			if parts.plugins != nil {
				if err := parts.plugins.InstallMissing(ctx, nil); err != nil {
					logger.Warn("plugin install", "event", "not-installed", "fix", session.Compose()+" exec engine software-factory install-agents")
				}
				parts.catalogs.Load()
			}
		}()
	}

	helperAddress := cfg.Helper
	if helperAddress == "" {
		helperAddress = "off"
	}
	reviewerHost := "helper"
	if parts.reviews == nil {
		reviewerHost = "off"
	}
	logger.Info("engine listening", "page", pageLn.Addr().String(), "public_port", public, "agent", agentAddress, "helper", helperAddress, "reviewer", reviewerHost, "version", Version)
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
	reviewsEnded := parts.endReviews(reviewsGrace, logger)
	reg.DetachAll()
	parts.waitSends(pushGrace)
	stopSends()
	parts.waitSends(sendsAfter)
	<-reviewsEnded
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

const reviewsGrace = 5 * time.Second

func (p engineParts) endReviews(limit time.Duration, logger shared.Logger) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		if p.reviews == nil {
			return
		}
		if left := p.reviews.WaitRunning(limit); left > 0 {
			logger.Warn("review", "event", "shutdown-timeout", "running", left, "waited", limit.String())
		}
	}()
	return done
}

func pageOnly(ctx context.Context, pageLn net.Listener, cfg PageConfig) (net.Listener, error) {
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
	inPool := func(a netip.Addr) bool {
		return slices.ContainsFunc(cfg.BoxRanges, func(p netip.Prefix) bool { return p.Contains(a) })
	}
	allowed := []netip.Addr{}
	for _, a := range addrs {
		a = a.Unmap()
		switch {
		case a.IsUnspecified() || a.IsLoopback():
			return nil, fail(cfg.PageName + " resolves to " + a.String() + ", which isn't the outside network's address")
		case inPool(a):
			return nil, fail(cfg.PageName + " resolves to an address in the session containers' networks (SF_BOX_SUBNETS)")
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
