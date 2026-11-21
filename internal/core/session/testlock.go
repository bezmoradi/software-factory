package session

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/gate"
	"github.com/bezmoradi/software-factory/internal/core/push"
)

const (
	MaxLockedTests      = 2000
	maxLockedBytes      = 256 << 10
	maxAllLockedBytes   = 1 << 20
	maxLockedShown      = 200
	AuditLockTests      = "lock-tests"
	AuditUnlockTests    = "unlock-tests"
	lockedEntryOverhead = 16
)

type TestLock struct {
	Commit     string
	At         time.Time
	Files      map[string]string
	Cut        bool
	BranchLeft int
}

type LockedChange struct {
	Commit  string
	Count   int
	Paths   []string
	Problem string
}

type LockView struct {
	Offered    bool
	Why        string
	Commit     string
	At         time.Time
	Files      int
	Paths      []string
	Cut        bool
	BranchLeft int
	Changed    *LockedChange
}

type TestLocks interface {
	LockKey(sid string) string
	LockedTests(ctx context.Context, sid, head string) *LockedChange
}

var _ TestLocks = (*Work)(nil)

func (l *TestLock) size() int {
	if l == nil {
		return 0
	}
	n := 0
	for p, b := range l.Files {
		n += len(p) + len(b) + lockedEntryOverhead
	}
	return n
}

func (l *TestLock) lowered() map[string]bool {
	out := make(map[string]bool, len(l.Files))
	for p := range l.Files {
		out[strings.ToLower(p)] = true
	}
	return out
}

func lockOffer(g gathered) string {
	switch {
	case g.head == "":
		return "The engine couldn't read the session's branch, so there's nothing to lock yet."
	case g.main != "" && g.base == "":
		return "The engine couldn't find where the session's branch leaves the main branch, so it can't tell whether the branch has commits of its own."
	case g.base == g.head:
		return "The session's branch has no commit the main branch lacks yet. Lock the tests once the agent has committed the ones you want kept."
	}
	return ""
}

func lockable(c push.Change) bool {
	return c.NewSHA != "" && c.NewMode != push.ModeSubmodule && c.NewMode != push.ModeFolder && isTestPath(c.Path)
}

func (w *Work) testsAt(ctx context.Context, repo, base, head string) (files map[string]string, cut bool, left int, err error) {
	files = map[string]string{}
	bytes, full := 0, false
	add := func(c push.Change) bool {
		if _, in := files[c.Path]; in {
			return true
		}
		n := len(c.Path) + len(c.NewSHA) + lockedEntryOverhead
		if full || len(files) >= MaxLockedTests || bytes+n > maxLockedBytes {
			full, cut = true, true
			return false
		}
		files[c.Path] = c.NewSHA
		bytes += n
		return true
	}
	if base != "" {
		changed, err := w.cfg.Files.ChangedFiles(ctx, repo, base, head)
		if err != nil {
			return nil, false, 0, err
		}
		for _, c := range changed {
			if lockable(c) && !add(c) {
				left++
			}
		}
	}
	list, err := w.cfg.Files.ChangedFiles(ctx, repo, "", head)
	if err != nil {
		return nil, false, 0, err
	}
	for _, c := range list {
		if full {
			break
		}
		if lockable(c) {
			add(c)
		}
	}
	return files, cut, left, nil
}

func (w *Work) LockTests(ctx context.Context, sid, head string) error {
	g, err := w.current(ctx, sid)
	if err != nil {
		return err
	}
	if why := lockOffer(g); why != "" {
		return conflict("nothing to lock", why)
	}
	if g.head != head {
		return conflict("changed", "The session's branch moved since the page read it. Look at the new commit, then lock again.")
	}
	files, cut, left, err := w.testsAt(ctx, g.t.Repo, g.base, g.head)
	if err != nil {
		return conflict("unreadable", "The engine's reader couldn't list the files of commit "+shortSHA7(g.head)+", so it locked nothing.")
	}
	if len(files) == 0 {
		return conflict("no tests", "Commit "+shortSHA7(g.head)+" has no file the engine counts as a test (such as *_test.go, *.test.ts, test_*.py or a file in a tests/ folder), so there's nothing to lock.")
	}
	lock := &TestLock{Commit: g.head, At: w.cfg.Now(), Files: files, Cut: cut, BranchLeft: left}
	w.mu.Lock()
	s := w.sessions[sid]
	if s == nil {
		w.mu.Unlock()
		return conflict("not started here", "Only a session this engine started has a workflow here.")
	}
	total := lock.size()
	for id, o := range w.sessions {
		if id != sid {
			total += o.lock.size()
		}
	}
	if total > maxAllLockedBytes {
		w.mu.Unlock()
		return conflict("locks", "The engine keeps at most 1 MiB of locked test paths across all sessions, and this lock would pass it. Unlock the tests of a session you're done with, then lock again.")
	}
	s.lock = lock
	w.mu.Unlock()
	w.persist(sid)
	reason := "the developer locked the " + strconv.Itoa(len(files)) + " test " + plural(len(files), "file", "files") + " shown on the page, at commit " + shortSHA(g.head)
	if cut {
		reason += ", the first " + strconv.Itoa(len(files)) + " of more"
	}
	if left > 0 {
		reason += ", " + strconv.Itoa(left) + " of the test files this branch changed not among them"
	}
	w.audit(AuditEntry{Session: sid, Repository: g.t.Name, Branch: g.t.Branch, Commit: g.head, Kind: AuditLockTests, Reason: reason})
	w.cfg.Logger.Info("tests locked", "files", len(files), "cut", cut, "branchLeft", left)
	w.lockMoved(sid)
	return nil
}

