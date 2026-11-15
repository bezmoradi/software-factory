package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/net/netutil"

	"github.com/bezmoradi/software-factory/internal/adapters/egress"
	"github.com/bezmoradi/software-factory/internal/adapters/sandbox/boxes"
	"github.com/bezmoradi/software-factory/internal/adapters/sandbox/docker"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	DefaultHelperListen  = ":8099"
	DefaultHelperKeyFile = "/run/sf-helper/key"
	helperConns          = 16
	helperStopFor        = 55 * time.Second
)

type HelperConfig struct {
	Listen  string
	KeyFile string
	Boxes   boxes.Config
}

func ParseHelperConfig(args []string, getenv func(string) string) (HelperConfig, error) {
	if len(args) > 0 {
		return HelperConfig{}, fmt.Errorf("box-helper: unexpected argument %q", args[0])
	}
	cfg := HelperConfig{Listen: DefaultHelperListen, KeyFile: DefaultHelperKeyFile}
	if v := getenv("SF_HELPER_LISTEN"); v != "" {
		if _, _, err := net.SplitHostPort(v); err != nil {
			return HelperConfig{}, fmt.Errorf("SF_HELPER_LISTEN must be host:port, got %q", v)
		}
		cfg.Listen = v
	}
	if v := getenv("SF_HELPER_KEY_FILE"); v != "" {
		k, err := helperKeyFile(v)
		if err != nil {
			return HelperConfig{}, err
		}
		cfg.KeyFile = k
	}
	b, err := boxes.ParseConfig(getenv)
	if err != nil {
		return HelperConfig{}, err
	}
	cfg.Boxes = b
	return cfg, nil
}

func helperKeyFile(v string) (string, error) {
	if !filepath.IsAbs(v) || strings.ContainsAny(v, "\x00\n") {
		return "", errors.New("SF_HELPER_KEY_FILE must be an absolute path")
	}
	return filepath.Clean(v), nil
}

func ServeHelper(ctx context.Context, args []string, logger shared.Logger) error {
	cfg, err := ParseHelperConfig(args, os.Getenv)
	if err != nil {
		return err
	}
	helper, err := boxes.NewHelper(ctx, boxes.HelperConfig{
		Docker: boxes.NewDocker(boxes.SocketPath), Config: cfg.Boxes, Hostname: os.Getenv("HOSTNAME"), Logger: logger,
	})
	if err != nil {
		return err
	}
	tc, err := egress.ControlTLS(controlFile(cfg.KeyFile, ControlCertName), controlFile(cfg.KeyFile, ControlTLSKeyName))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go helper.Watch(ctx)
	if err := helper.Sweep(ctx); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("box-helper: listen: %w", err)
	}
	srv := &http.Server{
		Handler:           boxes.Handler(boxes.ServerConfig{Boxes: helper, Reviewers: helper, KeyFile: cfg.KeyFile, Base: ctx, Logger: logger, About: helper.About}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      4 * time.Minute,
		IdleTimeout:       time.Minute,
		MaxHeaderBytes:    boxes.MaxRequest,
	}
	errs := make(chan error, 1)
	go func() { errs <- srv.Serve(tls.NewListener(netutil.LimitListener(ln, helperConns), tc)) }()
	logger.Info("box helper listening", "address", ln.Addr().String(), "tls", "1.3", "version", Version)
	var serveErr error
	select {
	case serveErr = <-errs:
	case <-ctx.Done():
	}
	cancel()
	shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
	_ = srv.Shutdown(shutdown)
	stop()
	stopCtx, stopAll := context.WithTimeout(context.Background(), helperStopFor)
	defer stopAll()
	helper.StopAll(stopCtx)
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	return nil
}

func HelperBoxes(addr, keyFile string) (session.Boxes, error) {
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return nil, fmt.Errorf("SF_HELPER must be host:port, got %q", addr)
	}
	k, err := helperKeyFile(keyFile)
	if err != nil {
		return nil, err
	}
	tc, err := docker.WriteControlTLS(controlFile(k, ControlCertName), controlFile(k, ControlTLSKeyName), addr)
	if err != nil {
		return nil, fmt.Errorf("box helper: %w", err)
	}
	key, err := docker.WriteControlKey(k)
	if err != nil {
		return nil, fmt.Errorf("box helper: %w", err)
	}
	return boxes.NewClient(addr, key, tc)
}
