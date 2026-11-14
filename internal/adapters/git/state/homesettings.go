package state

import (
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const homeSettingsFile = "agent-settings.json"

type HomeSettings struct {
	Dir string
	mu  *sync.Mutex
}

func NewHomeSettings(dir string) HomeSettings { return HomeSettings{Dir: dir, mu: &sync.Mutex{}} }

var _ session.HomeStore = HomeSettings{}

type homeEntryJSON struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Hash string `json:"hash,omitempty"`
}

type homeSettingsJSON struct {
	Version int             `json:"version"`
	Box     string          `json:"box,omitempty"`
	At      time.Time       `json:"at"`
	Since   time.Time       `json:"since"`
	Entries []homeEntryJSON `json:"entries"`
	Changed []string        `json:"changed"`
	More    int             `json:"more,omitempty"`
}

func (s HomeSettings) lock() func() {
	if s.mu == nil {
		return func() {}
	}
	s.mu.Lock()
	return s.mu.Unlock
}

func (s HomeSettings) Load() (session.HomeRecord, bool, error) {
	defer s.lock()()
	data, err := read(s.Dir, homeSettingsFile)
	if err != nil || data == nil {
		return session.HomeRecord{}, false, err
	}
	var f homeSettingsJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 || len(f.Changed) > session.MaxHomeChanged || f.More < 0 || f.Box != "" && !session.ValidBox(f.Box) {
		return session.HomeRecord{}, false, errors.New(homeSettingsFile + " isn't a record this engine reads")
	}
	entries := make([]session.HomeEntry, 0, len(f.Entries))
	for _, e := range f.Entries {
		entries = append(entries, session.HomeEntry{Name: e.Name, Kind: e.Kind, Hash: e.Hash})
	}
	entries, ok := session.CheckHomeEntries(entries)
	if !ok {
		return session.HomeRecord{}, false, errors.New(homeSettingsFile + " holds an entry this engine refuses")
	}
	for _, c := range f.Changed {
		if len(c) > session.MaxHomeName+16 {
			return session.HomeRecord{}, false, errors.New(homeSettingsFile + " holds an entry this engine refuses")
		}
	}
	return session.HomeRecord{Box: f.Box, Entries: entries, At: f.At, Since: f.Since, Changed: f.Changed, More: f.More}, true, nil
}

func (s HomeSettings) Save(r session.HomeRecord) error {
	defer s.lock()()
	f := homeSettingsJSON{Version: 1, Box: r.Box, At: r.At, Since: r.Since, Entries: make([]homeEntryJSON, 0, len(r.Entries)), Changed: r.Changed, More: r.More}
	if f.Changed == nil {
		f.Changed = []string{}
	}
	for _, e := range r.Entries {
		f.Entries = append(f.Entries, homeEntryJSON{Name: e.Name, Kind: e.Kind, Hash: e.Hash})
	}
	return write(s.Dir, homeSettingsFile, f)
}
