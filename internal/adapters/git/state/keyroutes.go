package state

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const keyRoutesFile = "keyroutes.json"

type KeyRoutes struct {
	Dir string
	mu  *sync.Mutex
}

func NewKeyRoutes(dir string) KeyRoutes { return KeyRoutes{Dir: dir, mu: &sync.Mutex{}} }

var _ session.KeyRoutes = KeyRoutes{}

type keyRouteJSON struct {
	Saved   bool   `json:"saved"`
	Gateway string `json:"gateway,omitempty"`
	Before  string `json:"before,omitempty"`
}

type keyRoutesJSON struct {
	Version int                     `json:"version"`
	Sides   map[string]keyRouteJSON `json:"sides"`
}

var errKeyRoutes = errors.New("keyroutes.json isn't a record of pasted keys this engine reads")

func (s KeyRoutes) lock() func() {
	if s.mu == nil {
		return func() {}
	}
	s.mu.Lock()
	return s.mu.Unlock
}

func keySide(side string) bool {
	return side == session.SideAgents || side == session.SideReviewers
}

func (s KeyRoutes) Load() (map[string]session.KeyRoute, error) {
	defer s.lock()()
	data, err := read(s.Dir, keyRoutesFile)
	if err != nil {
		return nil, err
	}
	out := map[string]session.KeyRoute{}
	if data == nil {
		return out, nil
	}
	var f keyRoutesJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 || len(f.Sides) > 2 {
		return nil, errKeyRoutes
	}
	for side, k := range f.Sides {
		gatewayOK := k.Gateway == "" || !strings.HasPrefix(k.Gateway, ".") && session.ValidAllowEntry(k.Gateway)
		beforeOK := k.Before == "" || session.ValidExpect(session.Expect(k.Before))
		if !keySide(side) || !gatewayOK || !beforeOK || !k.Saved && (k.Gateway != "" || k.Before != "") {
			return nil, errKeyRoutes
		}
		out[side] = session.KeyRoute{Saved: k.Saved, Gateway: k.Gateway, Before: session.Expect(k.Before)}
	}
	return out, nil
}

func (s KeyRoutes) Save(all map[string]session.KeyRoute) error {
	defer s.lock()()
	f := keyRoutesJSON{Version: 1, Sides: map[string]keyRouteJSON{}}
	for side, k := range all {
		if !keySide(side) {
			continue
		}
		j := keyRouteJSON{Saved: k.Saved}
		if k.Saved {
			j.Gateway, j.Before = k.Gateway, string(k.Before)
		}
		f.Sides[side] = j
	}
	return write(s.Dir, keyRoutesFile, f)
}
