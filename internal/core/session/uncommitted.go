package session

import (
	"slices"
	"sync"
	"time"
)

const (
	MaxUncommittedShown = 200
	maxChangedPaths     = 5000
	worktreeCheckGap    = 2 * time.Second
)

type Uncommitted struct {
	Commit string
	Count  int
	Paths  []string
	Cut    bool
	At     time.Time
}

func (u Uncommitted) clone() Uncommitted {
	u.Paths = slices.Clone(u.Paths)
	return u
}

type WorktreeReader interface {
	Uncommitted(repo, dir, commit string) (Uncommitted, error)
}

type WorktreeWatch struct {
	Targets func(sid string) (PushTarget, bool)
	Heads   Heads
	Reader  WorktreeReader
	Commits ClaimCommits
	History History
	Main    func(name string) (string, error)
}

type changedSet struct {
	repo  string
	paths map[string]bool
	cut   bool
}

type worktreeBook struct {
	mu          sync.Mutex
	watch       *WorktreeWatch
	uncommitted map[string]Uncommitted
	changed     map[string]changedSet
	busy        map[string]bool
	again       map[string]bool
	tools       map[string]map[string]pendingCheck
	checks      map[string]map[CheckKind]AgentCheck
}

func newWorktreeBook() *worktreeBook {
	return &worktreeBook{uncommitted: map[string]Uncommitted{}, changed: map[string]changedSet{}, busy: map[string]bool{}, again: map[string]bool{},
		tools: map[string]map[string]pendingCheck{}, checks: map[string]map[CheckKind]AgentCheck{}}
}

func (r *Registry) WatchWorktrees(w WorktreeWatch) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b := r.bookLocked()
	b.mu.Lock()
	b.watch = &w
	b.mu.Unlock()
}

func (r *Registry) bookLocked() *worktreeBook {
	if r.worktrees == nil {
		r.worktrees = newWorktreeBook()
	}
	return r.worktrees
}

func (r *Registry) noteFacts(sid, kind string, ev map[string]any, now time.Time) {
	if r.admitted[sid] == nil {
		return
	}
	b := r.bookLocked()
	switch kind {
	case "tool", "tool_done":
		b.noteCheck(sid, kind, ev, now)
	case "idle":
		go r.CheckWorktree(sid)
	}
}

func (r *Registry) CheckWorktree(sid string) {
	r.mu.Lock()
	b := r.bookLocked()
	r.mu.Unlock()
	b.mu.Lock()
	w := b.watch
	if w == nil || w.Targets == nil || w.Heads == nil || w.Reader == nil {
		b.mu.Unlock()
		return
	}
	if b.busy[sid] {
		b.again[sid] = true
		b.mu.Unlock()
		return
	}
	b.busy[sid] = true
	b.mu.Unlock()
	for {
		time.Sleep(worktreeCheckGap)
		dir, known := r.Worktree(sid)
		u, set, ok := readWorktree(*w, sid, dir)
		ok = ok && known
		u.At = r.now()
		b.mu.Lock()
		if ok {
			b.uncommitted[sid] = u
			b.changed[sid] = set
		}
		again := b.again[sid]
		delete(b.again, sid)
		if !again {
			delete(b.busy, sid)
		}
		b.mu.Unlock()
		r.mu.Lock()
		if r.admitted[sid] == nil {
			b.forget(sid)
		}
		r.announce(r.now())
		r.mu.Unlock()
		if !again {
			return
		}
	}
}

func readWorktree(w WorktreeWatch, sid, dir string) (Uncommitted, changedSet, bool) {
	t, ok := w.Targets(sid)
	if !ok || dir == "" {
		return Uncommitted{}, changedSet{}, false
	}
	head, err := w.Heads.Head(t.Repo, t.Branch)
	if err != nil {
		return Uncommitted{}, changedSet{}, false
	}
	u, err := w.Reader.Uncommitted(t.Repo, dir, head)
	if err != nil {
		return Uncommitted{}, changedSet{}, false
	}
	u.Commit = head
	set := changedSet{repo: t.Name, paths: map[string]bool{}, cut: u.Cut}
	for _, p := range u.Paths {
		set.paths[p] = true
	}
	if u.Count > len(u.Paths) {
		set.cut = true
	}
	if w.Commits != nil && w.History != nil && w.Main != nil {
		if main, err := w.Main(t.Name); err == nil && main != "" {
			if base, err := w.History.MergeBase(t.Repo, head, main); err == nil && base != "" && base != head {
				if list, err := w.Commits.ChangedFiles(t.Repo, base, head); err == nil {
					for _, c := range list {
						if len(set.paths) >= maxChangedPaths {
							set.cut = true
							break
						}
						set.paths[c.Path] = true
					}
				} else {
					set.cut = true
				}
			}
		}
	}
	return u, set, true
}

func (b *worktreeBook) forget(sid string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.uncommitted, sid)
	delete(b.changed, sid)
	delete(b.tools, sid)
	delete(b.checks, sid)
}

func (r *Registry) factsInto(sid string, info *Info) {
	b := r.worktrees
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if u, ok := b.uncommitted[sid]; ok && u.Count > 0 {
		v := u.clone()
		for i, p := range v.Paths {
			v.Paths[i] = shownPath(p)
		}
		info.Uncommitted = &v
	}
	info.Overlaps = r.overlapsLocked(b, sid)
	info.Checks = b.checksOf(sid)
}
