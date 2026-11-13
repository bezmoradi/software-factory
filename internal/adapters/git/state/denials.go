package state

import (
	"encoding/json"
	"errors"
	"sort"
	"sync"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const deniedFile = "denied.json"

type Denials struct {
	Dir string
	mu  *sync.Mutex
}

func NewDenials(dir string) Denials { return Denials{Dir: dir, mu: &sync.Mutex{}} }

var _ session.DenialStore = Denials{}

type denialJSON struct {
	SID    string `json:"sid"`
	Commit string `json:"commit"`
}

type deniedJSON struct {
	Version int          `json:"version"`
	Denied  []denialJSON `json:"denied"`
}

func (d Denials) Load() (map[string]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	data, err := read(d.Dir, deniedFile)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	if data == nil {
		return out, nil
	}
	var f deniedJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 {
		return nil, errors.New("denied.json isn't a denials record this engine reads")
	}
	for _, e := range f.Denied {
		if sessionID.MatchString(e.SID) && commitID.MatchString(e.Commit) && len(out) < maxSessions {
			out[e.SID] = e.Commit
		}
	}
	return out, nil
}

func (d Denials) Save(denied map[string]string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	f := deniedJSON{Version: 1, Denied: make([]denialJSON, 0, len(denied))}
	for sid, commit := range denied {
		f.Denied = append(f.Denied, denialJSON{SID: sid, Commit: commit})
	}
	sort.Slice(f.Denied, func(i, j int) bool { return f.Denied[i].SID < f.Denied[j].SID })
	return write(d.Dir, deniedFile, f)
}
