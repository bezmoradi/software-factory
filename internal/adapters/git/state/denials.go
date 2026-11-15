package state

import (
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"sync"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	deniedFile     = "denied.json"
	deniedVersion  = 2
	maxDeniedOfOne = session.MaxDenied
)

type Denials struct {
	Dir string
	mu  *sync.Mutex
}

func NewDenials(dir string) Denials { return Denials{Dir: dir, mu: &sync.Mutex{}} }

var _ session.DenialStore = Denials{}

type denialJSON struct {
	SID     string   `json:"sid"`
	Commit  string   `json:"commit,omitempty"`
	Commits []string `json:"commits,omitempty"`
}

type deniedJSON struct {
	Version int          `json:"version"`
	Denied  []denialJSON `json:"denied"`
}

func (d Denials) Load() (map[string][]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	data, err := read(d.Dir, deniedFile)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	if data == nil {
		return out, nil
	}
	var f deniedJSON
	if err := json.Unmarshal(data, &f); err != nil || !readsVersion(deniedFile, f.Version) {
		return nil, errors.New("denied.json isn't a denials record this engine reads")
	}
	for _, e := range f.Denied {
		commits := e.Commits
		if f.Version == 1 {
			commits = []string{e.Commit}
		}
		if !sessionID.MatchString(e.SID) {
			continue
		}
		if _, seen := out[e.SID]; !seen && len(out) >= maxSessions {
			return nil, errors.New("denied.json holds denials of more sessions than this engine reads")
		}
		for _, c := range commits {
			if commitID.MatchString(c) && !slices.Contains(out[e.SID], c) {
				out[e.SID] = append(out[e.SID], c)
			}
		}
		if n := len(out[e.SID]); n > maxDeniedOfOne {
			out[e.SID] = out[e.SID][n-maxDeniedOfOne:]
		}
	}
	return out, nil
}

func (d Denials) Save(denied map[string][]string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	f := deniedJSON{Version: writesVersion(deniedFile), Denied: make([]denialJSON, 0, len(denied))}
	for sid, commits := range denied {
		if len(commits) == 0 {
			continue
		}
		f.Denied = append(f.Denied, denialJSON{SID: sid, Commits: slices.Clone(commits)})
	}
	sort.Slice(f.Denied, func(i, j int) bool { return f.Denied[i].SID < f.Denied[j].SID })
	return write(d.Dir, deniedFile, f)
}
