package session

import (
	"context"
	"maps"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/push"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	MaxViewedSessions = 200
	MaxViewedFiles    = 1000
	MaxViewedPath     = 4096
	MaxViewedBytes    = 2 << 20

	viewedEntryBytes = 64
)

type ViewedMarks struct {
	At    time.Time
	Files map[string]string
}

type ViewedStore interface {
	Load() (map[string]ViewedMarks, error)
	Save(map[string]ViewedMarks) error
}

type ViewedPage interface {
	Marks(sid string) map[string]string
	Mark(ctx context.Context, sid, path, blob string, on bool) error
}

type ViewedFiles interface {
	ChangedFiles(ctx context.Context, repo, from, to string) ([]push.Change, error)
	BlobAt(ctx context.Context, repo, commit, path string) (string, error)
}

type ViewedConfig struct {
	Store   ViewedStore
	Targets func(sid string) (PushTarget, bool)
	Doubled func(sid string) bool
	Heads   Heads
	History History
	Main    func(name string) (string, error)
	Files   ViewedFiles
	Now     func() time.Time
	Logger  shared.Logger
}

type Viewed struct {
	cfg    ViewedConfig
	mu     sync.Mutex
	marks  map[string]ViewedMarks
	loaded bool
	broken bool
}

var _ ViewedPage = (*Viewed)(nil)

func NewViewed(cfg ViewedConfig) *Viewed {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Viewed{cfg: cfg, marks: map[string]ViewedMarks{}}
}

func (v *Viewed) loadLocked() {
	if v.loaded {
		return
	}
	v.loaded = true
	if v.cfg.Store == nil {
		return
	}
	saved, err := v.cfg.Store.Load()
	if err != nil {
		v.broken = true
		if v.cfg.Logger != nil {
			v.cfg.Logger.Warn("viewed", "event", "not-read")
		}
		return
	}
	if saved != nil {
		v.marks = saved
	}
}

func (v *Viewed) Marks(sid string) map[string]string {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.loadLocked()
	return maps.Clone(v.marks[sid].Files)
}

func ViewedPath(path string) bool {
	return path != "" && len(path) <= MaxViewedPath && utf8.ValidString(path)
}

func (v *Viewed) Mark(ctx context.Context, sid, path, blob string, on bool) error {
	if v.cfg.Targets == nil {
		return explained(conflict("not started here"), "Only a session this engine started has a changes view here.")
	}
	t, ok := v.cfg.Targets(sid)
	if !ok {
		return notStartedHere(v.cfg.Doubled, sid, "Only a session this engine started has a changes view here.")
	}
	if !ViewedPath(path) || !changeCommit.MatchString(blob) {
		return explained(invalid("fields"), "A viewed mark needs the file's path and its blob hash.")
	}
	if on {
		if err := v.onBranch(ctx, t, path, blob); err != nil {
			return err
		}
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.loadLocked()
	if v.broken {
		return explained(conflict("not read"), "The engine couldn't read its file of viewed marks, so it doesn't write over it. Its log says why.")
	}
	m := v.marks[sid]
	files := maps.Clone(m.Files)
	if files == nil {
		files = map[string]string{}
	}
	if on {
		if _, had := files[path]; !had && len(files) >= MaxViewedFiles {
			return explained(limited("viewed"), "This session already has the most viewed marks the engine keeps. Clear some first.")
		}
		files[path] = blob
	} else {
		delete(files, path)
	}
	next := maps.Clone(v.marks)
	if next == nil {
		next = map[string]ViewedMarks{}
	}
	if len(files) == 0 {
		delete(next, sid)
	} else {
		next[sid] = ViewedMarks{At: v.cfg.Now(), Files: files}
	}
	for other := range next {
		if _, ok := v.cfg.Targets(other); !ok && !isDoubled(v.cfg.Doubled, other) {
			delete(next, other)
		}
	}
	for len(next) > MaxViewedSessions || (len(next) > 1 && viewedSize(next) > MaxViewedBytes) {
		oldest, at := "", time.Time{}
		for k, m := range next {
			if oldest == "" || m.At.Before(at) {
				oldest, at = k, m.At
			}
		}
		delete(next, oldest)
	}
	if viewedSize(next) > MaxViewedBytes {
		return explained(limited("viewed"), "This session's viewed marks would take more room than the engine keeps. Clear some first.")
	}
	if v.cfg.Store != nil {
		if err := v.cfg.Store.Save(next); err != nil {
			if v.cfg.Logger != nil {
				v.cfg.Logger.Warn("viewed", "event", "not-saved")
			}
			return explained(conflict("not saved"), "The engine couldn't save the mark. Try again.")
		}
	}
	v.marks = next
	return nil
}

func viewedSize(marks map[string]ViewedMarks) int {
	n := 0
	for _, m := range marks {
		for path := range m.Files {
			n += len(path) + viewedEntryBytes
		}
	}
	return n
}

func (v *Viewed) onBranch(ctx context.Context, t PushTarget, path, blob string) error {
	off := explained(conflict("not on branch"), "That version of the file isn't one this session's branch changes, so the engine doesn't mark it viewed. Reload the Changes view and mark the file again.")
	if v.cfg.Heads == nil || v.cfg.Files == nil {
		return off
	}
	head, err := v.cfg.Heads.Head(t.Repo, t.Branch)
	if err != nil {
		return explained(conflict("branch"), "The engine couldn't read the session's branch, so it doesn't mark the file viewed.")
	}
	if at, err := v.cfg.Files.BlobAt(ctx, t.Repo, head, path); err == nil && at == blob {
		return nil
	}
	fork := ""
	if v.cfg.Main != nil && v.cfg.History != nil {
		if main, err := v.cfg.Main(t.Name); err == nil && main != "" {
			if fork, err = v.cfg.History.MergeBase(ctx, t.Repo, head, main); err != nil {
				return off
			}
		}
	}
	changes, err := v.cfg.Files.ChangedFiles(ctx, t.Repo, fork, head)
	if err != nil {
		return off
	}
	for _, c := range changes {
		if maskLines(c.Path, true) == path && (c.NewSHA == blob || c.OldSHA == blob) {
			return nil
		}
	}
	return off
}
