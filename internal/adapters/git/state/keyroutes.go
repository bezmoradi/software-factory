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
	Owner   string `json:"owner,omitempty"`
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

func keyTarget(target string) bool {
	return session.ValidLoginTarget(target)
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
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 || len(f.Sides) > session.MaxBoxRecords+2 {
		return nil, errKeyRoutes
	}
	for target, k := range f.Sides {
		gatewayOK := k.Gateway == "" || !strings.HasPrefix(k.Gateway, ".") && session.ValidAllowEntry(k.Gateway)
		beforeOK := k.Before == "" || session.ValidExpect(session.Expect(k.Before))
		if !keyTarget(target) && target != session.SideAgents || !gatewayOK || !beforeOK || !k.Saved && (k.Gateway != "" || k.Before != "") {
			return nil, errKeyRoutes
		}
		if target == session.SideAgents {
			continue
		}
		out[target] = session.KeyRoute{Saved: k.Saved, Gateway: k.Gateway, Before: session.Expect(k.Before)}
	}
	return out, nil
}

func (s KeyRoutes) Save(all map[string]session.KeyRoute) error {
	owner := ownerIn(s.Dir)
	defer s.lock()()
	f := keyRoutesJSON{Version: 1, Sides: map[string]keyRouteJSON{}}
	for target, k := range all {
		if !keyTarget(target) {
			continue
		}
		j := keyRouteJSON{Owner: owner, Saved: k.Saved}
		if k.Saved {
			j.Gateway, j.Before = k.Gateway, string(k.Before)
		}
		f.Sides[target] = j
	}
	return write(s.Dir, keyRoutesFile, f)
}
