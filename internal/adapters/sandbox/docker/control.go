package docker

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	StatusVersion    = "2"
	ControlKeyHeader = "X-SF-Control-Key"
	maxSides         = 4
	maxSideEntries   = 256
	maxRefused       = 256 << 10
)

var reasonPattern = regexp.MustCompile(`^[a-z_]{1,32}$`)

type sideJSON struct {
	Allow   []string         `json:"allow"`
	Expires map[string]int64 `json:"expires"`
}

type statusJSON struct {
	Version string              `json:"version"`
	Boot    string              `json:"boot"`
	Set     bool                `json:"set"`
	Deny    []string            `json:"deny"`
	Sides   map[string]sideJSON `json:"sides"`
}

type setJSON struct {
	Version string              `json:"version"`
	Sides   map[string]sideJSON `json:"sides"`
}

type seenJSON struct {
	Host   string `json:"host"`
	Reason string `json:"reason"`
	Count  int    `json:"count"`
	Last   int64  `json:"last"`
}

type refusedJSON struct {
	Version string                `json:"version"`
	Sides   map[string][]seenJSON `json:"sides"`
}

type errPart string

func (e errPart) Error() string { return string(e) }

func listOf(s sideJSON) (session.EgressList, bool) {
	if s.Allow == nil || len(s.Allow) > maxSideEntries || len(s.Expires) > maxSideEntries {
		return session.EgressList{}, false
	}
	out := session.EgressList{Allow: sortedCopy(s.Allow), Expires: map[string]time.Time{}}
	for h, t := range s.Expires {
		if t <= 0 {
			return session.EgressList{}, false
		}
		out.Expires[h] = time.Unix(t, 0)
	}
	return out, true
}

func sideOf(l session.EgressList) sideJSON {
	out := sideJSON{Allow: sortedCopy(l.Allow), Expires: map[string]int64{}}
	for h, t := range l.Expires {
		out.Expires[h] = t.Unix()
	}
	return out
}

func readStatusFrom(ctx context.Context, client *http.Client, tc *tls.Config, addr string) (session.EgressState, error) {
	ctx, cancel := context.WithTimeout(ctx, statusFor)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, statusURL(tc, addr, "/status"), nil)
	if err != nil {
		return session.EgressState{}, errPart("the engine couldn't ask the egress proxy for its allowlist")
	}
	resp, err := client.Do(req)
	if err != nil {
		return session.EgressState{}, errPart("the egress proxy's status at " + addr + " didn't answer")
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxStatus+1))
	var st statusJSON
	if err != nil || resp.StatusCode != http.StatusOK || len(data) > maxStatus || json.Unmarshal(data, &st) != nil || st.Version != StatusVersion || st.Deny == nil || len(st.Sides) > maxSides {
		return session.EgressState{}, errPart("the egress proxy's status couldn't be read")
	}
	agents, okA := listOf(st.Sides[session.SideAgents])
	reviewers, okR := listOf(st.Sides[session.SideReviewers])
	if !okA || !okR {
		return session.EgressState{}, errPart("the egress proxy's status couldn't be read")
	}
	return session.EgressState{Starting: !st.Set, Lists: session.EgressLists{Agents: agents, Reviewers: reviewers}, Deny: sortedCopy(st.Deny)}, nil
}

func (c *Checker) readStatus(ctx context.Context) (session.EgressState, error) {
	return readStatusFrom(ctx, c.cfg.Client, c.cfg.TLS, c.cfg.Status)
}

type Control struct {
	status string
	key    string
	tls    *tls.Config
	client *http.Client
}

var _ session.EgressControl = (*Control)(nil)

func NewControl(status, key string, tc *tls.Config) (*Control, error) {
	if _, _, err := net.SplitHostPort(status); err != nil {
		return nil, fmt.Errorf("egress control: %q must be host:port", status)
	}
	if len(key) != 64 {
		return nil, errors.New("egress control: needs a 64-digit key")
	}
	if tc == nil {
		return nil, errors.New("egress control: needs the pinned TLS certificate")
	}
	return &Control{status: status, key: key, tls: tc, client: statusClient(tc)}, nil
}

func (c *Control) do(ctx context.Context, method, path string, body []byte, limit int64) ([]byte, int, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, statusURL(c.tls, c.status, path), r)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set(ControlKeyHeader, c.key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, 0, errors.New("egress's control channel didn't answer")
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, resp.StatusCode, errors.New("egress's answer couldn't be read")
	}
	return data, resp.StatusCode, nil
}

func (c *Control) Status(ctx context.Context) (session.EgressState, error) {
	return readStatusFrom(ctx, c.client, c.tls, c.status)
}

func (c *Control) Set(ctx context.Context, lists session.EgressLists) error {
	body, err := json.Marshal(setJSON{Version: StatusVersion, Sides: map[string]sideJSON{session.SideAgents: sideOf(lists.Agents), session.SideReviewers: sideOf(lists.Reviewers)}})
	if err != nil {
		return err
	}
	_, code, err := c.do(ctx, http.MethodPut, "/allow", body, maxStatus)
	if err != nil {
		return err
	}
	if code != http.StatusOK {
		return errors.New("egress refused the lists with HTTP " + strconv.Itoa(code))
	}
	return nil
}

type boxesJSON struct {
	Version string   `json:"version"`
	Subnets []string `json:"subnets"`
}

func (c *Control) SetBoxes(ctx context.Context, subnets []netip.Prefix) error {
	q := boxesJSON{Version: StatusVersion, Subnets: make([]string, 0, len(subnets))}
	for _, p := range subnets {
		q.Subnets = append(q.Subnets, p.String())
	}
	body, err := json.Marshal(q)
	if err != nil {
		return err
	}
	_, code, err := c.do(ctx, http.MethodPut, "/boxes", body, maxStatus)
	if err != nil {
		return err
	}
	if code != http.StatusOK {
		return errors.New("egress refused the session containers' subnets with HTTP " + strconv.Itoa(code))
	}
	return nil
}

func (c *Control) Refused(ctx context.Context) ([]session.EgressRefusal, error) {
	data, code, err := c.do(ctx, http.MethodGet, "/refused", nil, maxRefused)
	if err != nil {
		return nil, err
	}
	var rep refusedJSON
	if code != http.StatusOK || json.Unmarshal(data, &rep) != nil || rep.Version != StatusVersion || len(rep.Sides) > maxSides {
		return nil, errors.New("egress's record of refusals couldn't be read")
	}
	var out []session.EgressRefusal
	for _, side := range []string{session.SideAgents, session.SideReviewers} {
		list := rep.Sides[side]
		if len(list) > maxSideEntries {
			list = list[:maxSideEntries]
		}
		for _, e := range list {
			if !hostLike(e.Host) || !reasonPattern.MatchString(e.Reason) || e.Count < 1 || e.Last <= 0 {
				continue
			}
			out = append(out, session.EgressRefusal{Side: side, Host: e.Host, Reason: e.Reason, Count: e.Count, Last: time.Unix(e.Last, 0)})
		}
	}
	return out, nil
}

func WriteControlKey(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("egress control: the key file must be an absolute path")
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	key := hex.EncodeToString(b)
	sum := sha256.Sum256([]byte(key))
	if err := writeShared(path, []byte(hex.EncodeToString(sum[:])+"\n"), 0o644); err != nil {
		return "", fmt.Errorf("egress control: write the key's hash: %w", err)
	}
	return key, nil
}
