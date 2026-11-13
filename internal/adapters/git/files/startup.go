package files

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/bezmoradi/software-factory/internal/core/push"
	"github.com/bezmoradi/software-factory/internal/core/session"
)

type Startup struct{}

var _ session.StartupReader = Startup{}

type startupWalk struct {
	files []session.StartupFile
	bytes int
}

func (Startup) Read(clone, worktree, branch string, entries []session.StartupEntry) (session.StartupRead, error) {
	commit, err := Reviews{}.Head(clone, branch)
	if err != nil {
		return session.StartupRead{}, fmt.Errorf("the session branch's commit can't be read: %s", cause(err))
	}
	w := &startupWalk{}
	roots := map[session.StartupPlace]string{session.InWorktree: worktree, session.InClone: clone}
	for _, e := range entries {
		dir, ok := roots[e.Place]
		if !ok || dir == "" {
			return session.StartupRead{}, fmt.Errorf("no folder for %s", e.Place)
		}
		if err := w.entry(dir, e); err != nil {
			return session.StartupRead{}, err
		}
	}
	if err := markCommitted(clone, commit, w.files); err != nil {
		return session.StartupRead{}, err
	}
	return session.StartupRead{Commit: commit, Files: w.files}, nil
}

func (w *startupWalk) entry(dir string, e session.StartupEntry) error {
	root, err := openUnlinkedFolder(dir)
	if err != nil {
		return fmt.Errorf("the %s folder can't be opened: %s", e.Place, cause(err))
	}
	defer func() { _ = root.Close() }()
	rel := path.Clean(e.Path)
	if !fs.ValidPath(rel) || rel == "." {
		return fmt.Errorf("%q isn't a relative path", e.Path)
	}
	parts := strings.Split(rel, "/")
	for i := 1; i < len(parts); i++ {
		prefix := path.Join(parts[:i]...)
		info, err := root.Lstat(prefix)
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s can't be read: %s", prefix, cause(err))
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			if err := w.record(root, e.Place, prefix, nil); err != nil {
				return err
			}
			if _, err := root.Stat(prefix); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("the link %s points outside the %s folder or can't be followed: %s", prefix, e.Place, cause(err))
			}
			continue
		}
		if !info.IsDir() {
			return nil
		}
	}
	return w.walk(root, e.Place, rel, e.Folder, 0)
}

func openUnlinkedFolder(dir string) (*os.Root, error) {
	clean := filepath.Clean(dir)
	real, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return nil, err
	}
	if real != clean {
		return nil, errors.New("its path goes through a symbolic link")
	}
	return openRealFolder(clean)
}

func (w *startupWalk) room() error {
	if len(w.files) >= session.MaxStartupFiles {
		return fmt.Errorf("more than %d start-up files", session.MaxStartupFiles)
	}
	return nil
}

func (w *startupWalk) link(root *os.Root, place session.StartupPlace, rel string, folder bool, depth int) error {
	info, err := root.Stat(rel)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return w.record(root, place, rel, nil)
	case err != nil:
		return fmt.Errorf("the link %s points outside the %s folder or can't be followed: %s", rel, place, cause(err))
	case info.IsDir() && folder:
		if err := w.record(root, place, rel, nil); err != nil {
			return err
		}
		if depth >= session.MaxStartupDepth {
			return fmt.Errorf("%s is nested deeper than %d folders", rel, session.MaxStartupDepth)
		}
		return w.folder(root, place, rel, depth, true)
	case info.IsDir():
		return w.record(root, place, rel, nil)
	case !info.Mode().IsRegular():
		return fmt.Errorf("the link %s points to something that isn't a regular file or a folder", rel)
	}
	data, err := w.read(root, rel, info, true)
	if err != nil {
		return err
	}
	return w.record(root, place, rel, data)
}

func (w *startupWalk) record(root *os.Root, place session.StartupPlace, rel string, data []byte) error {
	if slices.ContainsFunc(w.files, func(f session.StartupFile) bool { return f.Place == place && f.Path == rel }) {
		return nil
	}
	if err := w.room(); err != nil {
		return err
	}
	target, err := root.Readlink(rel)
	if err != nil {
		return fmt.Errorf("the link %s can't be read: %s", rel, cause(err))
	}
	if len(target) > 4096 {
		return fmt.Errorf("the link %s has a target over 4 KiB", rel)
	}
	w.files = append(w.files, session.StartupFile{Path: rel, Place: place, Link: true, Target: target, Data: data})
	return nil
}

func (w *startupWalk) walk(root *os.Root, place session.StartupPlace, rel string, folder bool, depth int) error {
	info, err := root.Lstat(rel)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s can't be read: %s", rel, cause(err))
	}
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		return w.link(root, place, rel, folder, depth)
	case info.IsDir() && folder:
		if depth >= session.MaxStartupDepth {
			return fmt.Errorf("%s is nested deeper than %d folders", rel, session.MaxStartupDepth)
		}
		return w.folder(root, place, rel, depth, false)
	case info.IsDir():
		return nil
	case info.Mode().IsRegular():
		return w.file(root, place, rel, info)
	}
	return fmt.Errorf("%s isn't a regular file, a folder or a link", rel)
}

