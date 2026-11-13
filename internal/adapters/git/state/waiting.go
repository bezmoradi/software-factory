package state

import (
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const waitingFile = "waiting.json"

type Waiting struct {
	Dir string
	mu  *sync.Mutex
}

func NewWaiting(dir string) Waiting { return Waiting{Dir: dir, mu: &sync.Mutex{}} }

var _ session.WaitingStore = Waiting{}

type waitJSON struct {
	SID    string    `json:"sid"`
	Commit string    `json:"commit"`
	Since  time.Time `json:"since,omitzero"`
}

type waitingJSON struct {
	Version int        `json:"version"`
	Waiting []waitJSON `json:"waiting"`
}

func (w Waiting) Load() ([]session.WaitingPlan, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	data, err := read(w.Dir, waitingFile)
	if err != nil || data == nil {
		return nil, err
	}
	var f waitingJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 {
		return nil, errors.New("waiting.json isn't a record of waiting pushes this engine reads")
	}
	var out []session.WaitingPlan
	seen := map[string]bool{}
	for _, e := range f.Waiting {
		if !sessionID.MatchString(e.SID) || !commitID.MatchString(e.Commit) || seen[e.SID] || len(out) >= maxSessions {
			continue
		}
		seen[e.SID] = true
		out = append(out, session.WaitingPlan{SID: e.SID, Commit: e.Commit, Since: e.Since})
	}
	return out, nil
}

func (w Waiting) Save(plans []session.WaitingPlan) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	f := waitingJSON{Version: 1, Waiting: make([]waitJSON, 0, len(plans))}
	for _, p := range plans {
		f.Waiting = append(f.Waiting, waitJSON{SID: p.SID, Commit: p.Commit, Since: p.Since.UTC()})
	}
	sort.Slice(f.Waiting, func(i, j int) bool { return f.Waiting[i].SID < f.Waiting[j].SID })
	return write(w.Dir, waitingFile, f)
}
