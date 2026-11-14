package files

import (
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
	"github.com/pjbgf/sha1cd"

	"github.com/bezmoradi/software-factory/internal/core/push"
	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	maxWorktreeEntries = 200_000
	maxTrackedEntries  = 500_000
	maxHashedBytes     = 2 << 30
	maxIgnoreFile      = 256 << 10
	maxIgnorePatterns  = 20_000
	maxWorktreeDepth   = 256
)

var worktreeFolder = regexp.MustCompile(`^sf-[0-9a-f]{8}$`)

type Worktrees struct {
	Workspace string
}

var _ session.WorktreeReader = Worktrees{}

type tracked struct {
	mode push.Mode
	sha  string
	seen bool
}

type worktreeWalk struct {
	root     *os.Root
	files    map[string]*tracked
	folders  map[string]bool
	changed  []string
	count    int
	entries  int
	hashed   int64
	cut      bool
	patterns int
}

func (w Worktrees) open(dir string) (*os.Root, error) {
	if w.Workspace == "" {
		return nil, errors.New("the engine has no workspace")
	}
	clean := filepath.Clean(dir)
	if filepath.Dir(clean) != filepath.Join(w.Workspace, "worktrees") || !worktreeFolder.MatchString(filepath.Base(clean)) {
		return nil, errors.New("the session's folder isn't one of the workspace's worktrees")
	}
	real, err := filepath.EvalSymlinks(clean)
	if err != nil || real != clean {
		return nil, errors.New("the session's folder goes through a symbolic link")
	}
	return openRealFolder(clean)
}

func (w Worktrees) Uncommitted(repoPath, dir, commit string) (session.Uncommitted, error) {
	root, err := w.open(dir)
	if err != nil {
		return session.Uncommitted{}, err
	}
	defer func() { _ = root.Close() }()
	walk := &worktreeWalk{root: root, files: map[string]*tracked{}, folders: map[string]bool{}}
	var base []gitignore.Pattern
	_, err = with(repoPath, func(r *repo) (struct{}, error) {
		c, err := r.commit(commit)
		if err != nil {
			return struct{}{}, err
		}
		if err := walk.track(r, c.Tree, "", 0); err != nil {
			return struct{}{}, err
		}
		if data, err := readSmall(r.root, "info/exclude", maxIgnoreFile); err == nil {
			base = walk.parse(data, nil)
		}
		return struct{}{}, nil
	})
	if err != nil {
		return session.Uncommitted{}, err
	}
	walk.folder("", base, 0)
	for p, t := range walk.files {
		if !t.seen {
			walk.add(p)
		}
	}
	slices.Sort(walk.changed)
	return session.Uncommitted{Count: walk.count, Paths: walk.changed, Cut: walk.cut}, nil
}

func (w *worktreeWalk) track(r *repo, tree, prefix string, depth int) error {
	if depth > maxTreeDepth {
		return push.ErrTooLarge
	}
	list, err := r.tree(tree)
	if err != nil {
		return err
	}
	for _, e := range list {
		p := prefix + e.name
		if e.mode == push.ModeFolder {
			w.folders[p] = true
			if err := w.track(r, e.sha, p+"/", depth+1); err != nil {
				return err
			}
			continue
		}
		if len(w.files) >= maxTrackedEntries {
			return push.ErrTooLarge
		}
		w.files[p] = &tracked{mode: e.mode, sha: e.sha}
	}
	return nil
}

func (w *worktreeWalk) add(p string) {
	w.count++
	if len(w.changed) < session.MaxUncommittedShown {
		w.changed = append(w.changed, p)
	}
}

func (w *worktreeWalk) parse(data []byte, domain []string) []gitignore.Pattern {
	var out []gitignore.Pattern
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if w.patterns >= maxIgnorePatterns {
			w.cut = true
			break
		}
		w.patterns++
		out = append(out, gitignore.ParsePattern(line, domain))
	}
	return out
}

func split(p string) []string {
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

func (w *worktreeWalk) folder(rel string, patterns []gitignore.Pattern, depth int) {
	if depth > maxWorktreeDepth {
		w.cut = true
		return
	}
	name := rel
	if name == "" {
		name = "."
	}
	if data, err := readSmall(w.root, path.Join(name, ".gitignore"), maxIgnoreFile); err == nil {
		patterns = append(slices.Clip(patterns), w.parse(data, split(rel))...)
	}
	f, err := w.root.Open(name)
	if err != nil {
		w.cut = true
		return
	}
	list, err := f.ReadDir(-1)
	_ = f.Close()
	if err != nil {
		w.cut = true
		return
	}
	match := gitignore.NewMatcher(patterns)
	for _, e := range list {
		if w.entries >= maxWorktreeEntries {
			w.cut = true
			return
		}
		w.entries++
		if e.Name() == ".git" {
			continue
		}
		p := e.Name()
		if rel != "" {
			p = rel + "/" + e.Name()
		}
		info, err := w.root.Lstat(p)
		if err != nil {
			w.cut = true
			continue
		}
		t := w.files[p]
		if t != nil {
			t.seen = true
		}
		switch {
		case info.IsDir():
			if t != nil && t.mode == push.ModeSubmodule {
				continue
			}
			if t != nil {
				w.add(p)
			}
			if !w.folders[p] && match.Match(split(p), true) {
				continue
			}
			if !w.folders[p] && present(w.root, p+"/.git") {
				w.add(p + "/")
				continue
			}
			w.folder(p, patterns, depth+1)
		case t == nil:
			if !match.Match(split(p), false) {
				w.add(p)
			}
		case !w.same(p, info, t):
			w.add(p)
		}
	}
}

func (w *worktreeWalk) same(p string, info fs.FileInfo, t *tracked) bool {
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		if t.mode != push.ModeSymlink {
			return false
		}
		target, err := w.root.Readlink(p)
		return err == nil && digest(kindBlob, []byte(target)) == t.sha
	case !info.Mode().IsRegular():
		return false
	case t.mode != push.ModeFile && t.mode != push.ModeExecutable:
		return false
	case (info.Mode().Perm()&0o111 != 0) != (t.mode == push.ModeExecutable):
		return false
	}
	if w.hashed+info.Size() > maxHashedBytes {
		w.cut = true
		return true
	}
	w.hashed += info.Size()
	f, err := w.root.Open(p)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	h := sha1cd.New()
	_, _ = io.WriteString(h, "blob "+strconv.FormatInt(info.Size(), 10)+"\x00")
	n, err := io.Copy(h, io.LimitReader(f, info.Size()+1))
	if err != nil || n != info.Size() {
		return false
	}
	return hex.EncodeToString(h.Sum(nil)) == t.sha
}
