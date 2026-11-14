package state

import (
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const recapSeenFile = "recap-seen.json"

type RecapSeen struct {
	Dir string
	mu  *sync.Mutex
}

func NewRecapSeen(dir string) RecapSeen { return RecapSeen{Dir: dir, mu: &sync.Mutex{}} }

var _ session.RecapSeenStore = RecapSeen{}

type seenJSON struct {
	SID  string    `json:"sid"`
	Seen time.Time `json:"seen"`
}

type recapSeenJSON struct {
	Version int        `json:"version"`
	Seen    []seenJSON `json:"seen"`
}

func (r RecapSeen) Load() (map[string]time.Time, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	data, err := read(r.Dir, recapSeenFile)
	if err != nil {
		return nil, err
	}
	out := map[string]time.Time{}
	if data == nil {
		return out, nil
	}
	var f recapSeenJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 {
		return nil, errors.New("recap-seen.json isn't a record this engine reads")
	}
	for _, e := range f.Seen {
		if sessionID.MatchString(e.SID) && !e.Seen.IsZero() && len(out) < session.MaxRecapSeen {
			out[e.SID] = e.Seen
		}
	}
	return out, nil
}

func (r RecapSeen) Save(seen map[string]time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	f := recapSeenJSON{Version: 1, Seen: make([]seenJSON, 0, len(seen))}
	for sid, at := range seen {
		if sessionID.MatchString(sid) && len(f.Seen) < session.MaxRecapSeen {
			f.Seen = append(f.Seen, seenJSON{SID: sid, Seen: at.UTC()})
		}
	}
	sort.Slice(f.Seen, func(i, j int) bool { return f.Seen[i].SID < f.Seen[j].SID })
	return write(r.Dir, recapSeenFile, f)
}