func (w *startupWalk) folder(root *os.Root, place session.StartupPlace, rel string, depth int, follow bool) error {
	flags := os.O_RDONLY | syscall.O_DIRECTORY
	if !follow {
		flags |= syscall.O_NOFOLLOW
	}
	d, err := root.OpenFile(rel, flags, 0)
	if err != nil {
		return fmt.Errorf("the folder %s can't be opened: %s", rel, cause(err))
	}
	list, err := d.ReadDir(session.MaxStartupFiles + 1)
	_ = d.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("the folder %s can't be listed: %s", rel, cause(err))
	}
	if len(list) > session.MaxStartupFiles {
		return fmt.Errorf("%s holds more than %d entries", rel, session.MaxStartupFiles)
	}
	names := make([]string, 0, len(list))
	for _, e := range list {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	for _, name := range names {
		if err := w.walk(root, place, path.Join(rel, name), true, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func (w *startupWalk) file(root *os.Root, place session.StartupPlace, rel string, seen fs.FileInfo) error {
	if err := w.room(); err != nil {
		return err
	}
	data, err := w.read(root, rel, seen, false)
	if err != nil {
		return err
	}
	w.files = append(w.files, session.StartupFile{Path: rel, Place: place, Data: data})
	return nil
}

func (w *startupWalk) read(root *os.Root, rel string, seen fs.FileInfo, follow bool) ([]byte, error) {
	if seen.Size() > session.MaxStartupFile {
		return nil, fmt.Errorf("%s is larger than %d KiB", rel, session.MaxStartupFile>>10)
	}
	flags := os.O_RDONLY | syscall.O_NONBLOCK
	if !follow {
		flags |= syscall.O_NOFOLLOW
	}
	f, err := root.OpenFile(rel, flags, 0)
	if err != nil {
		return nil, fmt.Errorf("%s can't be opened: %s", rel, cause(err))
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(info, seen) {
		return nil, fmt.Errorf("%s changed while it was read", rel)
	}
	data, err := io.ReadAll(io.LimitReader(f, session.MaxStartupFile+1))
	if err != nil {
		return nil, fmt.Errorf("%s can't be read: %s", rel, cause(err))
	}
	if len(data) > session.MaxStartupFile {
		return nil, fmt.Errorf("%s is larger than %d KiB", rel, session.MaxStartupFile>>10)
	}
	if w.bytes += len(data); w.bytes > session.MaxStartupBytes {
		return nil, fmt.Errorf("the start-up files hold more than %d MiB", session.MaxStartupBytes>>20)
	}
	return data, nil
}

func markCommitted(clone, commit string, files []session.StartupFile) error {
	if len(files) == 0 {
		return nil
	}
	_, err := with(clone, func(r *repo) (struct{}, error) {
		c, err := r.commit(commit)
		if err != nil {
			return struct{}{}, err
		}
		trees := map[string][]entry{}
		list := func(sha string) ([]entry, error) {
			if l, ok := trees[sha]; ok {
				return l, nil
			}
			l, err := r.tree(sha)
			if err != nil {
				return nil, err
			}
			if _, err := r.hold(l); err != nil {
				return nil, err
			}
			trees[sha] = l
			return l, nil
		}
		for i := range files {
			f := &files[i]
			if f.Place != session.InWorktree {
				continue
			}
			found, err := lookup(list, c.Tree, f.Path)
			if err != nil {
				return struct{}{}, err
			}
			if found == nil {
				continue
			}
			switch {
			case f.Link && found.mode == push.ModeSymlink:
				data, err := r.blob(found.sha)
				if err != nil {
					return struct{}{}, err
				}
				f.Committed = string(data) == f.Target
			case !f.Link && (found.mode == push.ModeFile || found.mode == push.ModeExecutable):
				data, err := r.blob(found.sha)
				if err != nil {
					return struct{}{}, err
				}
				f.Committed = bytes.Equal(data, f.Data)
			}
		}
		return struct{}{}, nil
	})
	if err != nil {
		return fmt.Errorf("the commit's files can't be read: %s", cause(err))
	}
	return nil
}

func lookup(list func(string) ([]entry, error), tree, p string) (*entry, error) {
	parts, err := splitPath(p)
	if err != nil {
		return nil, nil
	}
	for i, part := range parts {
		entries, err := list(tree)
		if err != nil {
			return nil, err
		}
		k := slices.IndexFunc(entries, func(e entry) bool { return e.name == part })
		if k < 0 {
			return nil, nil
		}
		e := entries[k]
		if i == len(parts)-1 {
			return &e, nil
		}
		if e.mode != push.ModeFolder {
			return nil, nil
		}
		tree = e.sha
	}
	return nil, nil
}
