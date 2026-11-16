package files

import (
	"bytes"
	"container/heap"
	"context"
	"errors"
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
	pageNoteHead = "\n[Software Factory: the diff "
	pageWork     = 256 << 20
	pageHash     = 64 << 20
)

const (
	sideA = 1 << iota
	sideB
	stale
)

var (
	_ session.History      = Reader{}
	_ session.ChangeReader = Reader{}
	_ session.BranchWalker = Reader{}
)

const walkCheck = 256

func walked(ctx context.Context, n int) error {
	if n%walkCheck != 0 {
		return nil
	}
	return ctx.Err()
}

func (Reader) MergeBase(ctx context.Context, path, a, b string) (string, error) {
	return with(path, func(r *repo) (string, error) {
		return mergeBase(ctx, r, a, b)
	})
}

func mergeBase(ctx context.Context, r *repo, a, b string) (string, error) {
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
	for n := 1; queue.Len() > 0 && live(); n++ {
		if err := walked(ctx, n); err != nil {
			return "", err
		}
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

func (Reader) Changes(ctx context.Context, path, base, head string) (session.ChangeSet, error) {
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
		if out.Commits, out.More, err = log(ctx, r, head, base); err != nil {
			return out, err
		}
		d := differ{r: r, ctx: ctx}
		if err := d.trees(left, hc.Tree, "", 0); err != nil {
			return out, err
		}
		sortChanges(d.out)
		out.Files = d.out
		from, to, skipped, undiffed, err := pageSides(ctx, r, d.out)
		if err != nil {
			return out, err
		}
		if err := ctx.Err(); err != nil {
			return out, err
		}
		out.NotDiffed, out.UndiffedLines = skipped, undiffed
		for _, n := range skipped {
			if n.Why != session.NotDiffedBinary {
				out.Uncounted = true
			}
		}
		var b strings.Builder
		clipped, err := encodeChange(&b, from, to, changeLimits{out: maxPageDiff, held: maxPageHeld, note: pageNoteHead + "%s and stops here.]\n"})
		if err != nil {
			return out, err
		}
		out.Diff, out.Clipped = b.String(), clipped
		if clipped {
			out.Diff = wholeLines(out.Diff)
		}
		if err := ctx.Err(); err != nil {
			return session.ChangeSet{}, err
		}
		return out, nil
	})
}

func wholeLines(diff string) string {
	at := strings.LastIndex(diff, pageNoteHead)
	if at < 0 {
		return diff
	}
	kept := diff[:at]
	if !strings.HasSuffix(kept, "\n") {
		kept = kept[:strings.LastIndexByte(kept, '\n')+1]
	}
	return kept + diff[at:]
}

func reachable(ctx context.Context, r *repo, base string, limit int) (map[string]bool, error) {
	have := map[string]bool{}
	if base == "" {
		return have, nil
	}
	have[base] = true
	queue := []string{base}
	for n := 1; len(queue) > 0; n++ {
		if err := walked(ctx, n); err != nil {
			return nil, err
		}
		sha := queue[0]
		queue = queue[1:]
		c, err := r.commit(sha)
		if err != nil {
			return nil, err
		}
		for _, p := range c.Parents {
			if !have[p] && len(have) < limit {
				have[p] = true
				queue = append(queue, p)
			}
		}
	}
	return have, nil
}

