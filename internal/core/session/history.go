package session

import (
	"context"

	"github.com/bezmoradi/software-factory/internal/core/push"
)

type Mains interface {
	Fresh(ctx context.Context, name string) (string, error)
}

type History interface {
	IsAncestor(ctx context.Context, repo, ancestor, commit string) (bool, error)
	MergeBase(ctx context.Context, repo, a, b string) (string, error)
}

type Heads interface {
	Head(repo, branch string) (string, error)
}

type ChangeSet struct {
	Commits   []push.Commit
	More      bool
	Files     []push.Change
	Diff      string
	Clipped   bool
	Uncounted bool
	NotDiffed []NotDiffed

	UndiffedLines int64
}

const (
	NotDiffedLarge      = "large"
	NotDiffedBudget     = "budget"
	NotDiffedBinary     = "binary"
	NotDiffedUnreadable = "unreadable"
)

type NotDiffed struct {
	Path string
	Size int64
	Why  string
}

type BranchWalker interface {
	BranchCommits(ctx context.Context, repo, head, stop string, limit int) (map[string]bool, bool, error)
}

type ChangeReader interface {
	Changes(ctx context.Context, repo, base, head string) (ChangeSet, error)
}
