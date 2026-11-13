package files

import (
	"bytes"
	"container/heap"
	"fmt"
	"sort"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/push"
)

const (
	maxTreeDepth   = 2048
	maxEntries     = 1_000_000
	maxCommits     = 1_000_000
	maxSendBytes   = 128 << 20
	maxSendObjects = 250_000
	maxTagDepth    = 10
)

type Reader struct{}

var _ push.GitReader = Reader{}

func with[T any](path string, f func(*repo) (T, error)) (T, error) {
	r, err := open(path)
	if err != nil {
		var zero T
		return zero, err
	}
	defer r.close()
	return f(r)
}

func (Reader) ResolveRef(path, name string) (string, error) {
	return with(path, func(r *repo) (string, error) {
		return r.resolve(name)
	})
}

func (Reader) Commit(path, sha string) (push.Commit, error) {
	return with(path, func(r *repo) (push.Commit, error) {
		return r.commit(sha)
	})
}

func (Reader) IsAncestor(path, ancestor, commit string) (bool, error) {
	return with(path, func(r *repo) (bool, error) {
		if _, err := r.commit(ancestor); err != nil {
			return false, err
		}
		seen := map[string]bool{}
		queue := []string{commit}
		for len(queue) > 0 {
			sha := queue[0]
			queue = queue[1:]
			if sha == ancestor {
				return true, nil
			}
			if seen[sha] {
				continue
			}
			seen[sha] = true
			if len(seen) > maxCommits {
				return false, fmt.Errorf("%w: more than %d commits to walk", push.ErrTooLarge, maxCommits)
			}
			c, err := r.commit(sha)
			if err != nil {
				return false, err
			}
			queue = append(queue, c.Parents...)
		}
		return false, nil
	})
}

func (Reader) ChangedFiles(path, from, to string) ([]push.Change, error) {
	return with(path, func(r *repo) ([]push.Change, error) {
		var left string
		if from != "" {
			c, err := r.commit(from)
			if err != nil {
				return nil, err
			}
			left = c.Tree
		}
		c, err := r.commit(to)
		if err != nil {
			return nil, err
		}
		d := differ{r: r}
		if err := d.trees(left, c.Tree, "", 0); err != nil {
			return nil, err
		}
		sort.Slice(d.out, func(i, j int) bool { return d.out[i].Path < d.out[j].Path })
		return d.out, nil
	})
}

type differ struct {
	r       *repo
	out     []push.Change
	visited int
}

func (d *differ) entries(sha string) (map[string]entry, func(), error) {
	m := map[string]entry{}
	if sha == "" {
		return m, func() {}, nil
	}
	list, release, err := d.r.heldTree(sha)
	if err != nil {
		return nil, nil, err
	}
	for _, e := range list {
		m[e.name] = e
	}
	return m, release, nil
}

func (d *differ) trees(a, b, prefix string, depth int) error {
	if a == b {
		return nil
	}
	if depth > maxTreeDepth {
		return fmt.Errorf("%w: trees nest deeper than %d", push.ErrTooLarge, maxTreeDepth)
	}
	left, releaseLeft, err := d.entries(a)
	if err != nil {
		return err
	}
	defer releaseLeft()
	right, releaseRight, err := d.entries(b)
	if err != nil {
		return err
	}
	defer releaseRight()
	names := make([]string, 0, len(left)+len(right))
	for n := range left {
		names = append(names, n)
	}
	for n := range right {
		if _, ok := left[n]; !ok {
			names = append(names, n)
		}
	}
	for _, name := range names {
		d.visited++
		if d.visited > maxEntries {
			return fmt.Errorf("%w: more than %d tree entries", push.ErrTooLarge, maxEntries)
		}
		l, inL := left[name]
		rt, inR := right[name]
		if inL && inR && l.sha == rt.sha && l.mode == rt.mode {
			continue
		}
		path, err := d.r.join(prefix, name)
		if err != nil {
			return err
		}
		lTree := inL && l.mode == push.ModeFolder
		rTree := inR && rt.mode == push.ModeFolder
		if lTree || rTree {
			var ls, rs string
			if lTree {
				ls = l.sha
			}
			if rTree {
				rs = rt.sha
			}
			sub, err := d.r.join(path, "/")
			if err != nil {
				return err
			}
			if err := d.trees(ls, rs, sub, depth+1); err != nil {
				return err
			}
			if inL && !lTree {
				d.out = append(d.out, push.Change{Path: path, Status: push.Deleted, OldMode: l.mode, OldSHA: l.sha})
			}
			if inR && !rTree {
				d.out = append(d.out, push.Change{Path: path, Status: push.Added, NewMode: rt.mode, NewSHA: rt.sha})
			}
			continue
		}
		c := push.Change{Path: path}
		switch {
		case !inL:
			c.Status, c.NewMode, c.NewSHA = push.Added, rt.mode, rt.sha
		case !inR:
			c.Status, c.OldMode, c.OldSHA = push.Deleted, l.mode, l.sha
		default:
			c.Status, c.OldMode, c.OldSHA, c.NewMode, c.NewSHA = push.Modified, l.mode, l.sha, rt.mode, rt.sha
			if family(l.mode) != family(rt.mode) {
				c.Status = push.TypeChanged
			}
		}
		d.out = append(d.out, c)
	}
	return nil
}

