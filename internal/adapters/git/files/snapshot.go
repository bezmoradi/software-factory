package files

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/push"
)

const (
	maxSnapshotEntries = 500_000
	maxSnapshotBytes   = 8 << 30
	maxLinkTarget      = 4096
	maxLinkHops        = 40
)

type planned struct {
	path   string
	mode   push.Mode
	sha    string
	target string
}

type plan struct {
	r     *repo
	items []planned
	links map[string]string
	kinds map[string]push.Mode
}

func unsafe(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{push.ErrUnsafeTree}, args...)...)
}

func (Reader) WriteTree(path, commit, dest string) error {
	_, err := with(path, func(r *repo) (struct{}, error) {
		return struct{}{}, writeTree(r, commit, dest)
	})
	return err
}

func writeTree(r *repo, commit, dest string) error {
	info, err := os.Lstat(dest)
	if err != nil || !info.IsDir() {
		return unsafe("the snapshot folder isn't a folder")
	}
	names, err := os.ReadDir(dest)
	if err != nil || len(names) > 0 {
		return unsafe("the snapshot folder isn't empty")
	}
	c, err := r.commit(commit)
	if err != nil {
		return err
	}
	p := &plan{r: r, links: map[string]string{}, kinds: map[string]push.Mode{}}
	if err := p.walk(c.Tree, "", 0); err != nil {
		return err
	}
	for _, it := range p.items {
		if it.mode != push.ModeSymlink {
			continue
		}
		if err := p.contained(it.path, it.target); err != nil {
			return err
		}
	}
	root, err := os.OpenRoot(dest)
	if err != nil {
		return unsafe("the snapshot folder can't be opened")
	}
	defer func() { _ = root.Close() }()
	if err := p.write(root); err != nil {
		clear(dest)
		return err
	}
	return nil
}

func clear(dest string) {
	entries, err := os.ReadDir(dest)
	if err != nil {
		return
	}
	for _, e := range entries {
		_ = os.RemoveAll(filepath.Join(dest, e.Name()))
	}
}

func dotGit(name string) bool {
	var b strings.Builder
	for _, r := range name {
		if r >= 0x200c && r <= 0x200f || r >= 0x202a && r <= 0x202e || r >= 0x206a && r <= 0x206f || r == 0xfeff {
			continue
		}
		b.WriteRune(r)
	}
	n := strings.ToLower(strings.TrimRight(b.String(), " ."))
	return n == ".git" || n == "git~1"
}

func fold(p string) string {
	return strings.ToLower(p)
}

func (p *plan) walk(tree, prefix string, depth int) error {
	if depth > maxTreeDepth {
		return unsafe("trees nest deeper than %d", maxTreeDepth)
	}
	list, release, err := p.r.heldTree(tree)
	if err != nil {
		return err
	}
	defer release()
	for _, e := range list {
		if dotGit(e.name) {
			return unsafe("the tree has a .git entry at %s", short(prefix+e.name))
		}
		if !utf8.ValidString(e.name) || strings.ContainsAny(e.name, "\\") {
			return unsafe("the tree has a name the snapshot can't hold at %s", short(prefix+e.name))
		}
		path, err := p.r.join(prefix, e.name)
		if err != nil {
			return err
		}
		key := fold(path)
		if key != path {
			if err := p.r.charge(len(key)); err != nil {
				return err
			}
		}
		if _, dup := p.kinds[key]; dup {
			return unsafe("the tree has two entries that differ only in case at %s", short(path))
		}
		p.kinds[key] = e.mode
		if len(p.items) >= maxSnapshotEntries {
			return fmt.Errorf("%w: more than %d entries in the tree", push.ErrTooLarge, maxSnapshotEntries)
		}
		it := planned{path: path, mode: e.mode, sha: e.sha}
		if e.mode == push.ModeSymlink {
			data, err := p.r.blob(e.sha)
			if err != nil {
				return err
			}
			if len(data) == 0 || len(data) > maxLinkTarget || strings.IndexByte(string(data), 0) >= 0 {
				return unsafe("the symbolic link %s has a target the snapshot can't hold", short(path))
			}
			if err := p.r.charge(len(data)); err != nil {
				return err
			}
			it.target = string(data)
			p.links[key] = it.target
		}
		p.items = append(p.items, it)
		if e.mode == push.ModeFolder {
			sub, err := p.r.join(path, "/")
			if err != nil {
				return err
			}
			if err := p.walk(e.sha, sub, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *plan) contained(link, target string) error {
	dir := strings.Split(link, "/")
	hops := 0
	if _, err := p.follow(dir[:len(dir)-1], target, &hops); err != nil {
		return unsafe("the symbolic link %s points outside the snapshot", short(link))
	}
	return nil
}

func (p *plan) follow(base []string, target string, hops *int) ([]string, error) {
	if strings.HasPrefix(target, "/") {
		return nil, errors.New("absolute")
	}
	stack := append([]string(nil), base...)
	for part := range strings.SplitSeq(target, "/") {
		switch part {
		case "", ".":
			continue
		case "..":
			if len(stack) == 0 {
				return nil, errors.New("escapes")
			}
			stack = stack[:len(stack)-1]
			continue
		}
		stack = append(stack, part)
		next, ok := p.links[fold(strings.Join(stack, "/"))]
		if !ok {
			continue
		}
		*hops++
		if *hops > maxLinkHops {
			return nil, errors.New("loop")
		}
		resolved, err := p.follow(stack[:len(stack)-1], next, hops)
		if err != nil {
			return nil, err
		}
		stack = resolved
	}
	return stack, nil
}

func (p *plan) write(root *os.Root) error {
	var total int64
	for _, it := range p.items {
		name := filepath.FromSlash(it.path)
		switch it.mode {
		case push.ModeFolder, push.ModeSubmodule:
			if err := root.Mkdir(name, 0o755); err != nil {
				return unsafe("%s can't be created: %v", short(it.path), cause(err))
			}
		case push.ModeFile, push.ModeExecutable:
			data, err := p.r.blob(it.sha)
			if err != nil {
				return err
			}
			total += int64(len(data))
			if total > maxSnapshotBytes {
				return fmt.Errorf("%w: the tree holds more than %d GiB", push.ErrTooLarge, maxSnapshotBytes>>30)
			}
			perm := os.FileMode(0o644)
			if it.mode == push.ModeExecutable {
				perm = 0o755
			}
			f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
			if err != nil {
				return unsafe("%s can't be created: %v", short(it.path), cause(err))
			}
			_, werr := f.Write(data)
			cerr := f.Close()
			if werr != nil || cerr != nil {
				return unsafe("%s can't be written", short(it.path))
			}
		}
	}
	for _, it := range p.items {
		if it.mode != push.ModeSymlink {
			continue
		}
		if err := root.Symlink(it.target, filepath.FromSlash(it.path)); err != nil {
			return unsafe("the symbolic link %s can't be created: %v", short(it.path), cause(err))
		}
	}
	return nil
}
