package egress

import (
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	ReasonBadRequest     = "bad_request"
	ReasonMethod         = "method"
	ReasonBadTarget      = "bad_target"
	ReasonPort           = "port"
	ReasonIPLiteral      = "ip_literal"
	ReasonInvalidHost    = "invalid_host"
	ReasonGitHost        = "git_host"
	ReasonNotAllowlisted = "not_allowlisted"
	ReasonPrivateAddress = "private_address"
	ReasonResolveFailed  = "resolve_failed"
	ReasonDialFailed     = "dial_failed"
	ReasonLimit          = "limit"
	ReasonSNIMismatch    = "sni_mismatch"
	ReasonNoSNI          = "no_sni"
	ReasonECH            = "ech"
	ReasonNotTLS         = "not_tls"
	ReasonOwnNetwork     = "own_network"
)

var gitHosts = []string{"github.com", "githubusercontent.com", "gitlab.com", "bitbucket.org"}

type Policy struct {
	allow   []string
	ports   []string
	deny    []string
	git     []string
	expires map[string]time.Time
}

type Source interface {
	Current() Policy
}

func (p Policy) Current() Policy { return p }

func (p Policy) WithAllow(allow []string, expires map[string]time.Time) (Policy, error) {
	return p.withGrant(allow, nil, nil, expires)
}

func (p Policy) withGrant(allow, ports, git []string, expires map[string]time.Time) (Policy, error) {
	out := Policy{deny: slices.Clone(p.deny), expires: map[string]time.Time{}}
	opened := map[string]bool{}
	for _, entry := range ports {
		host, _, ok := session.SplitPortTarget(entry)
		if !ok || !session.ValidPortEntry(entry) || normalize(host) != host {
			return Policy{}, fmt.Errorf("egress: a port grant names %q, which isn't a host and port egress forwards", entry)
		}
		out.ports = append(out.ports, entry)
	}
	out.ports = sortedUnique(out.ports)
	for _, entry := range git {
		if slices.Contains(out.ports, entry) {
			opened[entry] = true
			out.git = append(out.git, entry)
			continue
		}
		name, err := entryName(entry)
		if err != nil || !slices.ContainsFunc(allow, func(a string) bool { return normalize(strings.TrimSpace(a)) == name }) {
			return Policy{}, fmt.Errorf("egress: a git grant names %q, which isn't in the allowlist", entry)
		}
		opened[name] = true
		out.git = append(out.git, name)
	}
	out.git = sortedUnique(out.git)
	for _, entry := range out.ports {
		host, _, _ := session.SplitPortTarget(entry)
		if _, err := netip.ParseAddr(host); err != nil && gitHost(out.deny, host) && !opened[entry] {
			return Policy{}, fmt.Errorf("egress: a port grant names %q, a refused git host, without a git grant", entry)
		}
	}
	for _, entry := range allow {
		name, err := entryName(entry)
		if err != nil {
			return Policy{}, fmt.Errorf("egress: allowlist: %w", err)
		}
		if gitHost(out.deny, name) && !opened[name] {
			return Policy{}, fmt.Errorf("egress: allowlist: %q names or covers a refused git host", entry)
		}
		out.allow = append(out.allow, name)
	}
	out.allow = sortedUnique(out.allow)
	for entry, t := range expires {
		if slices.Contains(out.ports, entry) && !t.IsZero() {
			out.expires[entry] = t
			continue
		}
		name, err := entryName(entry)
		if err != nil || !slices.Contains(out.allow, name) || t.IsZero() {
			return Policy{}, fmt.Errorf("egress: an expiry names %q, which isn't in the allowlist", entry)
		}
		out.expires[name] = t
	}
	return out, nil
}

func (p Policy) Expires() map[string]time.Time {
	out := map[string]time.Time{}
	maps.Copy(out, p.expires)
	return out
}

func gitHost(deny []string, entry string) bool {
	bare, wild := strings.CutPrefix(entry, ".")
	for _, d := range deny {
		if bare == d || strings.HasSuffix(bare, "."+d) || (wild && strings.HasSuffix(d, "."+bare)) {
			return true
		}
	}
	return false
}