func family(m push.Mode) push.Mode {
	if m == push.ModeExecutable {
		return push.ModeFile
	}
	return m
}

func splitPath(p string) ([]string, error) {
	parts := strings.Split(p, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.IndexByte(part, 0) >= 0 {
			return nil, fmt.Errorf("%w: %q isn't a path in a commit", push.ErrNotFound, short(p))
		}
	}
	return parts, nil
}

func (Reader) ReadFile(path, commit, file string) (push.File, error) {
	return with(path, func(r *repo) (push.File, error) {
		parts, err := splitPath(file)
		if err != nil {
			return push.File{}, err
		}
		c, err := r.commit(commit)
		if err != nil {
			return push.File{}, err
		}
		tree := c.Tree
		for i, part := range parts {
			list, err := r.tree(tree)
			if err != nil {
				return push.File{}, err
			}
			var found *entry
			for k := range list {
				if list[k].name == part {
					found = &list[k]
					break
				}
			}
			if found == nil {
				return push.File{}, fmt.Errorf("%w: %s", push.ErrNotFound, short(file))
			}
			last := i == len(parts)-1
			if found.mode == push.ModeFolder {
				if last {
					return push.File{}, fmt.Errorf("%w: %s is a folder", push.ErrNotFound, short(file))
				}
				tree = found.sha
				continue
			}
			if !last {
				return push.File{}, fmt.Errorf("%w: %s", push.ErrNotFound, short(file))
			}
			if found.mode == push.ModeSubmodule {
				return push.File{Mode: found.mode, SHA: found.sha}, nil
			}
			data, err := r.blob(found.sha)
			if err != nil {
				return push.File{}, err
			}
			return push.File{Mode: found.mode, SHA: found.sha, Data: data}, nil
		}
		return push.File{}, fmt.Errorf("%w: %s", push.ErrNotFound, short(file))
	})
}

const (
	flagNew  = 1
	flagHave = 2
)

type queued struct {
	sha  string
	time int64
}

type byTime []queued

func (q byTime) Len() int           { return len(q) }
func (q byTime) Less(i, j int) bool { return q[i].time > q[j].time }
func (q byTime) Swap(i, j int)      { q[i], q[j] = q[j], q[i] }
func (q *byTime) Push(x any)        { *q = append(*q, x.(queued)) }
func (q *byTime) Pop() any {
	old := *q
	x := old[len(old)-1]
	*q = old[:len(old)-1]
	return x
}

type painter struct {
	r       *repo
	flags   map[string]int
	commits map[string]push.Commit
	queue   byTime
	inQueue map[string]bool
	pending int
}

func (p *painter) mark(sha string, flag int) error {
	old := p.flags[sha]
	now := old | flag
	if now == old {
		return nil
	}
	p.flags[sha] = now
	if p.inQueue[sha] {
		if old&flagHave == 0 && now&flagHave != 0 {
			p.pending--
		}
		return nil
	}
	c, ok := p.commits[sha]
	if !ok {
		var err error
		if c, err = p.r.commit(sha); err != nil {
			return err
		}
		if len(p.commits) >= maxCommits {
			return fmt.Errorf("%w: more than %d commits to walk", push.ErrTooLarge, maxCommits)
		}
		c = linksOnly(c)
		p.commits[sha] = c
	}
	heap.Push(&p.queue, queued{sha: sha, time: c.Committed.Unix()})
	p.inQueue[sha] = true
	if now&flagHave == 0 {
		p.pending++
	}
	return nil
}

