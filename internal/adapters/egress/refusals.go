package egress

import (
	"net"
	"net/http"
	"regexp"
	"slices"
	"sync"
)

const (
	MarkerHeader  = "Software-Factory-Probe"
	MaxMarkers    = 32
	MaxPerMarker  = 256
	maxTargetSize = 300
)

var markerPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

type Refusal struct {
	Side   string
	Target string
	Name   string
	Reason string
	Subnet string
}

type marked struct {
	refusals []Refusal
	dropped  int
}

type Refusals struct {
	mu      sync.Mutex
	byMark  map[string]*marked
	markers []string
}

func NewRefusals() *Refusals {
	return &Refusals{byMark: map[string]*marked{}}
}

func (r *Refusals) add(mark string, f Refusal) {
	if r == nil || !markerPattern.MatchString(mark) {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.byMark[mark]
	if m == nil {
		if len(r.markers) >= MaxMarkers {
			delete(r.byMark, r.markers[0])
			r.markers = slices.Delete(r.markers, 0, 1)
		}
		m = &marked{}
		r.byMark[mark] = m
		r.markers = append(r.markers, mark)
	}
	if len(m.refusals) >= MaxPerMarker {
		m.dropped++
		return
	}
	m.refusals = append(m.refusals, f)
}

func (r *Refusals) Of(mark string) ([]Refusal, int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.byMark[mark]
	if m == nil {
		return nil, 0, false
	}
	return slices.Clone(m.refusals), m.dropped, true
}

type Marks struct {
	Book   *Refusals
	Side   string
	Seen   *Seen
	subnet string
}

func (m Marks) from(subnet string) Marks {
	m.subnet = subnet
	return m
}

func (m Marks) saw(host, reason string) {
	m.Seen.add(host, reason)
}

func (m Marks) note(mark, target, name, reason string) {
	if m.Book == nil || mark == "" {
		return
	}
	m.Book.add(mark, Refusal{Side: m.Side, Target: clipTarget(target), Name: clipTarget(name), Reason: reason, Subnet: m.subnet})
}

func markOf(req *http.Request) string {
	values := req.Header.Values(MarkerHeader)
	if len(values) != 1 || !markerPattern.MatchString(values[0]) {
		return ""
	}
	return values[0]
}

func requestedTarget(authority string) string {
	host, port, err := net.SplitHostPort(authority)
	if err != nil {
		return logged(authority)
	}
	return net.JoinHostPort(logged(host), port)
}

func clipTarget(s string) string {
	if len(s) > maxTargetSize {
		return s[:maxTargetSize]
	}
	return s
}
