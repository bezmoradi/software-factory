package egress

import (
	"slices"
	"sync"
	"time"
)

const (
	MaxSeen  = 200
	maxCount = 1_000_000_000
)

type SeenRefusal struct {
	Host   string
	Reason string
	Count  int
	Last   time.Time
}

type seenKey struct {
	host   string
	reason string
}

type Seen struct {
	mu      sync.Mutex
	entries map[seenKey]*SeenRefusal
	now     func() time.Time
}

func NewSeen() *Seen {
	return &Seen{entries: map[seenKey]*SeenRefusal{}, now: time.Now}
}

func (s *Seen) add(host, reason string) {
	if s == nil || host == "" || reason == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := seenKey{host: host, reason: reason}
	e := s.entries[k]
	if e == nil {
		if len(s.entries) >= MaxSeen {
			var oldest seenKey
			var at time.Time
			first := true
			for key, v := range s.entries {
				if first || v.Last.Before(at) {
					oldest, at, first = key, v.Last, false
				}
			}
			delete(s.entries, oldest)
		}
		e = &SeenRefusal{Host: host, Reason: reason}
		s.entries[k] = e
	}
	e.Count = min(e.Count+1, maxCount)
	e.Last = s.now()
}

func (s *Seen) List() []SeenRefusal {
	if s == nil {
		return []SeenRefusal{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SeenRefusal, 0, len(s.entries))
	for _, e := range s.entries {
		out = append(out, *e)
	}
	slices.SortFunc(out, func(a, b SeenRefusal) int { return b.Last.Compare(a.Last) })
	return out
}
