package docker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
)

var (
	policyRefusals = []string{"bad_target", "ip_literal", "invalid_host", "git_host", "not_allowlisted", "private_address"}
	portRefusals   = []string{"port"}
	nameRefusals   = []string{"sni_mismatch"}
	frontFailures  = []string{"resolve_failed", "dial_failed"}
)

type Refusal struct {
	Side   string `json:"side"`
	Target string `json:"target"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
	Subnet string `json:"subnet"`
}

type refusalsJSON struct {
	Version  string    `json:"version"`
	Refusals []Refusal `json:"refusals"`
	Dropped  int       `json:"dropped"`
}

type Expect struct {
	Check  string
	Target string
	Name   string
	Reason []string
	Or     *Expect
}

func newMarker() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func hostName(h string) string {
	return strings.TrimSuffix(strings.ToLower(h), ".")
}

func Expects(cfg ProbeConfig) []Expect {
	var out []Expect
	for _, h := range cfg.GitHosts {
		out = append(out,
			Expect{Check: ProxyCheck(h, "443"), Target: net.JoinHostPort(hostName(h), "443"), Reason: policyRefusals},
			Expect{Check: ProxyCheck(h, "22"), Target: net.JoinHostPort(hostName(h), "22"), Reason: portRefusals})
	}
	if cfg.Outside != "" {
		out = append(out, Expect{Check: ProxyCheck(cfg.Outside, "443"), Target: net.JoinHostPort(hostName(cfg.Outside), "443"), Reason: policyRefusals})
	}
	if cfg.Front != "" {
		front := net.JoinHostPort(hostName(cfg.Front), "443")
		for _, h := range cfg.GitHosts {
			out = append(out, Expect{Check: FrontCheck(cfg.Front, h), Target: front, Name: hostName(h), Reason: nameRefusals,
				Or: &Expect{Target: front, Reason: frontFailures}})
		}
	}
	return out
}

func (e Expect) matches(side, subnet string, r Refusal) bool {
	if r.Side == side && r.Subnet == subnet && r.Target == e.Target && r.Name == e.Name && slices.Contains(e.Reason, r.Reason) {
		return true
	}
	return e.Or != nil && e.Or.matches(side, subnet, r)
}

func Confirm(side string, subnet netip.Prefix, expected []Expect, got []Refusal, dropped int) []string {
	from := ""
	if subnet.IsValid() {
		from = subnet.String()
	}
	used := make([]bool, len(got))
	var missing []string
	for _, e := range expected {
		found := false
		for i, r := range got {
			if !used[i] && e.matches(side, from, r) {
				used[i], found = true, true
				break
			}
		}
		if !found {
			missing = append(missing, e.Check)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	part := "egress has no record of refusing the probe's requests " + clipList(missing) + ", so the probe's report isn't confirmed"
	if from != "" {
		part = "egress has no record of refusing the probe's requests from the session's network " + clipList(missing) + ", so the probe's report isn't confirmed"
	}
	if dropped > 0 {
		part += fmt.Sprintf(" (egress kept only the first %d of this check's records)", len(got))
	}
	return []string{part}
}

func (c *Checker) confirm(ctx context.Context, cfg ProbeConfig, subnet netip.Prefix) []string {
	ctx, cancel := context.WithTimeout(ctx, statusFor)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, statusURL(c.cfg.TLS, c.cfg.Status, "/refusals/"+cfg.Marker), nil)
	if err != nil {
		return []string{"the engine couldn't ask the egress proxy which of the probe's requests it refused"}
	}
	resp, err := c.cfg.Client.Do(req)
	if err != nil {
		return []string{"the egress proxy's status at " + c.cfg.Status + " didn't answer which of the probe's requests it refused"}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return []string{"egress has no record of the probe's requests, so the probe's report isn't confirmed"}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxRefusals+1))
	var rep refusalsJSON
	if err != nil || resp.StatusCode != http.StatusOK || len(data) > maxRefusals || json.Unmarshal(data, &rep) != nil || rep.Refusals == nil {
		return []string{"the egress proxy's record of the probe's requests couldn't be read"}
	}
	return Confirm(c.cfg.Side, subnet, Expects(cfg), rep.Refusals, rep.Dropped)
}
