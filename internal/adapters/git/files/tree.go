package files

import (
	"github.com/bezmoradi/software-factory/internal/core/push"
	"github.com/bezmoradi/software-factory/internal/core/session"
)

var _ session.MainFiles = Reader{}

func (Reader) ListTree(path, commit, dir string) ([]session.TreeEntry, error) {
	return with(path, func(r *repo) ([]session.TreeEntry, error) {
		c, err := r.commit(commit)
		if err != nil {
			return nil, err
		}
		tree := c.Tree
		if dir != "" {
			parts, err := splitPath(dir)
			if err != nil {
				return nil, err
			}
			for _, part := range parts {
				list, err := r.tree(tree)
				if err != nil {
					return nil, err
				}
				next := ""
				for _, e := range list {
					if e.name == part && e.mode == push.ModeFolder {
						next = e.sha
						break
					}
				}
				if next == "" {
					return nil, nil
				}
				tree = next
			}
		}
		list, err := r.tree(tree)
		if err != nil {
			return nil, err
		}
		out := make([]session.TreeEntry, 0, min(len(list), session.MaxListedEntries))
		for _, e := range list {
			if len(out) >= session.MaxListedEntries {
				break
			}
			out = append(out, session.TreeEntry{Name: e.name, File: e.mode == push.ModeFile || e.mode == push.ModeExecutable, Folder: e.mode == push.ModeFolder})
		}
		return out, nil
	})
}
