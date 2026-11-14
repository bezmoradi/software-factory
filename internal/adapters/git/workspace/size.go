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

type Sizes struct{}

var (
	_ session.DiskSpace = Disk{}
	_ session.TreeSizes = Sizes{}
)

var errNotTree = errors.New("not a worktree folder")

func (Sizes) Size(ctx context.Context, workspace, tree string, maxEntries int) (session.TreeSize, error) {
	var out session.TreeSize
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return out, err
	}
	defer func() { _ = root.Close() }()
	rel := path.Join("worktrees", tree)
	for _, p := range []string{"worktrees", rel} {
		info, err := root.Lstat(p)
		if err != nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
			return out, errNotTree
		}
	}
	sub, err := root.OpenRoot(rel)
	if err != nil {
		return out, err
	}
	defer func() { _ = sub.Close() }()
	type folder struct {
		at    string
		depth int
	}
	stack := []folder{{at: ".", depth: 0}}
	for len(stack) > 0 {
		f := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		dir, err := sub.OpenFile(f.at, openDir, 0)
		if err != nil {
			continue
		}
		for {
			if ctx.Err() != nil || out.Entries >= maxEntries {
				_ = dir.Close()
				out.More = true
				return out, nil
			}
			list, err := dir.ReadDir(readBatch)
			for _, e := range list {
				out.Entries++
				t := e.Type()
				switch {
				case t.IsDir():
					if f.depth+1 >= maxDepth {
						out.More = true
						continue
					}
					stack = append(stack, folder{at: path.Join(f.at, e.Name()), depth: f.depth + 1})
				case t.IsRegular():
					if info, err := e.Info(); err == nil {
						out.Bytes += info.Size()
					}
				}
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					out.More = true
				}
				break
			}
		}
		_ = dir.Close()
	}
	return out, nil
}
