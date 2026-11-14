package server

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/adapters/egress"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	dropLogEvery      = 100
	AgentsSide        = "agents"
	ReviewersSide     = "reviewers"
	ControlCertName   = "control-cert.pem"
	ControlTLSKeyName = "control-tls-key.pem"
)

func controlFile(keyFile, name string) string {
	return filepath.Join(filepath.Dir(keyFile), name)
}

type EgressConfig struct {
	Listen       string
	ReviewListen string
	StatusListen string
	KeyFile      string
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
	agentsBook, reviewersBook := egress.NewRefusals(), egress.NewRefusals()
	agentsSeen, reviewersSeen := egress.NewSeen(), egress.NewSeen()
	live := egress.NewLive(policy, AgentsSide, ReviewersSide)
	subnets := egress.NewSubnets()
	status := &http.Server{
		Handler: egress.StatusHandler(egress.StatusConfig{
			Live: live, Books: []*egress.Refusals{agentsBook, reviewersBook},
			Seen: map[string]*egress.Seen{AgentsSide: agentsSeen, ReviewersSide: reviewersSeen}, KeyFile: cfg.KeyFile, Subnets: subnets,
		}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       time.Minute,
		MaxHeaderBytes:    8 << 10,
	}
	if cfg.KeyFile != "" {
		tc, err := egress.ControlTLS(controlFile(cfg.KeyFile, ControlCertName), controlFile(cfg.KeyFile, ControlTLSKeyName))
		if err != nil {
			_ = proxyListener.Close()
			_ = statusListener.Close()
			if reviewListener != nil {
				_ = reviewListener.Close()
			}
			return err
		}
		statusListener = tls.NewListener(statusListener, tc)
	}
	statusErr := make(chan error, 1)
	go func() {
		statusErr <- status.Serve(statusListener)
		cancel()
	}()

	logger.Info("egress listening", "proxy", proxyListener.Addr().String(), "status", statusListener.Addr().String(), "allow", len(policy.Allow()), "control", cfg.KeyFile != "", "tls", cfg.KeyFile != "")
	reviewErr := make(chan error, 1)
	if reviewListener != nil {
		logger.Info("egress listening for reviewers", "proxy", reviewListener.Addr().String())
		reviewProxy := egress.New(live.Side(ReviewersSide), cfg.Limits, net.DefaultResolver, &net.Dialer{}, sided{logger, ReviewersSide}, egress.Marks{Book: reviewersBook, Side: ReviewersSide, Seen: reviewersSeen})
		go func() {
			reviewErr <- reviewProxy.Serve(ctx, reviewListener)
			cancel()
		}()
	} else {
		reviewErr <- nil
	}
	proxy := egress.New(live.Side(AgentsSide), cfg.Limits, net.DefaultResolver, &net.Dialer{}, sided{logger, AgentsSide}, egress.Marks{Book: agentsBook, Side: AgentsSide, Seen: agentsSeen})
	proxyErr := proxy.Serve(ctx, egress.AllowSubnets(proxyListener, subnets, dropCounter(logger, "agents")))
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
	listen := fs.String("listen", env("EGRESS_LISTEN", ":3130"), "the agents' proxy listen address; it answers only the session containers' subnets the engine sets")
	reviewListen := fs.String("review-listen", env("EGRESS_REVIEW_LISTEN", ""), "a second proxy address with its own connection limit, for the reviewers; empty for none")
	statusListen := fs.String("status-listen", env("EGRESS_STATUS_LISTEN", ":3129"), "status endpoint listen address")
	keyFile := fs.String("control-key-file", env("EGRESS_CONTROL_KEY_FILE", ""), "a file holding the SHA-256 of the engine's control key; empty for no control channel")
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
		KeyFile:      *keyFile,
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

func dropCounter(logger shared.Logger, side string) func() {
	var mu sync.Mutex
	n := 0
	return func() {
		mu.Lock()
		n++
		count := n
		mu.Unlock()
		if count == 1 || count%dropLogEvery == 0 {
			logger.Warn("egress", "event", "dropped", "why", "not a session container's subnet", "side", side, "count", count)
		}
	}
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
