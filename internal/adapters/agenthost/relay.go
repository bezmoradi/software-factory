package agenthost

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/net/netutil"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	MaxSocketPath  = 103
	MaxRelayBody   = 8 << 20
	RelayConns     = 256
	relayHeaderFor = 60 * time.Second
)

func ListenPlugin(path string) (net.Listener, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("agent-host: the plugin socket must be an absolute, clean path")
	}
	if len(path) > MaxSocketPath {
		return nil, fmt.Errorf("agent-host: the plugin socket path is %d bytes; it must be at most %d", len(path), MaxSocketPath)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("agent-host: create the plugin socket's folder: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ownedByMe(info) {
		return nil, errors.New("agent-host: the plugin socket's folder must be a folder this user owns, not a symbolic link")
	}
	if info.Mode().Perm() != 0o700 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return nil, errors.New("agent-host: the plugin socket's folder must be mode 0700")
		}
	}
	if old, err := os.Lstat(path); err == nil {
		if old.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("agent-host: something other than a socket is at the plugin socket's path")
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("agent-host: remove the old plugin socket: %w", err)
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("agent-host: listen on the plugin socket: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("agent-host: set the plugin socket's mode: %w", err)
	}
	return netutil.LimitListener(ln, RelayConns), nil
}

func Relay(engine string, logger shared.Logger) (http.Handler, error) {
	if _, _, err := net.SplitHostPort(engine); err != nil {
		return nil, fmt.Errorf("agent-host: the engine's agent address must be host:port, got %q", engine)
	}
	if logger == nil {
		logger = quiet{}
	}
	target := &url.URL{Scheme: "http", Host: engine}
	transport := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		ResponseHeaderTimeout: relayHeaderFor,
		MaxIdleConnsPerHost:   32,
		MaxConnsPerHost:       RelayConns,
		IdleConnTimeout:       time.Minute,
		DisableCompression:    true,
		Proxy:                 nil,
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.URL.Path = pr.In.URL.Path
			pr.Out.URL.RawPath = pr.In.URL.RawPath
			pr.Out.URL.RawQuery = pr.In.URL.RawQuery
			pr.Out.Host = pr.In.Host
		},
		Transport: transport,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if r.Context().Err() != nil || errors.Is(err, context.Canceled) {
				logger.Info("plugin relay", "event", "client-gone")
				return
			}
			if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
				logger.Warn("plugin relay", "event", "refused", "why", "body")
				send(w, http.StatusRequestEntityTooLarge, problemJSON{Error: "body"})
				return
			}
			logger.Warn("plugin relay", "event", "engine-unreachable", "why", relayFailure(err))
			send(w, http.StatusBadGateway, problemJSON{Error: "engine"})
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if browserHeaders(r) {
			logger.Warn("plugin relay", "event", "refused", "why", "browser")
			send(w, http.StatusForbidden, problemJSON{Error: "browser"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, MaxRelayBody)
		proxy.ServeHTTP(w, r)
	}), nil
}

func relayFailure(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if ne, ok := errors.AsType[net.Error](err); ok && ne.Timeout() {
		return "timeout"
	}
	if oe, ok := errors.AsType[*net.OpError](err); ok && oe.Op == "dial" {
		return "dial"
	}
	return "other"
}

func ServeRelay(ctx context.Context, ln net.Listener, h http.Handler) error {
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       time.Minute,
		MaxHeaderBytes:    16 << 10,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
