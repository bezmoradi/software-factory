package boxes

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	maxBoxAnswer  = 64 << 10
	maxListAnswer = 1 << 20
	maxFacts      = 64
	maxFact       = 256
	maxBoxes      = 1024
)

type Client struct {
	addr   string
	key    string
	client *http.Client
}

var _ session.Boxes = (*Client)(nil)

func NewClient(addr, key string, tc *tls.Config) (*Client, error) {
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return nil, fmt.Errorf("box helper: %q must be host:port", addr)
	}
	if _, err := hex.DecodeString(key); err != nil || len(key) != 64 {
		return nil, errors.New("box helper: needs a 64-digit key")
	}
	if tc == nil || tc.MinVersion < tls.VersionTLS13 || tc.VerifyConnection == nil {
		return nil, errors.New("box helper: needs TLS 1.3 pinned to the certificate the engine wrote")
	}
	t := &http.Transport{
		Proxy:                  nil,
		DialContext:            (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		TLSClientConfig:        tc.Clone(),
		TLSHandshakeTimeout:    5 * time.Second,
		ResponseHeaderTimeout:  startFor + 30*time.Second,
		MaxResponseHeaderBytes: 16 << 10,
		MaxIdleConns:           4,
		IdleConnTimeout:        30 * time.Second,
		ForceAttemptHTTP2:      false,
	}
	return &Client{addr: addr, key: key, client: &http.Client{Transport: t, Timeout: startFor + time.Minute}}, nil
}

func (c *Client) do(ctx context.Context, method, path string, limit int64, wait time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, "https://"+c.addr+path, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", session.ErrBoxUnavailable, err)
	}
	req.Header.Set(KeyHeader, c.key)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w at %s", session.ErrBoxUnavailable, c.addr)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: its answer couldn't be read", session.ErrBoxUnavailable)
	}
	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent:
		return data, nil
	}
	var e errorJSON
	if json.Unmarshal(data, &e) != nil || e.Version != WireVersion {
		return nil, fmt.Errorf("%w with HTTP %d", session.ErrBoxRefused, resp.StatusCode)
	}
	msg := clipMessage(e.Message)
	switch e.Reason {
	case ReasonLimit:
		return nil, fmt.Errorf("%w: %s", session.ErrBoxLimit, msg)
	case ReasonRuntime:
		return nil, fmt.Errorf("%w: %s", session.ErrBoxRuntime, msg)
	case ReasonUnknown:
		return nil, fmt.Errorf("%w: %s", session.ErrBoxUnknown, msg)
	case ReasonKey, ReasonNoKey:
		return nil, fmt.Errorf("%w: %s", session.ErrBoxUnavailable, msg)
	}
	return nil, fmt.Errorf("%w (%s): %s", session.ErrBoxRefused, clipMessage(e.Reason), msg)
}

func (c *Client) Reachable(ctx context.Context) error {
	_, err := c.do(ctx, http.MethodGet, "/healthz", maxBoxAnswer, 5*time.Second)
	return err
}

func clipMessage(s string) string {
	s = oneLine(s)
	if len(s) > 400 {
		s = s[:400]
	}
	return s
}

func boxPath(box string) (string, error) {
	if !session.ValidBox(box) {
		return "", fmt.Errorf("%w: a box is 16 lowercase hexadecimal digits", session.ErrBoxRefused)
	}
	return "/v1/boxes/" + box, nil
}

func (c *Client) Create(ctx context.Context, box string) error {
	p, err := boxPath(box)
	if err != nil {
		return err
	}
	_, err = c.do(ctx, http.MethodPut, p, maxBoxAnswer, otherFor+30*time.Second)
	return err
}

