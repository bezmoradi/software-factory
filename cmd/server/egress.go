package server

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bezmoradi/software-factory/internal/adapters/egress"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

type EgressConfig struct {
	Listen       string
	ReviewListen string
	StatusListen string
	Allow        []string
	Deny         []string
	Limits       egress.Limits
}

func ServeEgress(ctx context.Context, args []string, logger shared.Logger) error {
	cfg, err := ParseEgressConfig(args, os.Getenv)
	if err != nil {
		return err
	}
	policy, err := egress.NewPolicy(cfg.Allow, cfg.Deny)
	if err != nil {
		return err
	}
	proxyListener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("egress: listen: %w", err)
	}
	var reviewListener net.Listener
	if cfg.ReviewListen != "" {
		if reviewListener, err = net.Listen("tcp", cfg.ReviewListen); err != nil {
			_ = proxyListener.Close()
			return fmt.Errorf("egress: review listen: %w", err)
		}
	}
	statusListener, err := net.Listen("tcp", cfg.StatusListen)
	if err != nil {
		_ = proxyListener.Close()
		if reviewListener != nil {
			_ = reviewListener.Close()
		}
		return fmt.Errorf("egress: status listen: %w", err)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	status := &http.Server{
		Handler:           egress.StatusHandler(policy),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       time.Minute,
		MaxHeaderBytes:    8 << 10,
	}
	statusErr := make(chan error, 1)
	go func() {
		statusErr <- status.Serve(statusListener)
		cancel()
	}()

	logger.Info("egress listening", "proxy", proxyListener.Addr().String(), "status", statusListener.Addr().String(), "allow", len(policy.Allow()))
	reviewErr := make(chan error, 1)
	if reviewListener != nil {
		logger.Info("egress listening for reviewers", "proxy", reviewListener.Addr().String())
		reviewProxy := egress.New(policy, cfg.Limits, net.DefaultResolver, &net.Dialer{}, sided{logger, "reviewers"})
		go func() {
			reviewErr <- reviewProxy.Serve(ctx, reviewListener)
			cancel()
		}()
	} else {
		reviewErr <- nil
	}
	proxy := egress.New(policy, cfg.Limits, net.DefaultResolver, &net.Dialer{}, sided{logger, "agents"})
	proxyErr := proxy.Serve(ctx, proxyListener)
	cancel()
	if err := <-reviewErr; err != nil && proxyErr == nil {
		proxyErr = err
	}

	shutdownCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	_ = status.Shutdown(shutdownCtx)
	if err := <-statusErr; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("egress: status: %w", err)
	}
	return proxyErr
}

func ParseEgressConfig(args []string, getenv func(string) string) (EgressConfig, error) {
	env := func(key, fallback string) string {
		if v := getenv(key); v != "" {
			return v
		}
		return fallback
	}
	fs := flag.NewFlagSet("egress", flag.ContinueOnError)
	listen := fs.String("listen", env("EGRESS_LISTEN", ":3128"), "proxy listen address")
	reviewListen := fs.String("review-listen", env("EGRESS_REVIEW_LISTEN", ""), "a second proxy address with its own connection limit, for the reviewers; empty for none")
	statusListen := fs.String("status-listen", env("EGRESS_STATUS_LISTEN", ":3129"), "status endpoint listen address")
	allow := fs.String("allow", env("EGRESS_ALLOW", ""), "comma-separated allowed hosts; a leading dot allows subdomains only")
	deny := fs.String("deny", env("EGRESS_DENY", ""), "comma-separated extra git hosts, always refused with their subdomains")
	maxConns := fs.String("max-conns", env("EGRESS_MAX_CONNS", "256"), "maximum concurrent connections")
	idle := fs.String("idle-timeout", env("EGRESS_IDLE_TIMEOUT", "5m"), "close a tunnel after this long with no bytes either way")
	total := fs.String("max-duration", env("EGRESS_MAX_DURATION", "1h"), "close a tunnel after this long")
	if err := fs.Parse(args); err != nil {
		return EgressConfig{}, err
	}
	if fs.NArg() > 0 {
		return EgressConfig{}, fmt.Errorf("egress: unexpected argument %q", fs.Arg(0))
	}

	conns, err := strconv.Atoi(*maxConns)
	if err != nil || conns < 1 {
		return EgressConfig{}, fmt.Errorf("egress: max-conns must be a positive number, got %q", *maxConns)
	}
	idleTimeout, err := positiveDuration("idle-timeout", *idle)
	if err != nil {
		return EgressConfig{}, err
	}
	maxDuration, err := positiveDuration("max-duration", *total)
	if err != nil {
		return EgressConfig{}, err
	}
	return EgressConfig{
		Listen:       *listen,
		ReviewListen: *reviewListen,
		StatusListen: *statusListen,
		Allow:        list(*allow),
		Deny:         list(*deny),
		Limits: egress.Limits{
			MaxConns: conns,
			Idle:     idleTimeout,
			Total:    maxDuration,
			Header:   10 * time.Second,
			Dial:     10 * time.Second,
		},
	}, nil
}

func positiveDuration(name, value string) (time.Duration, error) {
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("egress: %s must be a positive duration, got %q", name, value)
	}
	return d, nil
}

func list(value string) []string {
	var out []string
	for item := range strings.SplitSeq(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

type sided struct {
	shared.Logger
	side string
}

func (l sided) Info(message string, args ...any) {
	l.Logger.Info(message, append(args, "side", l.side)...)
}

func (l sided) Warn(message string, args ...any) {
	l.Logger.Warn(message, append(args, "side", l.side)...)
}

func (l sided) Error(message string, args ...any) {
	l.Logger.Error(message, append(args, "side", l.side)...)
}
