package state

import (
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const updateFile = "update.json"

type UpdatePrefs struct {
	Dir string
	mu  *sync.Mutex
}

func NewUpdatePrefs(dir string) UpdatePrefs { return UpdatePrefs{Dir: dir, mu: &sync.Mutex{}} }

var _ session.UpdateStore = UpdatePrefs{}

type updateLastJSON struct {
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"`
	Release string    `json:"release,omitempty"`
}

type updateFileJSON struct {
	Version int             `json:"version"`
	Later   string          `json:"later,omitempty"`
	Last    *updateLastJSON `json:"last,omitempty"`
	Owner   string          `json:"owner,omitempty"`
}

func (u UpdatePrefs) lock() func() {
	if u.mu == nil {
		return func() {}
	}
	u.mu.Lock()
	return u.mu.Unlock
}

func (u UpdatePrefs) Load() (session.UpdateSettings, error) {
	defer u.lock()()
	data, err := read(u.Dir, updateFile)
	if err != nil {
		return session.UpdateSettings{}, err
	}
	if data == nil {
		return session.UpdateSettings{}, nil
	}
	var f updateFileJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 {
		return session.UpdateSettings{}, errors.New("update.json isn't a record this engine reads")
	}
	var out session.UpdateSettings
	if f.Later != "" {
		later, ok := session.ParseReleaseVersion(f.Later)
		if !ok {
			return session.UpdateSettings{}, errors.New("update.json names a release this engine can't read")
		}
		out.Later = later
	}
	if f.Last != nil {
		last, ok := session.CleanUpdateLast(session.UpdateLast{At: f.Last.At, Kind: f.Last.Kind, Version: f.Last.Release})
		if ok {
			out.Last = last
		}
	}
	return out, nil
}

func (u UpdatePrefs) Save(s session.UpdateSettings) error {
	owner := ownerIn(u.Dir)
	defer u.lock()()
	f := updateFileJSON{Version: 1, Later: s.Later, Owner: owner}
	if !s.Last.At.IsZero() {
		f.Last = &updateLastJSON{At: s.Last.At.UTC(), Kind: s.Last.Kind, Release: s.Last.Version}
	}
	return write(u.Dir, updateFile, f)
}