func (c *Client) Start(ctx context.Context, box string) (session.BoxInfo, error) {
	p, err := boxPath(box)
	if err != nil {
		return session.BoxInfo{}, err
	}
	data, err := c.do(ctx, http.MethodPost, p+"/start", maxBoxAnswer, startFor+30*time.Second)
	if err != nil {
		return session.BoxInfo{}, err
	}
	var a answerJSON
	if json.Unmarshal(data, &a) != nil || a.Version != WireVersion || a.Box == nil {
		return session.BoxInfo{}, fmt.Errorf("%w: its answer isn't one the engine reads", session.ErrBoxUnavailable)
	}
	info, ok := infoFrom(*a.Box)
	if !ok || info.Box != box || !info.Agent.IsValid() {
		return session.BoxInfo{}, fmt.Errorf("%w: its answer isn't one the engine reads", session.ErrBoxUnavailable)
	}
	return info, nil
}

func (c *Client) Stop(ctx context.Context, box string) error {
	p, err := boxPath(box)
	if err != nil {
		return err
	}
	_, err = c.do(ctx, http.MethodPost, p+"/stop", maxBoxAnswer, otherFor+30*time.Second)
	return err
}

func (c *Client) Remove(ctx context.Context, box string) error {
	p, err := boxPath(box)
	if err != nil {
		return err
	}
	_, err = c.do(ctx, http.MethodDelete, p, maxBoxAnswer, otherFor+30*time.Second)
	return err
}

func (c *Client) List(ctx context.Context) ([]session.BoxInfo, error) {
	data, err := c.do(ctx, http.MethodGet, "/v1/boxes", maxListAnswer, otherFor+30*time.Second)
	if err != nil {
		return nil, err
	}
	var l listJSON
	if json.Unmarshal(data, &l) != nil || l.Version != WireVersion || l.Boxes == nil || len(l.Boxes) > maxBoxes {
		return nil, fmt.Errorf("%w: its list isn't one the engine reads", session.ErrBoxUnavailable)
	}
	out := make([]session.BoxInfo, 0, len(l.Boxes))
	for _, b := range l.Boxes {
		info, ok := infoFrom(b)
		if !ok {
			return nil, fmt.Errorf("%w: its list isn't one the engine reads", session.ErrBoxUnavailable)
		}
		out = append(out, info)
	}
	return out, nil
}

func infoFrom(b boxJSON) (session.BoxInfo, bool) {
	if !session.ValidBox(b.Box) || len(b.State) > 32 || len(b.Recipe) > 64 || len(b.Facts) > maxFacts {
		return session.BoxInfo{}, false
	}
	info := session.BoxInfo{Box: b.Box, State: oneLine(b.State), Recipe: b.Recipe}
	if b.Recipe != "" && !validRecipeLabel(b.Recipe) {
		return session.BoxInfo{}, false
	}
	for _, f := range b.Facts {
		if len(f) > maxFact || !strings.Contains(f, "=") {
			return session.BoxInfo{}, false
		}
		info.Facts = append(info.Facts, oneLine(f))
	}
	if b.Subnet == "" {
		return info, b.Host == "" && b.Engine == "" && b.Agent == "" && b.Gateway == ""
	}
	subnet, err := netip.ParsePrefix(b.Subnet)
	if err != nil || !subnet.Addr().Is4() || subnet.Bits() != boxBits || subnet.Masked() != subnet {
		return session.BoxInfo{}, false
	}
	addrs := []*netip.Addr{&info.Engine, &info.Agent, &info.Gateway}
	for i, s := range []string{b.Engine, b.Agent, b.Gateway} {
		a, err := netip.ParseAddr(s)
		if err != nil || !subnet.Contains(a) {
			return session.BoxInfo{}, false
		}
		*addrs[i] = a
	}
	if info.Engine != engineOf(subnet) || info.Agent != agentOf(subnet) {
		return session.BoxInfo{}, false
	}
	host, port, err := net.SplitHostPort(b.Host)
	if err != nil || host != info.Agent.String() || port != agentHostPort {
		return session.BoxInfo{}, false
	}
	info.Subnet, info.Host = subnet, b.Host
	return info, true
}
