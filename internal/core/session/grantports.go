package session

import (
	"net"
	"net/netip"
	"strconv"
	"strings"
)

const (
	MaxPort        = 65535
	PortProxy      = "egress:3130"
	PortForwardsAt = "127.0.0.1"
	httpsPort      = 443
	ownNetworkWord = "on one of Software Factory's own networks"
)

var (
	cgnatRange    = netip.MustParsePrefix("100.64.0.0/10")
	metadataAddrs = []netip.Addr{netip.MustParseAddr("100.100.100.200"), netip.MustParseAddr("fd00:ec2::254")}
	reservedRange = []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("192.0.0.0/24"),
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("240.0.0.0/4"),
		netip.MustParsePrefix("::/96"),
		netip.MustParsePrefix("64:ff9b::/96"),
		netip.MustParsePrefix("64:ff9b:1::/48"),
		netip.MustParsePrefix("100::/64"),
		netip.MustParsePrefix("2001::/32"),
		netip.MustParsePrefix("2002::/16"),
		netip.MustParsePrefix("fec0::/10"),
	}
)

func PortTarget(host string, port int) string {
	return net.JoinHostPort(host, strconv.Itoa(port))
}

func SplitPortTarget(entry string) (string, int, bool) {
	host, p, err := net.SplitHostPort(entry)
	if err != nil || host == "" {
		return "", 0, false
	}
	n, err := strconv.Atoi(p)
	if err != nil || n < 1 || n > MaxPort || strconv.Itoa(n) != p {
		return "", 0, false
	}
	return host, n, true
}

func portEntry(entry string) bool {
	_, _, ok := SplitPortTarget(entry)
	return ok
}

func targetHost(entry string) string {
	if h, _, ok := SplitPortTarget(entry); ok {
		return h
	}
	return entry
}

func AddressNever(a netip.Addr) string {
	a = a.Unmap()
	switch {
	case !a.IsValid() || a.Zone() != "":
		return "not an address egress dials"
	case a.IsUnspecified():
		return "the unspecified address"
	case a.IsLoopback():
		return "a loopback address"
	case a == metadataAddrs[0] || a == metadataAddrs[1]:
		return "a cloud metadata address"
	case a.IsLinkLocalUnicast():
		return "a link-local address"
	case a.IsMulticast() || a.IsLinkLocalMulticast() || a.IsInterfaceLocalMulticast():
		return "a multicast address"
	}
	for _, p := range reservedRange {
		if p.Contains(a) {
			return "a reserved address"
		}
	}
	return ""
}

func LANAddress(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsPrivate() || cgnatRange.Contains(a)
}

func ValidPortEntry(entry string) bool {
	host, port, ok := SplitPortTarget(entry)
	if !ok {
		return false
	}
	if a, err := netip.ParseAddr(host); err == nil {
		return a.String() == host && AddressNever(a) == ""
	}
	return !strings.HasPrefix(host, ".") && ValidAllowEntry(host) && !strings.HasSuffix(host, ".local") && !ClaudeVendorHost(host) && port != httpsPort
}

func ValidBoundEntry(entry string) bool {
	return ValidAllowEntry(entry) || ValidPortEntry(entry)
}

func (g *Grants) neverWords(a netip.Addr) string {
	if why := AddressNever(a); why != "" {
		return why
	}
	if g.cfg.Stack != nil {
		for _, p := range g.cfg.Stack() {
			if p.Contains(a.Unmap()) {
				return ownNetworkWord
			}
		}
	}
	return ""
}

func (g *Grants) portRule(raw string, port int) (string, error) {
	host := HostName(raw)
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	if port < 1 || port > MaxPort {
		return host, onField("port", invalid("port", "A port from 1 to 65535."))
	}
	if a, err := netip.ParseAddr(host); err == nil {
		if a.Zone() != "" {
			return host, onField("host", invalid("host", "Give the address without a zone."))
		}
		host = a.Unmap().String()
		if why := g.neverWords(a); why != "" {
			return host, onField("host", conflict("never", "Never reachable: "+why+"."))
		}
		return host, nil
	}
	switch {
	case strings.HasPrefix(host, "."):
		return host, onField("host", invalid("host", "Give one host, not every name under a domain."))
	case !ValidAllowEntry(host):
		return host, onField("host", invalid("host", "Not a host name or an address."))
	case strings.HasSuffix(host, ".local"):
		return host, onField("host", invalid("host", "Give the machine's address, like 192.168.1.20."))
	case ClaudeVendorHost(host):
		return host, onField("host", conflict("vendor", "Claude Code's vendor hosts keep their own rule."))
	case port == httpsPort:
		return host, onField("port", invalid("port", "For HTTPS on 443, leave the port out."))
	}
	if err := g.cfg.Allow.refuseOutside(host); err != nil {
		return host, onField("host", err)
	}
	return host, nil
}

func (g *Grants) portTarget(host string, port int) accessTarget {
	target := PortTarget(host, port)
	a := accessTarget{host: host, port: port, gits: g.gitsOf([]string{target})}
	if addr, err := netip.ParseAddr(host); err == nil {
		if LANAddress(addr) {
			a.lans = []string{target}
		}
		if len(a.gits) > 0 {
			a.gitAddr = g.cfg.GitAddress(addr.Unmap())
		}
	}
	return a
}

func lanEntries(entries []string) []string {
	var out []string
	for _, e := range entries {
		h, _, ok := SplitPortTarget(e)
		if !ok {
			continue
		}
		if a, err := netip.ParseAddr(h); err == nil && LANAddress(a) {
			out = append(out, e)
		}
	}
	return out
}

func reachWords(entries []string) string {
	var web, tcp []string
	for _, e := range entries {
		if portEntry(e) {
			tcp = append(tcp, e)
		} else {
			web = append(web, e)
		}
	}
	switch {
	case len(tcp) == 0:
		return "HTTPS connections to " + hostList(web)
	case len(web) == 0:
		return "TCP connections to " + hostList(tcp)
	}
	return "HTTPS connections to " + hostList(web) + " and TCP connections to " + hostList(tcp)
}

func PortReachWords(host string, port int) string {
	p := strconv.Itoa(port)
	return "ssh to " + host + " goes through the proxy by itself; another TCP client reaches it at " + PortForwardsAt + ":" + p +
		" in your container, which agent-host forwards while the grant lasts unless another grant or something of yours already uses that port, or through the proxy with nc -X connect -x " + PortProxy + " " + host + " " + p
}
