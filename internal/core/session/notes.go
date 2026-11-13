package session

import (
	"slices"
	"strings"
)

const (
	maxNote    = 1200
	NoteMarker = "[Software Factory] "
)

func (r *Registry) NoteFor(source string) func(sid, text string) {
	return func(sid, text string) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if text == "" {
			delete(r.notes[sid], source)
			if len(r.notes[sid]) == 0 {
				delete(r.notes, sid)
			}
			return
		}
		if r.notes == nil {
			r.notes = map[string]map[string]string{}
		}
		if r.notes[sid] == nil {
			r.notes[sid] = map[string]string{}
		}
		r.notes[sid][source] = clip(text, maxNote)
	}
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
		said = append(said, all[s])
	}
	out := NoteMarker + strings.Join(said, " ")
	if own, ok := strings.CutPrefix(text, NoteMarker); ok {
		out += " " + own
	} else if text != "" {
		out += "\n\n" + text
	}
	if utf16Len(out) > MaxText {
		return text, false
	}
	return out, true
}
