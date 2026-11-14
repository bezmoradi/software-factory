package docker

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"
	"syscall"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const (
	resolvConf = "/etc/resolv.conf"
	maxServers = 3
	dnsFor     = 3 * time.Second
	quicProbe  = 1200
)

var OutwardUDP = []string{"8.8.8.8:53", "1.1.1.1:53", "1.1.1.1:443"}

func DNSCheck(name string) string { return "dns:" + name }

func UDPCheck(address string) string { return "udp:" + address }

func RandomLabel(outside string) string { return "*." + outside }

func nameServers() []string {
	f, err := os.Open(resolvConf)
	if err != nil {
		return []string{"127.0.0.1"}
	}
	defer func() { _ = f.Close() }()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() && len(out) < maxServers {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "nameserver" {
			if a, err := netip.ParseAddr(fields[1]); err == nil {
				out = append(out, a.WithZone("").String())
			}
		}
	}
	if len(out) == 0 {
		return []string{"127.0.0.1"}
	}
	return out
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func query(name string) ([]byte, uint16, error) {
	n, err := dnsmessage.NewName(strings.TrimSuffix(name, ".") + ".")
	if err != nil {
		return nil, 0, err
	}
	var idb [2]byte
	_, _ = rand.Read(idb[:])
	id := binary.BigEndian.Uint16(idb[:])
	msg := dnsmessage.Message{
		ID: id, RecursionDesired: true,
		Questions: []dnsmessage.Question{{Name: n, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}},
	}
	packed, err := msg.Pack()
	return packed, id, err
}

type dnsAnswer struct {
	got     bool
	rcode   dnsmessage.RCode
	answers int
}

func ask(ctx context.Context, d Dialer, server, name string) (dnsAnswer, error) {
	packed, id, err := query(name)
	if err != nil {
		return dnsAnswer{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, dnsFor)
	defer cancel()
	conn, err := d.DialContext(ctx, "udp", net.JoinHostPort(server, "53"))
	if err != nil {
		return dnsAnswer{}, nil
	}
	defer func() { _ = conn.Close() }()
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	if _, err := conn.Write(packed); err != nil {
		return dnsAnswer{}, nil
	}
	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return dnsAnswer{}, nil
		}
		var p dnsmessage.Parser
		h, err := p.Start(buf[:n])
		if err != nil || h.ID != id || !h.Response {
			continue
		}
		_ = p.SkipAllQuestions()
		answers, _ := p.AllAnswers()
		return dnsAnswer{got: true, rcode: h.RCode, answers: len(answers)}, nil
	}
}

func unforwarded(a dnsAnswer) bool {
	return a.rcode == dnsmessage.RCodeServerFailure && a.answers == 0
}

func lookupOutside(ctx context.Context, d Dialer, servers []string, name, shown string) Result {
	for _, s := range servers {
		a, err := ask(ctx, d, s, name)
		if err != nil {
			return Result{Detail: "the probe couldn't build a DNS query for " + shown}
		}
		if a.got && !unforwarded(a) {
			return Result{Detail: fmt.Sprintf("a DNS lookup of %s from the probed container got an answer from %s (%s, %d records), so DNS queries leave the sandbox", shown, s, rcodeWord(a.rcode), a.answers)}
		}
	}
	return Result{OK: true, Detail: "no DNS answer for " + shown + " from outside the sandbox"}
}

func rcodeWord(r dnsmessage.RCode) string {
	switch r {
	case dnsmessage.RCodeSuccess:
		return "an answer"
	case dnsmessage.RCodeNameError:
		return "no such name"
	case dnsmessage.RCodeServerFailure:
		return "server failure"
	case dnsmessage.RCodeRefused:
		return "refused"
	}
	return fmt.Sprintf("code %d", r)
}

func NoRoute(err error) bool {
	return errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM)
}

func udpOut(ctx context.Context, d Dialer, cfg ProbeConfig, address string) Result {
	ctx, cancel := context.WithTimeout(ctx, cfg.Dial)
	defer cancel()
	conn, err := d.DialContext(ctx, "udp", address)
	if err != nil {
		if NoRoute(err) {
			return Result{OK: true, Detail: "no route to " + address + " over UDP"}
		}
		return Result{Detail: "the probe's UDP socket to " + address + " failed with " + errWord(err) + ", not with no route"}
	}
	defer func() { _ = conn.Close() }()
	payload := make([]byte, quicProbe)
	if strings.HasSuffix(address, ":53") {
		payload, _, _ = query("example.com")
	} else {
		payload[0] = 0xc0
		copy(payload[1:5], []byte{0x1a, 0x2a, 0x3a, 0x4a})
		payload[5] = 8
		_, _ = rand.Read(payload[6:14])
	}
	if _, err := conn.Write(payload); err != nil && NoRoute(err) {
		return Result{OK: true, Detail: "no route to " + address + " over UDP"}
	}
	return Result{Detail: "the probed container has a route to " + address + " over UDP"}
}

func errWord(err error) string {
	var ne net.Error
	switch {
	case errors.As(err, &ne) && ne.Timeout():
		return "a timeout"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused"
	case errors.Is(err, syscall.ECONNRESET):
		return "connection reset"
	}
	return "another error"
}