func log(ctx context.Context, r *repo, head, base string) ([]push.Commit, bool, error) {
	have, err := reachable(ctx, r, base, maxMergeWalk)
	if err != nil {
		return nil, false, err
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
	s.size = func() (int64, bool) {
		p, err := r.store.probe(sha)
		return p.size, err == nil
	}
	return s
}

func (Reader) BranchCommits(ctx context.Context, path, head, stop string, limit int) (map[string]bool, bool, error) {
	type walkedSet struct {
		seen     map[string]bool
		complete bool
	}
	w, err := with(path, func(r *repo) (walkedSet, error) {
		seen := map[string]bool{head: true}
		queue := []string{head}
		for n := 0; len(queue) > 0; n++ {
			if err := walked(ctx, n); err != nil {
				return walkedSet{}, err
			}
			sha := queue[0]
			queue = queue[1:]
			if sha == stop {
				continue
			}
			c, err := r.commit(sha)
			if err != nil {
				return walkedSet{}, err
			}
			for _, p := range c.Parents {
				if seen[p] {
					continue
				}
				if len(seen) >= limit {
					return walkedSet{seen: seen}, nil
				}
				seen[p] = true
				queue = append(queue, p)
			}
		}
		return walkedSet{seen: seen, complete: true}, nil
	})
	return w.seen, w.complete, err
}

func pageSides(ctx context.Context, r *repo, changes []push.Change) (map[string]side, map[string]side, []session.NotDiffed, int64, error) {
	from, to := map[string]side{}, map[string]side{}
	var skipped []session.NotDiffed
	var undiffed int64
	left := int64(maxPageHeld)
	inter := int64(pageWork)
	hashLeft := int64(pageHash)
	for _, c := range changes {
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, 0, err
		}
		why, size, tally := "", int64(0), int64(0)
		mark := func(next string) {
			if notDiffedRank[next] > notDiffedRank[why] {
				why = next
			}
		}
		load := func(mode push.Mode, sha string) (side, error) {
			if mode == push.ModeSubmodule {
				tally++
				return blobSide(r, mode, sha), nil
			}
			s := side{mode: fileMode(mode), big: true}
			p, err := r.store.probe(sha)
			if err := ctx.Err(); err != nil {
				return s, err
			}
			if err != nil {
				mark(session.NotDiffedUnreadable)
				return s, nil
			}
			size = max(size, p.size)
			switch {
			case p.size > maxDiffedFile && p.whole && p.kind == kindBlob && p.size <= maxObject && p.size <= hashLeft && isBinary(p.head):
				hashLeft -= p.size
				binary, lines, err := r.store.verifiedBinary(ctx, sha, p)
				switch {
				case err != nil:
					return s, err
				case !binary:
					mark(session.NotDiffedLarge)
				default:
					tally += lines
					mark(session.NotDiffedBinary)
				}
				return s, nil
			case p.size > maxDiffedFile:
				mark(session.NotDiffedLarge)
				return s, nil
			case p.size > left:
				mark(session.NotDiffedBudget)
				return s, nil
			}
			data, err := r.blobWithin(sha, maxDiffedFile, cacheEntry, &inter)
			if over, ok := errors.AsType[objectLimit](err); ok {
				if over.final {
					size = max(size, over.size)
				}
				if over.budget {
					mark(session.NotDiffedBudget)
				} else {
					mark(session.NotDiffedLarge)
				}
				return s, nil
			}
			if err := ctx.Err(); err != nil {
				return s, err
			}
			if err != nil {
				mark(session.NotDiffedUnreadable)
				return s, nil
			}
			left -= int64(len(data))
			s.big, s.content = false, data
			tally += lineCount(data)
			if isBinary(data) {
				mark(session.NotDiffedBinary)
			}
			return s, nil
		}
		if c.OldSHA != "" {
			s, err := load(c.OldMode, c.OldSHA)
			if err != nil {
				return nil, nil, nil, 0, err
			}
			from[c.Path] = s
		}
		if c.NewSHA != "" {
			s, err := load(c.NewMode, c.NewSHA)
			if err != nil {
				return nil, nil, nil, 0, err
			}
			to[c.Path] = s
		}
		if why == session.NotDiffedBinary {
			undiffed += tally
		}
		if why != "" {
			skipped = append(skipped, session.NotDiffed{Path: c.Path, Size: size, Why: why})
		}
	}
	return from, to, skipped, undiffed, nil
}

var notDiffedRank = map[string]int{
	session.NotDiffedBinary:     1,
	session.NotDiffedBudget:     2,
	session.NotDiffedLarge:      3,
	session.NotDiffedUnreadable: 4,
}

func lineCount(data []byte) int64 {
	n := int64(bytes.Count(data, []byte{'\n'}))
	if len(data) > 0 && data[len(data)-1] != '\n' {
		n++
	}
	return n
}
