package session

import (
	"net/netip"
	"net/url"
	"slices"
	"strings"
)

const (
	SideAgents        = "agents"
	SideReviewers     = "reviewers"
	MaxAddedHosts     = 64
	MaxHostName       = 253
	MaxGatewayURL     = 512
	maxGatewayPath    = 256
	GatewayKeyNote    = "Paste the gateway's key or token with its address for the reviewers; for Claude Code sessions, paste it in each session's sign-in panel, since each session runs in its own container. The engine passes it only to the container it was pasted for, which saves it for Claude Code's apiKeyHelper, as with a pasted API key, and the page never shows it again. Claude Code sends it in both the Authorization (Bearer) and x-api-key headers. Turning the gateway on for a side, changing its host, turning a side off or removing the gateway first removes the key saved there, an API key included, so no key reaches an address it wasn't pasted for."
	UploadsWarning    = "also accepts uploads, such as publishing a package, so an agent could send your code out through it."
	PyPIReadHostsNote = "For installs from PyPI, add only its read hosts, pypi.org and files.pythonhosted.org; upload.pypi.org takes uploads."
)

type AgentHosts struct {
	ID    string
	Name  string
	Hosts []string
}

func AgentHostTable() []AgentHosts {
	return []AgentHosts{
		{ID: "claude", Name: "Claude Code", Hosts: []string{"api.anthropic.com", "claude.ai", "claude.com", "platform.claude.com"}},
	}
}

func agentHostsOf(id string) []string {
	for _, a := range AgentHostTable() {
		if a.ID == id {
			return a.Hosts
		}
	}
	return nil
}

var uploadHosts = []string{
	"registry.npmjs.org", "registry.yarnpkg.com",
	"upload.pypi.org", "test.pypi.org",
	"crates.io", "rubygems.org", "www.nuget.org", "hex.pm", "pub.dev",
	"central.sonatype.com", "oss.sonatype.org", "s01.oss.sonatype.org",
	"docker.io", "index.docker.io", "registry-1.docker.io", "ghcr.io", "quay.io",
	"pastebin.com", "transfer.sh", "file.io", "0x0.st",
	"amazonaws.com", "blob.core.windows.net", "storage.googleapis.com", "pkg.dev", "azurecr.io",
}

var claudeVendorDomains = []string{"anthropic.com", "claude.ai", "claude.com", "statsig.com", "statsigapi.net", "sentry.io", "datadoghq.com"}

func ClaudeVendorHost(entry string) bool {
	bare, wild := strings.CutPrefix(entry, ".")
	for _, d := range claudeVendorDomains {
		if bare == d || strings.HasSuffix(bare, "."+d) || (wild && strings.HasSuffix(d, "."+bare)) {
			return true
		}
	}
	return false
}

var pypiHosts = []string{"pypi.org", "upload.pypi.org", "test.pypi.org", "files.pythonhosted.org"}

var PyPIReadHosts = []string{"pypi.org", "files.pythonhosted.org"}

func HostName(entry string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(entry)), ".")
}

func ValidAllowEntry(entry string) bool {
	bare := strings.TrimPrefix(entry, ".")
	if _, err := netip.ParseAddr(bare); err == nil {
		return false
	}
	if !validDNSName(bare) || !strings.Contains(bare, ".") {
		return false
	}
	last := bare[strings.LastIndexByte(bare, '.')+1:]
	return last != "" && (last[0] < '0' || last[0] > '9')
}

func validDNSName(name string) bool {
	if name == "" || len(name) > MaxHostName {
		return false
	}
	for label := range strings.SplitSeq(name, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range []byte(label) {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	return true
}

func EntryCovers(entry, host string) bool {
	if bare, wild := strings.CutPrefix(entry, "."); wild {
		return strings.HasSuffix(host, "."+bare)
	}
	return entry == host
}

func DeniedBy(deny []string, entry string) string {
	bare, wild := strings.CutPrefix(entry, ".")
	for _, d := range deny {
		if bare == d || strings.HasSuffix(bare, "."+d) || (wild && strings.HasSuffix(d, "."+bare)) {
			return d
		}
	}
	return ""
}

func UploadHost(entry string) bool {
	bare, wild := strings.CutPrefix(entry, ".")
	for _, h := range uploadHosts {
		if bare == h || strings.HasSuffix(bare, "."+h) || (wild && strings.HasSuffix(h, "."+bare)) {
			return true
		}
	}
	return false
}

func HostNote(typed string) string {
	if PyPIHost(HostName(typed)) {
		return PyPIReadHostsNote
	}
	return ""
}

func (r RefusalView) Note() string {
	switch {
	case len(r.Suggest) > 0:
		return PyPIReadHostsNote
	case r.Uploads:
		return r.Host + " also accepts uploads."
	}
	return ""
}

func PyPIHost(entry string) bool {
	bare, wild := strings.CutPrefix(entry, ".")
	if bare == "pythonhosted.org" || strings.HasSuffix(bare, ".pythonhosted.org") || bare == "pypi.org" || strings.HasSuffix(bare, ".pypi.org") {
		return true
	}
	if wild {
		return slices.ContainsFunc(pypiHosts, func(h string) bool { return strings.HasSuffix(h, "."+bare) })
	}
	return false
}

func CheckGatewayURL(raw string) (string, string, error) {
	raw = strings.TrimSpace(raw)
	bad := func(why string) (string, string, error) {
		return "", "", explained(invalid("gateway"), why)
	}
	if raw == "" || len(raw) > MaxGatewayURL {
		return bad("The gateway's base URL needs 1 to 512 characters.")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Opaque != "" {
		return bad("The gateway's base URL must start with https:// and name a host, such as https://llm-gateway.example.com.")
	}
	if u.User != nil {
		return bad("The gateway's base URL can't hold a user name or password. Paste its key or token in the key field instead.")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" {
		return bad("The gateway's base URL can't have a query (?) or a fragment (#).")
	}
	if p := u.Port(); p != "" && p != "443" {
		return bad("The gateway must answer on port 443: egress lets agents reach only that port.")
	}
	host := HostName(u.Hostname())
	if strings.HasPrefix(host, ".") || !ValidAllowEntry(host) {
		return bad("The gateway's base URL must name one host by a name with at least two labels, not an IP address or a leading dot for subdomains.")
	}
	if len(u.EscapedPath()) > maxGatewayPath || strings.ContainsAny(u.Path, "\x00\r\n\t ") {
		return bad("The gateway's base URL path is longer than 256 characters or holds spaces or control characters.")
	}
	out := url.URL{Scheme: "https", Host: host, Path: strings.TrimRight(u.Path, "/"), RawPath: strings.TrimRight(u.RawPath, "/")}
	return out.String(), host, nil
}

func sortedHosts(in []string) []string {
	out := slices.Clone(in)
	if out == nil {
		out = []string{}
	}
	slices.Sort(out)
	return slices.Compact(out)
}
