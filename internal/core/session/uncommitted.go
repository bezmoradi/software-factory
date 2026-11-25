package session

import (
	"context"
	"slices"
	"sync"
	"time"
)

const (
	MaxUncommittedShown = 200
	maxChangedPaths     = 5000
	worktreeCheckGap    = 2 * time.Second
	worktreeReadAgain   = 30 * time.Second
)

type WorktreeState int

const (
	WorktreeUnread WorktreeState = iota
	WorktreeUnreadable
	WorktreeBusy
	WorktreeSettled
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
	failed      map[string]bool
	asked       map[string]time.Time
	tools       map[string]map[string]pendingCheck
	edits       map[string]map[string]bool
	checks      map[string]map[CheckKind]AgentCheck
}

func newWorktreeBook() *worktreeBook {
	return &worktreeBook{uncommitted: map[string]Uncommitted{}, changed: map[string]changedSet{}, busy: map[string]bool{}, again: map[string]bool{},
		failed: map[string]bool{}, asked: map[string]time.Time{},
		tools: map[string]map[string]pendingCheck{}, edits: map[string]map[string]bool{}, checks: map[string]map[CheckKind]AgentCheck{}}
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
		r.copyAfterTurn(sid)
	}
}

func (r *Registry) CheckWorktree(sid string) {
	r.mu.Lock()
	b := r.bookLocked()
	r.mu.Unlock()
	b.mu.Lock()
	w := b.watch
	if !w.usable() {
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
			delete(b.failed, sid)
		} else {
			b.failed[sid] = true
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

func (w *WorktreeWatch) usable() bool {
	return w != nil && w.Targets != nil && w.Heads != nil && w.Reader != nil
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
			if base, err := w.History.MergeBase(context.Background(), t.Repo, head, main); err == nil && base != "" && base != head {
				if list, err := w.Commits.ChangedFiles(context.Background(), t.Repo, base, head); err == nil {
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
	delete(b.edits, sid)
	delete(b.checks, sid)
	delete(b.failed, sid)
	delete(b.asked, sid)
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

func (r *Registry) SettledUncommitted(sid string) (Uncommitted, WorktreeState) {
	if info, ok := r.Session(sid); !ok || info.Busy {
		return Uncommitted{}, WorktreeBusy
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	b := r.worktrees
	if b == nil {
		return Uncommitted{}, WorktreeUnreadable
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.watch.usable() {
		return Uncommitted{}, WorktreeUnreadable
	}
	if b.busy[sid] {
		return Uncommitted{}, WorktreeUnread
	}
	if u, ok := b.uncommitted[sid]; ok {
		return u.clone(), WorktreeSettled
	}
	now := r.now()
	if at, ok := b.asked[sid]; ok && now.Sub(at) < worktreeReadAgain {
		if b.failed[sid] {
			return Uncommitted{}, WorktreeUnreadable
		}
		return Uncommitted{}, WorktreeUnread
	}
	b.asked[sid] = now
	go r.CheckWorktree(sid)
	return Uncommitted{}, WorktreeUnread
}

func (r *Registry) AgentChecks(sid string) []AgentCheck {
	r.mu.Lock()
	defer r.mu.Unlock()
	b := r.worktrees
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.checksOf(sid)
}
