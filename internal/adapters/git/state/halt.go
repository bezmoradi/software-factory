package state

import (
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const haltFile = "halt.json"

type Halt struct {
	Dir string
	mu  *sync.Mutex
}

func NewHalt(dir string) Halt { return Halt{Dir: dir, mu: &sync.Mutex{}} }

var _ session.HaltStore = Halt{}

type haltJSON struct {
	Version int       `json:"version"`
	Mode    string    `json:"mode"`
	Since   time.Time `json:"since,omitzero"`
	AwayAll bool      `json:"awayAll"`
	Away    []string  `json:"away"`
	Owner   string    `json:"owner,omitempty"`
}

func (h Halt) Load() (session.HaltState, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	data, err := read(h.Dir, haltFile)
	if err != nil || data == nil {
		return session.HaltState{}, err
	}
	var f haltJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 || !session.ValidHaltMode(f.Mode) {
		return session.HaltState{}, errors.New("halt.json isn't a record of the stop switch this engine reads")
	}
	out := session.HaltState{Mode: f.Mode, Since: f.Since, AwayAll: f.AwayAll}
	seen := map[string]bool{}
	for _, sid := range f.Away {
		if !sessionID.MatchString(sid) || seen[sid] || len(out.Away) >= maxSessions {
			continue
		}
		seen[sid] = true
		out.Away = append(out.Away, sid)
	}
	return out, nil
}

func (h Halt) Save(s session.HaltState) error {
	owner := ownerIn(h.Dir)
	h.mu.Lock()
	defer h.mu.Unlock()
	f := haltJSON{Owner: owner, Version: 1, Mode: s.Mode, Since: s.Since.UTC(), AwayAll: s.AwayAll, Away: []string{}}
	for _, sid := range s.Away {
		if sessionID.MatchString(sid) && len(f.Away) < maxSessions {
			f.Away = append(f.Away, sid)
		}
	}
	sort.Strings(f.Away)
	return write(h.Dir, haltFile, f)
}
