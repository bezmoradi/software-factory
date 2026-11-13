package files

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
	"syscall"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

type treeWalk struct {
	root     *os.Root
	scan     session.StartupScan
	approved []string
	nested   []session.StartupEntry
	found    []string
	stops    []string
	folders  int
	full     bool
}

func walkTree(dir string, scan session.StartupScan) (*treeWalk, error) {
	root, err := openUnlinkedFolder(dir)
	if err != nil {
		return nil, fmt.Errorf("the worktree folder can't be opened: %s", cause(err))
	}
	defer func() { _ = root.Close() }()
	t := &treeWalk{root: root, scan: scan}
	for _, e := range scan.Entries {
		if e.Place == session.InWorktree && e.Folder {
			t.approved = append(t.approved, path.Clean(e.Path))
		}
	}
	t.visit(".", 0)
	slices.Sort(t.stops)
	return t, nil
}

func (t *treeWalk) stop(why string) {
	if !slices.Contains(t.stops, why) {
		t.stops = append(t.stops, why)
	}
}

func (t *treeWalk) visit(rel string, depth int) {
	if t.full {
		return
	}
	if t.folders++; t.folders > session.MaxWalkFolders {
		t.full = true
		t.stop(fmt.Sprintf("the search stopped after %d folders", session.MaxWalkFolders))
		return
	}
	d, err := t.root.OpenFile(rel, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		t.stop("a folder that couldn't be opened wasn't searched")
		return
	}
	list, err := d.ReadDir(session.MaxWalkEntries + 1)
	_ = d.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		t.stop("a folder that couldn't be listed wasn't searched")
		return
	}
	if len(list) > session.MaxWalkEntries {
		t.stop(fmt.Sprintf("a folder holding more than %d entries wasn't searched", session.MaxWalkEntries))
		return
	}
	slices.SortFunc(list, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	for _, e := range list {
		child := path.Join(rel, e.Name())
		mode := e.Type()
		switch {
		case mode&fs.ModeSymlink != 0:
			if n, ok := session.NestedAt(t.scan.Entries, child); ok {
				t.nested = append(t.nested, n)
			} else if t.scan.Instructions.Matches(child) {
				t.found = append(t.found, child)
			}
		case mode.IsDir():
			if session.WalkSkips(e.Name()) || slices.Contains(t.approved, child) {
				continue
			}
			if n, ok := session.NestedAt(t.scan.Entries, child); ok {
				t.nested = append(t.nested, n)
				continue
			}
			if depth+1 > session.MaxWalkDepth {
				t.stop(fmt.Sprintf("folders nested deeper than %d weren't searched", session.MaxWalkDepth))
				continue
			}
			t.visit(child, depth+1)
		case mode.IsRegular():
			if t.scan.Instructions.Matches(child) {
				t.found = append(t.found, child)
			}
		}
	}
}

func readInstructions(dir string, found []string) ([]session.StartupFile, []string) {
	if len(found) == 0 {
		return nil, nil
	}
	root, err := openUnlinkedFolder(dir)
	if err != nil {
		return nil, []string{"the worktree folder couldn't be opened again, so no instruction file is shown"}
	}
	defer func() { _ = root.Close() }()
	var out []session.StartupFile
	var notes []string
	total := 0
	for _, rel := range found {
		shown := session.Escape([]byte(rel))
		if len(out) >= session.MaxInstructionFiles {
			notes = append(notes, fmt.Sprintf("there are more than %d instruction files: %s and the ones after it aren't listed", session.MaxInstructionFiles, shown))
			break
		}
		f, note := readInstruction(root, rel)
		if note != "" {
			notes = append(notes, shown+" "+note)
			continue
		}
		if total += len(f.Data); total > session.MaxInstructionBytes {
			notes = append(notes, fmt.Sprintf("the instruction files hold more than %d MiB: %s and the ones after it aren't shown", session.MaxInstructionBytes>>20, shown))
			break
		}
		out = append(out, f)
	}
	return out, notes
}

func readAbove(folders, names []string) ([]session.StartupFile, []string) {
	var out []session.StartupFile
	var notes []string
	total := 0
	for _, dir := range folders {
		root, err := openUnlinkedFolder(dir)
		if err != nil {
			notes = append(notes, fmt.Sprintf("the folder %s above the worktree couldn't be opened without following a link, so its instruction files aren't shown", session.Escape([]byte(dir))))
			continue
		}
		for _, rel := range names {
			if _, err := root.Lstat(rel); errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
				continue
			}
			full := path.Join(dir, rel)
			f, note := readInstruction(root, rel)
			if note != "" {
				notes = append(notes, session.Escape([]byte(full))+" "+note)
				continue
			}
			if total += len(f.Data); total > session.MaxInstructionBytes {
				notes = append(notes, fmt.Sprintf("the instruction files above the worktree hold more than %d MiB: %s and the ones after it aren't shown", session.MaxInstructionBytes>>20, session.Escape([]byte(full))))
				_ = root.Close()
				return out, notes
			}
			f.Path, f.Place = full, session.AboveWorktree
			out = append(out, f)
		}
		_ = root.Close()
	}
	return out, notes
}

func readInstruction(root *os.Root, rel string) (session.StartupFile, string) {
	f := session.StartupFile{Path: rel, Place: session.InWorktree}
	info, err := root.Lstat(rel)
	if err != nil {
		return f, "couldn't be read"
	}
	follow := false
	if info.Mode()&fs.ModeSymlink != 0 {
		target, err := root.Readlink(rel)
		if err != nil || len(target) > 4096 {
			return f, "is a link that couldn't be read"
		}
		f.Link, f.Target = true, target
		info, err = root.Stat(rel)
		if err != nil || !info.Mode().IsRegular() {
			return f, ""
		}
		follow = true
	}
	if !info.Mode().IsRegular() {
		return f, "isn't a regular file"
	}
	if info.Size() > session.MaxStartupFile {
		return f, fmt.Sprintf("is larger than %d KiB and isn't shown", session.MaxStartupFile>>10)
	}
	data, err := (&startupWalk{}).read(root, rel, info, follow)
	if err != nil {
		return f, "couldn't be read or changed while it was read"
	}
	f.Data = data
	return f, ""
}
