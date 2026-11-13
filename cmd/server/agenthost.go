package server

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"golang.org/x/net/netutil"

	"github.com/bezmoradi/software-factory/internal/adapters/agenthost"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

type AgentHostConfig struct {
	Listen       string
	Workspace    string
	AgentsBin    string
	PluginSocket string
	Engine       string
	Git          string
	MaxProcesses int
	Review       bool
	KeyFile      string
}

func ParseAgentHostConfig(args []string, getenv func(string) string) (AgentHostConfig, error) {
	env := func(key, fallback string) string {
		if v := getenv(key); v != "" {
			return v
		}
		return fallback
	}
	fs := flag.NewFlagSet("agent-host", flag.ContinueOnError)
	listen := fs.String("listen", env("SF_AGENT_HOST_LISTEN", ":8090"), "control and stdio listen address on the agent network")
	workspace := fs.String("workspace", env("SF_WORKSPACE", ""), "the workspace folder: repositories in repos/, worktrees in worktrees/")
	agentsBin := fs.String("agents-bin", env("SF_AGENTS_BIN", DefaultAgentsDir+"/bin"), "the installed agents' folder, put first on their PATH, or off")
	socket := fs.String("plugin-socket", env(agenthost.SocketVariable, agenthost.DefaultSocket), "the plugin's Unix socket, or off")
	engine := fs.String("engine", env("SF_ENGINE_AGENT_ADDRESS", "engine:8081"), "the engine's agent listener, host:port")
	git := fs.String("git", env("SF_GIT", "git"), "the git program for worktrees")
	maxProcs := fs.String("max-processes", env("SF_AGENT_HOST_MAX_PROCESSES", strconv.Itoa(agenthost.DefaultMaxProcs)), "most agent processes at once")
	mode := fs.String("mode", env("SF_AGENT_HOST_MODE", "agent"), "agent, or review in the reviewer container")
	keyFile := fs.String("key-file", env("SF_AGENT_HOST_KEY_FILE", ""), "a file holding the key every request but /healthz must carry, written by the engine; empty for none")
	if err := fs.Parse(args); err != nil {
		return AgentHostConfig{}, err
	}
	if fs.NArg() > 0 {
		return AgentHostConfig{}, fmt.Errorf("agent-host: unexpected argument %q", fs.Arg(0))
	}
	if _, _, err := net.SplitHostPort(*listen); err != nil {
		return AgentHostConfig{}, fmt.Errorf("agent-host: listen must be host:port, got %q", *listen)
	}
	if !filepath.IsAbs(*workspace) {
		return AgentHostConfig{}, errors.New("agent-host: set -workspace or SF_WORKSPACE to an absolute path")
	}
	n, err := strconv.Atoi(*maxProcs)
	if err != nil || n < 1 || n > 1024 {
		return AgentHostConfig{}, fmt.Errorf("agent-host: max-processes must be between 1 and 1024, got %q", *maxProcs)
	}
	cfg := AgentHostConfig{Listen: *listen, Workspace: filepath.Clean(*workspace), Engine: *engine, Git: *git, MaxProcesses: n}
	switch *mode {
	case "agent":
	case "review":
		cfg.Review = true
	default:
		return AgentHostConfig{}, fmt.Errorf("agent-host: mode must be agent or review, got %q", *mode)
	}
	if *agentsBin != "off" {
		if !filepath.IsAbs(*agentsBin) {
			return AgentHostConfig{}, errors.New("agent-host: -agents-bin must be an absolute path, or off")
		}
		cfg.AgentsBin = filepath.Clean(*agentsBin)
	}
	if *keyFile != "" {
		if !filepath.IsAbs(*keyFile) {
			return AgentHostConfig{}, errors.New("agent-host: -key-file must be an absolute path")
		}
		cfg.KeyFile = filepath.Clean(*keyFile)
	}
	if *socket != "off" && !cfg.Review {
		if !filepath.IsAbs(*socket) || len(*socket) > agenthost.MaxSocketPath {
			return AgentHostConfig{}, fmt.Errorf("agent-host: the plugin socket must be an absolute path of at most %d bytes, or off", agenthost.MaxSocketPath)
		}
		if _, _, err := net.SplitHostPort(*engine); err != nil {
			return AgentHostConfig{}, fmt.Errorf("agent-host: engine must be host:port, got %q", *engine)
		}
		cfg.PluginSocket = *socket
	}
	return cfg, nil
}

func ServeAgentHost(ctx context.Context, args []string, logger shared.Logger) error {
	if err := agenthost.ProtectMemory(); err != nil {
		return err
	}
	cfg, err := ParseAgentHostConfig(args, os.Getenv)
	if err != nil {
		return err
	}
	host, err := agenthost.New(agenthost.Config{
		Workspace: cfg.Workspace, AgentsBin: cfg.AgentsBin, PluginSocket: cfg.PluginSocket, Git: cfg.Git, MaxProcesses: cfg.MaxProcesses,
		Review: cfg.Review, KeyFile: cfg.KeyFile, Environ: os.Environ(), Logger: logger,
	})
	if err != nil {
		return err
	}
	defer host.Close()
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("agent-host: listen: %w", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, 2)
	if cfg.PluginSocket != "" {
		relay, err := agenthost.Relay(cfg.Engine, logger)
		if err != nil {
			_ = ln.Close()
			return err
		}
		sock, err := agenthost.ListenPlugin(cfg.PluginSocket)
		if err != nil {
			_ = ln.Close()
			return err
		}
		defer func() { _ = os.Remove(cfg.PluginSocket) }()
		go func() { errs <- agenthost.ServeRelay(ctx, sock, relay) }()
	}
	srv := &http.Server{
		Handler:           host.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       time.Minute,
		MaxHeaderBytes:    16 << 10,
	}
	go func() { errs <- srv.Serve(netutil.LimitListener(ln, cfg.MaxProcesses+32)) }()
	socket := cfg.PluginSocket
	if socket == "" {
		socket = "off"
	}
	mode := "agent"
	if cfg.Review {
		mode = "review"
	}
	logger.Info("agent-host listening", "mode", mode, "control", ln.Addr().String(), "plugin_socket", socket, "version", Version)
	var serveErr error
	select {
	case serveErr = <-errs:
	case <-ctx.Done():
	}
	cancel()
	shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	_ = srv.Shutdown(shutdown)
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	return nil
}

func ServeMCP(ctx context.Context, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("mcp: unexpected argument %q", args[0])
	}
	socket := os.Getenv(agenthost.SocketVariable)
	if socket == "" {
		socket = agenthost.DefaultSocket
	}
	ask := agenthost.SocketAsker(socket, os.Getenv("SF_PUSH_TOKEN"))
	return agenthost.NewMCP(ask, Version, os.Stdout).Serve(ctx, os.Stdin)
}
