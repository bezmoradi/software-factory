package session

import (
	"context"

	"github.com/bezmoradi/software-factory/internal/core/push"
)

type Mains interface {
	Fresh(ctx context.Context, name string) (string, error)
}

type History interface {
	IsAncestor(repo, ancestor, commit string) (bool, error)
	MergeBase(repo, a, b string) (string, error)
}

type Heads interface {
	Head(repo, branch string) (string, error)
}

type ChangeSet struct {
	Commits []push.Commit
	More    bool
	Files   []push.Change
	Diff    string
	Clipped bool
}

type ChangeReader interface {
	Changes(repo, base, head string) (ChangeSet, error)
}