func (w *Work) UnlockTests(sid string) error {
	t, ok := w.cfg.Targets(sid)
	if !ok {
		return notStartedHere(w.cfg.Doubled, sid, "Only a session this engine started has a workflow here.")
	}
	w.mu.Lock()
	s := w.sessions[sid]
	var was *TestLock
	if s != nil {
		was, s.lock = s.lock, nil
	}
	w.mu.Unlock()
	switch {
	case s == nil:
		return conflict("not started here", "Only a session this engine started has a workflow here.")
	case was == nil:
		return conflict("not locked", "This session has no locked tests.")
	}
	w.persist(sid)
	w.unlocked(sid, t, was, "the developer unlocked on the page the tests locked at commit "+shortSHA(was.Commit))
	return nil
}

func (w *Work) unlocked(sid string, t PushTarget, was *TestLock, reason string) {
	w.audit(AuditEntry{Session: sid, Repository: t.Name, Branch: t.Branch, Commit: was.Commit, Kind: AuditUnlockTests, Reason: reason})
	w.cfg.Logger.Info("tests unlocked")
	w.lockMoved(sid)
}

func (w *Work) OnLockMoved(fn func(sid string)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lockShow = fn
}

func (w *Work) lockMoved(sid string) {
	w.mu.Lock()
	fn := w.lockShow
	w.mu.Unlock()
	if fn != nil {
		fn(sid)
	}
}

func (w *Work) lockOf(sid string) *TestLock {
	w.mu.Lock()
	defer w.mu.Unlock()
	if s := w.sessions[sid]; s != nil {
		return s.lock
	}
	return nil
}

func (w *Work) LockKey(sid string) string {
	l := w.lockOf(sid)
	if l == nil {
		return ""
	}
	return l.Commit + "@" + strconv.FormatInt(l.At.UnixNano(), 10)
}

func (w *Work) LockedTests(ctx context.Context, sid, head string) *LockedChange {
	l := w.lockOf(sid)
	if l == nil || head == "" {
		return nil
	}
	t, ok := w.cfg.Targets(sid)
	if !ok {
		return nil
	}
	return w.lockedChange(ctx, t.Repo, l, head)
}

func (w *Work) lockedChange(ctx context.Context, repo string, l *TestLock, head string) *LockedChange {
	out := &LockedChange{Commit: l.Commit}
	if head == l.Commit {
		return out
	}
	var changed []string
	if list, err := w.cfg.Files.ChangedFiles(ctx, repo, l.Commit, head); err == nil {
		for _, c := range list {
			if blob, ok := l.Files[c.Path]; ok && (c.Status == push.Deleted || c.NewSHA != blob) {
				changed = append(changed, c.Path)
			}
		}
	} else if list, err := w.cfg.Files.ChangedFiles(ctx, repo, "", head); err == nil {
		now := map[string]string{}
		for _, c := range list {
			if _, ok := l.Files[c.Path]; ok {
				now[c.Path] = c.NewSHA
			}
		}
		for p, blob := range l.Files {
			if now[p] != blob {
				changed = append(changed, p)
			}
		}
	} else {
		out.Problem = "the engine's reader couldn't compare commit " + shortSHA7(head) + " with the tests locked at " + shortSHA7(l.Commit)
		return out
	}
	slices.Sort(changed)
	out.Count = len(changed)
	for _, p := range changed[:min(len(changed), maxLockedShown)] {
		out.Paths = append(out.Paths, shownPath(p))
	}
	return out
}

func (w *Work) lockView(ctx context.Context, g gathered) LockView {
	v := LockView{Why: lockOffer(g)}
	v.Offered = v.Why == ""
	l := g.ws.lock
	if l == nil {
		return v
	}
	v.Commit, v.At, v.Files, v.Cut, v.BranchLeft = l.Commit, l.At, len(l.Files), l.Cut, l.BranchLeft
	for _, p := range slices.Sorted(maps.Keys(l.Files))[:min(len(l.Files), maxLockedShown)] {
		v.Paths = append(v.Paths, shownPath(p))
	}
	if g.head != "" {
		v.Changed = w.lockedChange(ctx, g.t.Repo, l, g.head)
	}
	return v
}

func (w *Work) lockedWrite(ws workSession, top string, paths []string) string {
	if ws.lock == nil {
		return ""
	}
	locked := ws.lock.lowered()
	for _, p := range paths {
		if gate.LockedWrite(top, p, locked) {
			return "it is one of the tests the person locked at commit " + shortSHA7(ws.lock.Commit)
		}
	}
	return ""
}

func lockedLine(c *LockedChange) string {
	return "Locked tests changed since " + shortSHA7(c.Commit) + ": " + strconv.Itoa(c.Count)
}