func (p *painter) run() error {
	for p.pending > 0 && p.queue.Len() > 0 {
		q := heap.Pop(&p.queue).(queued)
		delete(p.inQueue, q.sha)
		f := p.flags[q.sha]
		if f&flagHave == 0 {
			p.pending--
		}
		flag := flagNew
		if f&flagHave != 0 {
			flag = flagHave
		}
		for _, parent := range p.commits[q.sha].Parents {
			if err := p.mark(parent, flag); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *repo) peel(sha string) (string, bool, error) {
	for range maxTagDepth {
		o, err := r.store.read(sha)
		if err != nil {
			return "", false, nil
		}
		switch o.kind {
		case kindCommit:
			return sha, true, nil
		case kindTag:
			if len(o.data) > maxTagObject {
				return "", false, fmt.Errorf("%w: tag %s is over %d MiB, more than the engine reads in one tag", push.ErrTooLarge, sha, maxTagObject>>20)
			}
			line, _, _ := bytes.Cut(o.data, []byte("\n"))
			target, ok := strings.CutPrefix(string(line), "object ")
			if !ok || !validSHA(target) {
				return "", false, nil
			}
			sha = target
		default:
			return "", false, nil
		}
	}
	return "", false, nil
}

type collector struct {
	r        *repo
	excluded map[string]bool
	added    map[string]bool
	out      []push.Object
	bytes    int64
	visited  int
}

func (c *collector) exclude(tree string, depth int) error {
	if c.excluded[tree] {
		return nil
	}
	if depth > maxTreeDepth {
		return fmt.Errorf("%w: trees nest deeper than %d", push.ErrTooLarge, maxTreeDepth)
	}
	c.excluded[tree] = true
	list, release, err := c.r.heldTree(tree)
	if err != nil {
		return err
	}
	defer release()
	for _, e := range list {
		c.visited++
		if c.visited > maxEntries*4 {
			return fmt.Errorf("%w: more than %d tree entries", push.ErrTooLarge, maxEntries*4)
		}
		switch e.mode {
		case push.ModeFolder:
			if err := c.exclude(e.sha, depth+1); err != nil {
				return err
			}
		case push.ModeSubmodule:
		default:
			c.excluded[e.sha] = true
		}
	}
	return nil
}

func (c *collector) add(sha string) (object, error) {
	o, err := c.r.store.read(sha)
	if err != nil {
		return object{}, err
	}
	c.added[sha] = true
	c.bytes += int64(len(o.data))
	if c.bytes > maxSendBytes {
		return object{}, fmt.Errorf("%w: the push would send more than %d MiB of new objects, the most the engine sends in one push", push.ErrTooLarge, maxSendBytes>>20)
	}
	if len(c.out) >= maxSendObjects {
		return object{}, fmt.Errorf("%w: the push would send more than %d new objects, the most the engine sends in one push", push.ErrTooLarge, maxSendObjects)
	}
	c.out = append(c.out, push.Object{SHA: sha, Kind: o.kind.String(), Data: o.data})
	return o, nil
}

func (c *collector) tree(sha string, depth int) error {
	if c.excluded[sha] || c.added[sha] {
		return nil
	}
	if depth > maxTreeDepth {
		return fmt.Errorf("%w: trees nest deeper than %d", push.ErrTooLarge, maxTreeDepth)
	}
	o, err := c.add(sha)
	if err != nil {
		return err
	}
	if o.kind != kindTree {
		return fmt.Errorf("%w: %s is a %s, not a tree", push.ErrCorrupt, sha, o.kind)
	}
	list, err := parseTree(sha, o.data)
	if err != nil {
		return err
	}
	release, err := c.r.hold(list)
	if err != nil {
		return err
	}
	defer release()
	for _, e := range list {
		c.visited++
		if c.visited > maxEntries*4 {
			return fmt.Errorf("%w: more than %d tree entries", push.ErrTooLarge, maxEntries*4)
		}
		switch e.mode {
		case push.ModeFolder:
			if err := c.tree(e.sha, depth+1); err != nil {
				return err
			}
		case push.ModeSubmodule:
		default:
			if c.excluded[e.sha] || c.added[e.sha] {
				continue
			}
			o, err := c.add(e.sha)
			if err != nil {
				return err
			}
			if o.kind != kindBlob {
				return fmt.Errorf("%w: %s is a %s, not a blob", push.ErrCorrupt, e.sha, o.kind)
			}
		}
	}
	return nil
}

func (Reader) ObjectsToSend(path, commit string, remoteHas []string) ([]push.Object, error) {
	return with(path, func(r *repo) ([]push.Object, error) {
		if _, err := r.commit(commit); err != nil {
			return nil, err
		}
		p := &painter{r: r, flags: map[string]int{}, commits: map[string]push.Commit{}, inQueue: map[string]bool{}}
		var haves []string
		for _, h := range remoteHas {
			sha, ok, err := r.peel(h)
			if err != nil {
				return nil, err
			}
			if ok {
				haves = append(haves, sha)
				if err := p.mark(sha, flagHave); err != nil {
					return nil, err
				}
			}
		}
		if err := p.mark(commit, flagNew); err != nil {
			return nil, err
		}
		if err := p.run(); err != nil {
			return nil, err
		}
		var fresh []string
		for sha, f := range p.flags {
			if f == flagNew {
				fresh = append(fresh, sha)
			}
		}
		sort.Strings(fresh)
		c := &collector{r: r, excluded: map[string]bool{}, added: map[string]bool{}}
		edges := map[string]bool{}
		for _, h := range haves {
			edges[h] = true
		}
		for _, sha := range fresh {
			for _, parent := range p.commits[sha].Parents {
				if p.flags[parent]&flagHave != 0 {
					edges[parent] = true
				}
			}
		}
		for sha := range edges {
			if err := c.exclude(p.commits[sha].Tree, 0); err != nil {
				return nil, err
			}
		}
		for _, sha := range fresh {
			if _, err := c.add(sha); err != nil {
				return nil, err
			}
			if err := c.tree(p.commits[sha].Tree, 0); err != nil {
				return nil, err
			}
		}
		return c.out, nil
	})
}
