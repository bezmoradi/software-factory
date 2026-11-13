package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/bezmoradi/software-factory/cmd/server"
	"github.com/bezmoradi/software-factory/configs"
	"github.com/bezmoradi/software-factory/internal/adapters/loggers/slog"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

func main() {
	logger := slog.New()
	if len(os.Args) > 1 && (os.Args[1] == "mcp" || os.Args[1] == "sandbox-probe") {
		logger = slog.New("/dev/stderr")
	}
	if err := run(os.Args[1:], logger); err != nil {
		logger.Fatal(err)
	}
}

func run(args []string, logger shared.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	role := ""
	if len(args) > 0 {
		role = args[0]
	}
	switch role {
	case "", "egress", "install-agents":
		configs.LoadEnv()
	}
	if err := server.ApplyUmask(os.Getenv); err != nil {
		return err
	}
	switch role {
	case "":
		return server.Serve(ctx, logger)
	case "egress":
		return server.ServeEgress(ctx, args[1:], logger)
	case "agent-host":
		return server.ServeAgentHost(ctx, args[1:], logger)
	case "mcp":
		return server.ServeMCP(ctx, args[1:])
	case "sandbox-probe":
		return server.ServeProbe(ctx, args[1:], os.Stdout)
	case "install-agents":
		return server.InstallAgents(ctx, args[1:], logger)
	}
	return fmt.Errorf("unknown role %q", role)
}
