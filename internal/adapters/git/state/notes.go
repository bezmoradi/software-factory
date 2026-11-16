package state

import (
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"sync"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	notesFile      = "notes.json"
	maxNotes       = 2000
	maxNoteSources = 20
)

var noteSource = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)

type Notes struct {
	Dir string
	mu  *sync.Mutex
}

func NewNotes(dir string) Notes { return Notes{Dir: dir, mu: &sync.Mutex{}} }

var _ session.NoteStore = Notes{}

type noteJSON struct {
	SID    string `json:"sid"`
	Source string `json:"source"`
	Text   string `json:"text"`
}

type notesJSON struct {
	Version int        `json:"version"`
	Notes   []noteJSON `json:"notes"`
}

func noteOf(n noteJSON) bool {
	return sessionID.MatchString(n.SID) && noteSource.MatchString(n.Source) && n.Text != "" && utf8.ValidString(n.Text) && len(n.Text) <= session.NoteCap(n.Source)
}

func (n Notes) Load() (map[string]map[string]string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	data, err := read(n.Dir, notesFile)
	if err != nil || data == nil {
		return nil, err
	}
	var f notesJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 {
		return nil, errors.New("notes.json isn't a record of notes to agents this engine reads")
	}
	out := map[string]map[string]string{}
	count := 0
	for _, e := range f.Notes {
		if !noteOf(e) || count >= maxNotes || len(out[e.SID]) >= maxNoteSources {
			continue
		}
		if out[e.SID] == nil {
			out[e.SID] = map[string]string{}
		}
		if _, dup := out[e.SID][e.Source]; dup {
			continue
		}
		out[e.SID][e.Source] = e.Text
		count++
	}
	return out, nil
}

func (n Notes) Save(all map[string]map[string]string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	f := notesJSON{Version: 1, Notes: []noteJSON{}}
	for sid, sources := range all {
		for source, text := range sources {
			e := noteJSON{SID: sid, Source: source, Text: text}
			if noteOf(e) {
				f.Notes = append(f.Notes, e)
			}
		}
	}
	sort.Slice(f.Notes, func(i, j int) bool {
		if f.Notes[i].SID != f.Notes[j].SID {
			return f.Notes[i].SID < f.Notes[j].SID
		}
		return f.Notes[i].Source < f.Notes[j].Source
	})
	if len(f.Notes) > maxNotes {
		noteDropped(n.Dir, notesFile, len(f.Notes)-maxNotes)
		f.Notes = f.Notes[:maxNotes]
	}
	return write(n.Dir, notesFile, f)
}