func NewPolicy(allow, extraDeny []string) (Policy, error) {
	var p Policy
	for _, entry := range allow {
		name, err := entryName(entry)
		if err != nil {
			return Policy{}, fmt.Errorf("egress: allowlist: %w", err)
		}
		p.allow = append(p.allow, name)
	}
	p.deny = append(p.deny, gitHosts...)
	for _, entry := range extraDeny {
		name, err := entryName(entry)
		if err != nil {
			return Policy{}, fmt.Errorf("egress: denied hosts: %w", err)
		}
		p.deny = append(p.deny, strings.TrimPrefix(name, "."))
	}
	p.allow = sortedUnique(p.allow)
	p.deny = sortedUnique(p.deny)
	return p, nil
}

func (p Policy) Allow() []string {
	return slices.Clone(p.allow)
}

func (p Policy) Deny() []string {
	return slices.Clone(p.deny)
}

func (p Policy) Git() []string {
	return slices.Clone(p.git)
}

func (p Policy) Ports() []string {
	return slices.Clone(p.ports)
}

func (p Policy) portAllowed(target string) bool {
	if !p.exact(p.ports, target) {
		return false
	}
	host, _, _ := session.SplitPortTarget(target)
	if _, err := netip.ParseAddr(host); err == nil || !p.denied(host) {
		return true
	}
	return p.exact(p.git, target)
}

func (p Policy) exact(list []string, entry string) bool {
	if !slices.Contains(list, entry) {
		return false
	}
	t, ok := p.expires[entry]
	return !ok || time.Now().Before(t)
}

func (p Policy) Check(host string) string {
	name := normalize(host)
	if _, err := netip.ParseAddr(name); err == nil {
		return ReasonIPLiteral
	}
	if numeric(name) {
		return ReasonIPLiteral
	}
	if !validName(name) {
		return ReasonInvalidHost
	}
	if p.denied(name) {
		return ReasonGitHost
	}
	if !p.allowed(name) {
		return ReasonNotAllowlisted
	}
	return ""
}

func (p Policy) denied(name string) bool {
	for _, d := range p.deny {
		if name == d || strings.HasSuffix(name, "."+d) {
			return true
		}
	}
	return false
}

func (p Policy) allowed(name string) bool {
	return p.covered(p.allow, name)
}

func (p Policy) gitAllowed(name string) bool {
	return p.covered(p.git, name)
}

func (p Policy) covered(list []string, name string) bool {
	now := time.Now()
	for _, a := range list {
		if t, ok := p.expires[a]; ok && !now.Before(t) {
			continue
		}
		if strings.HasPrefix(a, ".") {
			if strings.HasSuffix(name, a) {
				return true
			}
		} else if name == a {
			return true
		}
	}
	return false
}

func entryName(entry string) (string, error) {
	name := normalize(strings.TrimSpace(entry))
	bare := strings.TrimPrefix(name, ".")
	if _, err := netip.ParseAddr(bare); err == nil || numeric(bare) {
		return "", fmt.Errorf("%q is an IP address", entry)
	}
	if !validName(bare) || !strings.Contains(bare, ".") {
		return "", fmt.Errorf("%q is not a host name with at least two labels", entry)
	}
	return name, nil
}

func normalize(host string) string {
	return strings.TrimSuffix(strings.ToLower(host), ".")
}

func numeric(name string) bool {
	last := name[strings.LastIndexByte(name, '.')+1:]
	return last != "" && last[0] >= '0' && last[0] <= '9'
}

func validName(name string) bool {
	if name == "" || len(name) > 253 {
		return false
	}
	for label := range strings.SplitSeq(name, ".") {
		if !validLabel(label) {
			return false
		}
	}
	return true
}

func validLabel(label string) bool {
	if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
		return false
	}
	for _, c := range []byte(label) {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

func sortedUnique(names []string) []string {
	out := slices.Clone(names)
	if out == nil {
		out = []string{}
	}
	slices.Sort(out)
	return slices.Compact(out)
}
