package state

import (
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const viewedFile = "viewed.json"

var blobHex = regexp.MustCompile(`^[0-9a-f]{40}$`)

type Viewed struct {
	Dir string
	mu  *sync.Mutex
}

func NewViewed(dir string) Viewed { return Viewed{Dir: dir, mu: &sync.Mutex{}} }

var _ session.ViewedStore = Viewed{}

type viewedFileJSON struct {
	Path string `json:"path"`
	Blob string `json:"blob"`
}

type viewedSessionJSON struct {
	SID   string           `json:"sid"`
	At    time.Time        `json:"at"`
	Files []viewedFileJSON `json:"files"`
}

type viewedJSON struct {
	Version  int                 `json:"version"`
	Sessions []viewedSessionJSON `json:"sessions"`
}

func (v Viewed) Load() (map[string]session.ViewedMarks, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	data, err := read(v.Dir, viewedFile)
	if err != nil {
		return nil, err
	}
	out := map[string]session.ViewedMarks{}
	if data == nil {
		return out, nil
	}
	var f viewedJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 {
		return nil, errors.New("viewed.json isn't a record this engine reads")
	}
	for _, s := range f.Sessions {
		if !sessionID.MatchString(s.SID) || len(out) >= session.MaxViewedSessions {
			continue
		}
		files := map[string]string{}
		for _, x := range s.Files {
			if session.ViewedPath(x.Path) && blobHex.MatchString(x.Blob) && len(files) < session.MaxViewedFiles {
				files[x.Path] = x.Blob
			}
		}
		if len(files) > 0 {
			out[s.SID] = session.ViewedMarks{At: s.At, Files: files}
		}
	}
	return out, nil
}

func (v Viewed) Save(marks map[string]session.ViewedMarks) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	f := viewedJSON{Version: 1, Sessions: make([]viewedSessionJSON, 0, len(marks))}
	for sid, m := range marks {
		if !sessionID.MatchString(sid) {
			continue
		}
		s := viewedSessionJSON{SID: sid, At: m.At.UTC(), Files: make([]viewedFileJSON, 0, len(m.Files))}
		for path, blob := range m.Files {
			s.Files = append(s.Files, viewedFileJSON{Path: path, Blob: blob})
		}
		sort.Slice(s.Files, func(i, j int) bool { return s.Files[i].Path < s.Files[j].Path })
		f.Sessions = append(f.Sessions, s)
	}
	sort.Slice(f.Sessions, func(i, j int) bool { return f.Sessions[i].SID < f.Sessions[j].SID })
	return write(v.Dir, viewedFile, f)
}
