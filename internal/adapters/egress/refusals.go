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
	MaxMarkers    = 8
	MaxRings      = 32
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
	mu     sync.Mutex
	byMark map[string]*marked
	rings  map[string][]string
	order  []string
}

func NewRefusals() *Refusals {
	return &Refusals{byMark: map[string]*marked{}, rings: map[string][]string{}}
}

func (r *Refusals) touch(ring string) {
	if i := slices.Index(r.order, ring); i >= 0 {
		r.order = slices.Delete(r.order, i, i+1)
	}
	r.order = append(r.order, ring)
}

func (r *Refusals) dropRing(ring string) {
	for _, mark := range r.rings[ring] {
		delete(r.byMark, mark)
	}
	delete(r.rings, ring)
	if i := slices.Index(r.order, ring); i >= 0 {
		r.order = slices.Delete(r.order, i, i+1)
	}
}

func (r *Refusals) add(mark string, f Refusal) {
	if r == nil || !markerPattern.MatchString(mark) {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	ring := f.Subnet
	m := r.byMark[mark]
	if m == nil {
		if _, known := r.rings[ring]; !known && len(r.rings) >= MaxRings && len(r.order) > 0 {
			r.dropRing(r.order[0])
		}
		markers := r.rings[ring]
		if len(markers) >= MaxMarkers {
			delete(r.byMark, markers[0])
			markers = slices.Delete(markers, 0, 1)
		}
		m = &marked{}
		r.byMark[mark] = m
		r.rings[ring] = append(markers, mark)
	}
	r.touch(ring)
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
