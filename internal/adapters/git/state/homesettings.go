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
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Hash  string `json:"hash,omitempty"`
	Class int    `json:"class,omitempty"`
}

type homeBoxJSON struct {
	Box     string          `json:"box,omitempty"`
	At      time.Time       `json:"at"`
	Since   time.Time       `json:"since"`
	Entries []homeEntryJSON `json:"entries"`
	Changed []string        `json:"changed"`
	More    int             `json:"more,omitempty"`
	Dropped bool            `json:"dropped,omitempty"`
}

type homeSettingsJSON struct {
	Version int `json:"version"`
	homeBoxJSON
	Boxes []homeBoxJSON `json:"boxes,omitempty"`
}

type homeBoxesJSON struct {
	Version int           `json:"version"`
	Boxes   []homeBoxJSON `json:"boxes"`
}

var errHomeUnread = errors.New(homeSettingsFile + " isn't a record this engine reads")

func (s HomeSettings) lock() func() {
	if s.mu == nil {
		return func() {}
	}
	s.mu.Lock()
	return s.mu.Unlock
}

func homeRecordOf(b homeBoxJSON) (session.HomeRecord, error) {
	if !session.ValidBox(b.Box) || len(b.Changed) > session.MaxHomeChanged || b.More < 0 || b.Dropped && (len(b.Entries) > 0 || len(b.Changed) > 0 || b.More > 0) {
		return session.HomeRecord{}, errHomeUnread
	}
	entries := make([]session.HomeEntry, 0, len(b.Entries))
	for _, e := range b.Entries {
		entries = append(entries, session.HomeEntry{Name: e.Name, Kind: e.Kind, Hash: e.Hash, Class: e.Class})
	}
	entries, ok := session.CheckHomeEntries(entries)
	if !ok {
		return session.HomeRecord{}, errors.New(homeSettingsFile + " holds an entry this engine refuses")
	}
	changed := make([]string, 0, len(b.Changed))
	for _, c := range b.Changed {
		if len(c) > session.MaxHomeName+16 {
			return session.HomeRecord{}, errors.New(homeSettingsFile + " holds an entry this engine refuses")
		}
		changed = append(changed, session.MaskHomeLine(c))
	}
	return session.HomeRecord{Box: b.Box, Entries: entries, At: b.At, Since: b.Since, Changed: changed, More: b.More, Dropped: b.Dropped}, nil
}

func (s HomeSettings) Load() ([]session.HomeRecord, error) {
	defer s.lock()()
	data, err := read(s.Dir, homeSettingsFile)
	if err != nil || data == nil {
		return nil, err
	}
	var f homeSettingsJSON
	if err := json.Unmarshal(data, &f); err != nil || !readsVersion(homeSettingsFile, f.Version) {
		return nil, errHomeUnread
	}
	if f.Version == 1 {
		if f.Box == "" || f.Dropped {
			return nil, nil
		}
		r, err := homeRecordOf(f.homeBoxJSON)
		if err != nil {
			return nil, err
		}
		return []session.HomeRecord{r}, nil
	}
	if len(f.Boxes) > session.MaxHomeBoxes {
		return nil, errHomeUnread
	}
	out := make([]session.HomeRecord, 0, len(f.Boxes))
	seen := map[string]bool{}
	for _, b := range f.Boxes {
		r, err := homeRecordOf(b)
		if err != nil {
			return nil, err
		}
		if seen[r.Box] {
			return nil, errHomeUnread
		}
		seen[r.Box] = true
		out = append(out, r)
	}
	return out, nil
}

func dropped(b homeBoxJSON) homeBoxJSON {
	return homeBoxJSON{Box: b.Box, At: b.At, Since: b.Since, Entries: []homeEntryJSON{}, Changed: []string{}, Dropped: true}
}

func (s HomeSettings) Save(records []session.HomeRecord) ([]string, error) {
	defer s.lock()()
	f := homeBoxesJSON{Version: writesVersion(homeSettingsFile), Boxes: make([]homeBoxJSON, 0, len(records))}
	sizes := make([]int, 0, len(records))
	for _, r := range records[:min(len(records), session.MaxHomeBoxes)] {
		changed := make([]string, 0, len(r.Changed))
		for _, c := range r.Changed {
			changed = append(changed, session.MaskHomeLine(c))
		}
		b := homeBoxJSON{Box: r.Box, At: r.At, Since: r.Since, Entries: make([]homeEntryJSON, 0, len(r.Entries)), Changed: changed, More: r.More}
		if b.Changed == nil {
			b.Changed = []string{}
		}
		size := 0
		for _, e := range r.Entries {
			b.Entries = append(b.Entries, homeEntryJSON{Name: e.Name, Kind: e.Kind, Hash: e.Hash, Class: e.Class})
			size += len(e.Name) + len(e.Kind) + len(e.Hash) + 64
		}
		for _, c := range b.Changed {
			size += len(c) + 8
		}
		if r.Dropped {
			b, size = dropped(b), 0
		}
		f.Boxes = append(f.Boxes, b)
		sizes = append(sizes, size)
	}
	var gone []string
	for {
		data, err := encode(f)
		if err != nil {
			return nil, err
		}
		largest := -1
		for i, b := range f.Boxes {
			if !b.Dropped && (largest < 0 || sizes[i] > sizes[largest]) {
				largest = i
			}
		}
		if len(data) <= maxFile || largest < 0 {
			return gone, writeBytes(s.Dir, homeSettingsFile, data)
		}
		gone = append(gone, f.Boxes[largest].Box)
		f.Boxes[largest] = dropped(f.Boxes[largest])
		sizes[largest] = 0
	}
}
