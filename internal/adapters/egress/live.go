package egress

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"slices"
	"sync"
	"time"
)

const MaxEntries = 256

type Lists struct {
	Allow   []string
	Expires map[string]time.Time
}

type Live struct {
	mu    sync.RWMutex
	sides map[string]Policy
	order []string
	set   bool
	boot  string
}

func NewLive(start Policy, sides ...string) *Live {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	l := &Live{sides: map[string]Policy{}, order: slices.Clone(sides), boot: hex.EncodeToString(b)}
	for _, s := range sides {
		l.sides[s] = start
	}
	return l
}

type side struct {
	live *Live
	name string
}

func (s side) Current() Policy {
	s.live.mu.RLock()
	defer s.live.mu.RUnlock()
	return s.live.sides[s.name]
}

func (l *Live) Side(name string) Source { return side{live: l, name: name} }

func (l *Live) Replace(lists map[string]Lists) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	next := map[string]Policy{}
	for _, s := range l.order {
		got, ok := lists[s]
		if !ok {
			return fmt.Errorf("egress: the new lists lack the %s side", s)
		}
		if len(got.Allow) > MaxEntries || len(got.Expires) > MaxEntries {
			return fmt.Errorf("egress: the %s side holds more than %d entries", s, MaxEntries)
		}
		p, err := l.sides[s].WithAllow(got.Allow, got.Expires)
		if err != nil {
			return err
		}
		next[s] = p
	}
	if len(lists) != len(l.order) {
		return fmt.Errorf("egress: the new lists name a side egress doesn't have")
	}
	l.sides = next
	l.set = true
	return nil
}

type snapshot struct {
	boot  string
	set   bool
	deny  []string
	sides map[string]Policy
	order []string
}

func (l *Live) snapshot() snapshot {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := snapshot{boot: l.boot, set: l.set, sides: map[string]Policy{}, order: slices.Clone(l.order)}
	for k, v := range l.sides {
		out.sides[k] = v
		out.deny = v.Deny()
	}
	return out
}
