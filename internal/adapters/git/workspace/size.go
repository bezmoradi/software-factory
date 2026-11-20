package workspace

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	readBatch = 512
	maxDepth  = 64
)

type Disk struct{}

type Sizes struct {
	OpenTree func(path string) (*os.Root, error)
}

var (
	_ session.DiskSpace = Disk{}
	_ session.TreeSizes = Sizes{}
)

var errNotTree = errors.New("not a worktree folder")

func (s Sizes) Size(ctx context.Context, workspace, box, tree string, maxEntries int) (session.TreeSize, error) {
	return s.walk(ctx, session.BoxWorktree(workspace, box, tree), maxEntries, false)
}

func (s Sizes) BoxSize(ctx context.Context, workspace, box string, maxEntries int) (session.TreeSize, error) {
	return s.walk(ctx, session.BoxFolder(workspace, box), maxEntries, true)
}

type openFolder struct {
	dir   *os.File
	at    string
	depth int
	batch []fs.DirEntry
	ended bool
}

func (s Sizes) walk(ctx context.Context, dir string, maxEntries int, whole bool) (session.TreeSize, error) {
	var out session.TreeSize
	if dir == "" || s.OpenTree == nil {
		return out, errNotTree
	}
	root, err := s.OpenTree(dir)
	if err != nil {
		return out, errNotTree
	}
	defer func() { _ = root.Close() }()
	top, err := root.OpenFile(".", openDir, 0)
	if err != nil {
		out.More = whole
		return out, nil
	}
	open := []*openFolder{{dir: top, at: "."}}
	defer func() {
		for _, f := range open {
			_ = f.dir.Close()
		}
	}()
	for len(open) > 0 {
		if ctx.Err() != nil || out.Entries >= maxEntries {
			out.More = true
			return out, nil
		}
		f := open[len(open)-1]
		if len(f.batch) == 0 {
			if f.ended {
				_ = f.dir.Close()
				open = open[:len(open)-1]
				continue
			}
			list, err := f.dir.ReadDir(readBatch)
			f.batch = list
			if err != nil {
				if !errors.Is(err, io.EOF) && !errors.Is(err, fs.ErrNotExist) {
					out.More = true
				}
				f.ended = true
			}
			continue
		}
		e := f.batch[0]
		f.batch = f.batch[1:]
		out.Entries++
		t := e.Type()
		if t.IsRegular() {
			bytes, more, folder := fileSize(f.dir, e, whole)
			out.Bytes += bytes
			out.More = out.More || more
			if !folder {
				continue
			}
			t = fs.ModeDir
		}
		if !t.IsDir() {
			continue
		}
		if f.depth+1 >= maxDepth {
			out.More = true
			continue
		}
		at := path.Join(f.at, e.Name())
		sub, err := openChild(root, f.dir, at, e.Name())
		if err != nil {
			if whole {
				bytes, more := replacedChild(f.dir, e.Name(), err)
				out.Bytes += bytes
				out.More = out.More || more
			}
			continue
		}
		open = append(open, &openFolder{dir: sub, at: at, depth: f.depth + 1})
	}
	return out, nil
}
