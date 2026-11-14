package session

import (
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	maxNote        = 1200
	maxNoteSources = 20
	NoteMarker     = "[Software Factory] "
	noteBreak      = "\n\n"
	noteIndent     = "    "
)

var noteSource = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)

func NoteCap(source string) int {
	if source == DecisionNoteSource {
		return MaxDecisionNote
	}
	return maxNote
}

type NoteStore interface {
	Load() (map[string]map[string]string, error)
	Save(map[string]map[string]string) error
}

func (r *Registry) NoteFor(source string) func(sid, text string) {
	return func(sid, text string) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if text == "" {
			if _, ok := r.notes[sid][source]; !ok {
				return
			}
			delete(r.notes[sid], source)
			if len(r.notes[sid]) == 0 {
				delete(r.notes, sid)
			}
			r.notesChangedLocked()
			return
		}
		r.setNoteLocked(sid, source, text)
	}
}

func (r *Registry) NotesFor(source string) func(sid, text string) {
	return func(sid, text string) {
		if text == "" {
			return
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		key := source + "-" + strconv.FormatInt(r.now().UnixNano(), 10)
		for r.notes[sid][key] != "" {
			key += "0"
		}
		r.setNoteLocked(sid, key, text)
	}
}

func (r *Registry) setNoteLocked(sid, source, text string) {
	if r.notes == nil {
		r.notes = map[string]map[string]string{}
	}
	if r.notes[sid] == nil {
		r.notes[sid] = map[string]string{}
	}
	all := r.notes[sid]
	if _, ok := all[source]; !ok && len(all) >= maxNoteSources {
		delete(all, slices.Min(slices.Collect(maps.Keys(all))))
	}
	all[source] = clip(text, NoteCap(source))
	r.notesChangedLocked()
}

func (r *Registry) dropNotesLocked(sid string) {
	if _, ok := r.notes[sid]; !ok {
		return
	}
	delete(r.notes, sid)
	r.notesChangedLocked()
}

func (r *Registry) UseNotes(store NoteStore) {
	if store == nil {
		return
	}
	saved, err := store.Load()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.noteStore = store
	if err != nil {
		return
	}
	for sid, all := range saved {
		if !sidPattern.MatchString(sid) || len(r.notes) >= maxRemembered {
			continue
		}
		for source, text := range all {
			if !noteSource.MatchString(source) || text == "" || !utf8.ValidString(text) || len(r.notes[sid]) >= maxNoteSources {
				continue
			}
			if r.notes == nil {
				r.notes = map[string]map[string]string{}
			}
			if r.notes[sid] == nil {
				r.notes[sid] = map[string]string{}
			}
			if _, ok := r.notes[sid][source]; !ok {
				r.notes[sid][source] = clip(text, NoteCap(source))
			}
		}
	}
}

func (r *Registry) DropStrayNotes() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	dropped := 0
	for sid := range r.notes {
		if r.admitted[sid] == nil {
			delete(r.notes, sid)
			dropped++
		}
	}
	if dropped > 0 {
		r.notesChangedLocked()
	}
	return dropped
}

func (r *Registry) notesChangedLocked() {
	if r.noteStore == nil {
		return
	}
	r.noteVersion++
	version := r.noteVersion
	snapshot := make(map[string]map[string]string, len(r.notes))
	for sid, all := range r.notes {
		kept := map[string]string{}
		for source, text := range all {
			if where, _ := ForgeSecret("", text); where == "" {
				kept[source] = text
			}
		}
		if len(kept) > 0 {
			snapshot[sid] = kept
		}
	}
	store := r.noteStore
	go r.saveNotes(store, version, snapshot)
}

func (r *Registry) saveNotes(store NoteStore, version uint64, snapshot map[string]map[string]string) {
	r.noteMu.Lock()
	defer r.noteMu.Unlock()
	if version <= r.noteSaved {
		return
	}
	_ = store.Save(snapshot)
	r.noteSaved = version
}

func noteParagraph(text string) string {
	return strings.ReplaceAll(strings.TrimSpace(text), "\n", "\n"+noteIndent)
}

func (r *Registry) noted(sid, text string) (string, bool) {
	all := r.notes[sid]
	if len(all) == 0 || commandName(text) != "" {
		return text, false
	}
	sources := make([]string, 0, len(all))
	for s := range all {
		sources = append(sources, s)
	}
	slices.Sort(sources)
	said := make([]string, 0, len(sources))
	for _, s := range sources {
		said = append(said, noteParagraph(all[s]))
	}
	out := NoteMarker + strings.Join(said, noteBreak+NoteMarker)
	if own, ok := strings.CutPrefix(text, NoteMarker); ok {
		out += noteBreak + NoteMarker + own
	} else if text != "" {
		out += noteBreak + text
	}
	if utf16Len(out) > MaxText {
		return text, false
	}
	return out, true
}
