package session

import (
	"errors"

	"github.com/bezmoradi/software-factory/internal/core/push"
)

var ErrNotReviewed = errors.New("not a reviewed commit of this session")

type BoundedFiles interface {
	ReadFileWithin(repo, commit, path string, limit int64) (push.File, error)
}

type ReviewedRead struct {
	File push.File
	Err  error
}

func (w *Work) ReviewedFiles(sid, commit string, paths []string, limit int64) ([]ReviewedRead, error) {
	t, ok := w.cfg.Targets(sid)
	if !ok || !commitSHA.MatchString(commit) || w.cfg.Reviews == nil || w.cfg.Bounded == nil {
		return nil, ErrNotReviewed
	}
	entries, err := w.cfg.Reviews.Entries()
	if err != nil {
		return nil, ErrNotReviewed
	}
	reviewed := false
	for k := range entries {
		if k.Commit == commit && k.Branch == t.Branch {
			reviewed = true
			break
		}
	}
	if !reviewed {
		return nil, ErrNotReviewed
	}
	out := make([]ReviewedRead, 0, len(paths))
	for _, p := range paths {
		f, err := w.cfg.Bounded.ReadFileWithin(t.Repo, commit, p, limit)
		out = append(out, ReviewedRead{File: f, Err: err})
	}
	return out, nil
}
