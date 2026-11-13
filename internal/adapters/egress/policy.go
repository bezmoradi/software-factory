package egress

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"
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
)

var gitHosts = []string{"github.com", "githubusercontent.com", "gitlab.com", "bitbucket.org"}

type Policy struct {
	allow []string
	deny  []string
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
	for _, a := range p.allow {
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
