package session

import (
	"crypto/subtle"
	"net/netip"
	"strconv"
	"sync"
	"time"
)

const (
	PreviewCodeFor = time.Minute

	saidPreviewStopped = "This session isn't running. Wake it first."
	saidPreviewOwned   = "Only a session this engine runs in its own container has a preview."
)

type PreviewOffer struct {
	Code  string
	Port  int
	Ports []int
}

type PreviewTarget struct {
	Addr    netip.AddrPort
	Allowed bool
	Stopped bool
}

type Previews struct {
	d      *Dispatcher
	now    func() time.Time
	mu     sync.Mutex
	sid    string
	box    string
	port   int
	code   string
	until  time.Time
	cookie string
}

func NewPreviews(d *Dispatcher, now func() time.Time) *Previews {
	if now == nil {
		now = time.Now
	}
	return &Previews{d: d, now: now}
}

func previewOff(i Info) string {
	switch {
	case !i.Verified || i.Kind != "background" || i.Box == "":
		return saidPreviewOwned
	case !i.Running:
		return saidPreviewStopped
	}
	return ""
}

func (d *Dispatcher) boxAddr(box string) (netip.Addr, bool) {
	if !d.boxed() || box == "" {
		return netip.Addr{}, false
	}
	info, ok := d.cfg.Routes.Info(box)
	if !ok || !info.Agent.IsValid() {
		return netip.Addr{}, false
	}
	return info.Agent, true
}

func (d *Dispatcher) portBase(sid string) int {
	if w := d.workOf(); w != nil {
		return w.Ports(sid)
	}
	return 0
}

func (p *Previews) Open(sid string, port int) (PreviewOffer, error) {
	box, ok := p.d.SessionBox(sid)
	if !ok {
		return PreviewOffer{}, notFound("session", "The engine has no session of that id.")
	}
	if _, up := p.d.boxAddr(box); !up {
		return PreviewOffer{}, conflict("stopped", saidPreviewStopped)
	}
	base := p.d.portBase(sid)
	if base == 0 {
		return PreviewOffer{}, conflict("ports", "This session has no ports of its own. Stop it and wake it again.")
	}
	if port == 0 {
		port = base
	}
	last := base + PortSpan - 1
	if port < base || port > last {
		return PreviewOffer{}, invalid("port", "Port "+strconv.Itoa(port)+" isn't one of this session's ports ("+strconv.Itoa(base)+" to "+strconv.Itoa(last)+").")
	}
	ports := make([]int, 0, PortSpan)
	for n := base; n <= last; n++ {
		ports = append(ports, n)
	}
	code := randomHex(32)
	p.mu.Lock()
	p.sid, p.box, p.port = sid, box, port
	p.code, p.until, p.cookie = code, p.now().Add(PreviewCodeFor), ""
	p.mu.Unlock()
	return PreviewOffer{Code: code, Port: port, Ports: ports}, nil
}

func (p *Previews) Redeem(code string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.code == "" || subtle.ConstantTimeCompare([]byte(code), []byte(p.code)) != 1 || !p.now().Before(p.until) {
		return "", false
	}
	p.code = ""
	p.cookie = randomHex(32)
	return p.cookie, true
}

func (p *Previews) Target(cookies []string) PreviewTarget {
	p.mu.Lock()
	sid, box, port, cookie := p.sid, p.box, p.port, p.cookie
	p.mu.Unlock()
	if cookie == "" {
		return PreviewTarget{}
	}
	matched := false
	for _, c := range cookies {
		if subtle.ConstantTimeCompare([]byte(c), []byte(cookie)) == 1 {
			matched = true
		}
	}
	if !matched {
		return PreviewTarget{}
	}
	target := PreviewTarget{Allowed: true}
	if now, ok := p.d.SessionBox(sid); !ok || now != box {
		target.Stopped = true
		return target
	}
	addr, up := p.d.boxAddr(box)
	if !up {
		target.Stopped = true
		return target
	}
	target.Addr = netip.AddrPortFrom(addr, uint16(port))
	return target
}
