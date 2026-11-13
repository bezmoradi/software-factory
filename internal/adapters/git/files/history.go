package files

import (
	"container/heap"
	"fmt"
	"sort"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/push"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/go-git/go-git/v5/plumbing/filemode"
)

const (
	maxMergeWalk = 200_000
	maxLog       = 100
	maxPageDiff  = 1 << 20
	maxPageHeld  = 8 << 20
)

const (
	sideA = 1 << iota
	sideB
	stale
)

var (
	_ session.History      = Reader{}
	_ session.ChangeReader = Reader{}
)

func (Reader) MergeBase(path, a, b string) (string, error) {
	return with(path, func(r *repo) (string, error) {
		return mergeBase(r, a, b)
	})
}

func mergeBase(r *repo, a, b string) (string, error) {
	if a == b {
		if _, err := r.commit(a); err != nil {
			return "", err
		}
		return a, nil
	}
	flags := map[string]int{}
	commits := map[string]push.Commit{}
	var queue byTime
	add := func(sha string, flag int) error {
		c, ok := commits[sha]
		if !ok {
			if len(commits) >= maxMergeWalk {
				return fmt.Errorf("%w: more than %d commits between the branch and the main branch", push.ErrTooLarge, maxMergeWalk)
			}
			var err error
			if c, err = r.commit(sha); err != nil {
				return err
			}
			c = linksOnly(c)
			commits[sha] = c
		}
		flags[sha] |= flag
		heap.Push(&queue, queued{sha: sha, time: c.Committed.Unix()})
		return nil
	}
	if err := add(a, sideA); err != nil {
		return "", err
	}
	if err := add(b, sideB); err != nil {
		return "", err
	}
	live := func() bool {
		for _, q := range queue {
			if flags[q.sha]&stale == 0 {
				return true
			}
		}
		return false
	}
	found := ""
	for queue.Len() > 0 && live() {
		q := heap.Pop(&queue).(queued)
		f := flags[q.sha]
		if f&(sideA|sideB) == sideA|sideB && f&stale == 0 {
			if found == "" {
				found = q.sha
			}
			f |= stale
			flags[q.sha] = f
		}
		carry := f & (sideA | sideB | stale)
		for _, p := range commits[q.sha].Parents {
			if flags[p]&carry == carry {
				continue
			}
			if err := add(p, carry); err != nil {
				return "", err
			}
		}
	}
	return found, nil
}

func (Reader) Changes(path, base, head string) (session.ChangeSet, error) {
	return with(path, func(r *repo) (session.ChangeSet, error) {
		var out session.ChangeSet
		hc, err := r.commit(head)
		if err != nil {
			return out, err
		}
		left := ""
		if base != "" {
			bc, err := r.commit(base)
			if err != nil {
				return out, err
			}
			left = bc.Tree
		}
		if out.Commits, out.More, err = log(r, head, base); err != nil {
			return out, err
		}
		d := differ{r: r}
		if err := d.trees(left, hc.Tree, "", 0); err != nil {
			return out, err
		}
		sortChanges(d.out)
		out.Files = d.out
		from, to := map[string]side{}, map[string]side{}
		for _, c := range d.out {
			if c.OldSHA != "" {
				from[c.Path] = blobSide(r, c.OldMode, c.OldSHA)
			}
			if c.NewSHA != "" {
				to[c.Path] = blobSide(r, c.NewMode, c.NewSHA)
			}
		}
		var b strings.Builder
		clipped, err := encodeChange(&b, from, to, changeLimits{out: maxPageDiff, held: maxPageHeld, note: "\n[Software Factory: the diff %s and stops here.]\n"})
		if err != nil {
			return out, err
		}
		out.Diff, out.Clipped = b.String(), clipped
		return out, nil
	})
}

func log(r *repo, head, base string) ([]push.Commit, bool, error) {
	have := map[string]bool{}
	if base != "" {
		seen := 0
		queue := []string{base}
		for len(queue) > 0 && seen < maxMergeWalk {
			sha := queue[0]
			queue = queue[1:]
			if have[sha] {
				continue
			}
			have[sha] = true
			seen++
			c, err := r.commit(sha)
			if err != nil {
				return nil, false, err
			}
			queue = append(queue, c.Parents...)
		}
	}
	var out []push.Commit
	var queue byTime
	added := map[string]bool{}
	push1 := func(sha string) error {
		if have[sha] || added[sha] {
			return nil
		}
		c, err := r.commit(sha)
		if err != nil {
			return err
		}
		added[sha] = true
		heap.Push(&queue, queued{sha: sha, time: c.Committed.Unix()})
		return nil
	}
	if err := push1(head); err != nil {
		return nil, false, err
	}
	for queue.Len() > 0 {
		if len(out) >= maxLog {
			return out, true, nil
		}
		q := heap.Pop(&queue).(queued)
		c, err := r.commit(q.sha)
		if err != nil {
			return nil, false, err
		}
		out = append(out, c)
		for _, p := range c.Parents {
			if err := push1(p); err != nil {
				return nil, false, err
			}
		}
	}
	return out, false, nil
}

func sortChanges(list []push.Change) {
	sort.Slice(list, func(i, j int) bool { return list[i].Path < list[j].Path })
}

func fileMode(m push.Mode) filemode.FileMode {
	switch m {
	case push.ModeExecutable:
		return filemode.Executable
	case push.ModeSymlink:
		return filemode.Symlink
	case push.ModeSubmodule:
		return filemode.Submodule
	}
	return filemode.Regular
}

func blobSide(r *repo, mode push.Mode, sha string) side {
	s := side{mode: fileMode(mode)}
	if mode == push.ModeSubmodule {
		s.content = []byte("Subproject commit " + sha + "\n")
		return s
	}
	s.load = func() ([]byte, error) { return r.blob(sha) }
	return s
}
