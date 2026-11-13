package push

import (
	"errors"
	"time"
)

var (
	ErrNotRepository = errors.New("not a git repository the engine can read")
	ErrNotFound      = errors.New("not found")
	ErrSubstituted   = errors.New("the repository has replace refs or grafts, which make git show other content for a commit than a push sends")
	ErrUnsupported   = errors.New("unsupported repository format")
	ErrCorrupt       = errors.New("an object's content doesn't match its name or can't be parsed")
	ErrTooLarge      = errors.New("over a size limit")
	ErrUnsafeTree    = errors.New("the tree can't be written safely")
)

type Mode string

const (
	ModeFile       Mode = "file"
	ModeExecutable Mode = "executable"
	ModeSymlink    Mode = "symlink"
	ModeSubmodule  Mode = "submodule"
	ModeFolder     Mode = "folder"
)

type Status string

const (
	Added       Status = "added"
	Modified    Status = "modified"
	Deleted     Status = "deleted"
	TypeChanged Status = "typechanged"
)

type Commit struct {
	SHA       string
	Tree      string
	Parents   []string
	Committed time.Time
	Message   string
}

type Change struct {
	Path    string
	Status  Status
	OldMode Mode
	NewMode Mode
	OldSHA  string
	NewSHA  string
}

type File struct {
	Mode Mode
	SHA  string
	Data []byte
}

type Object struct {
	SHA  string
	Kind string
	Data []byte
}

type GitReader interface {
	IsAncestor(repo, ancestor, commit string) (bool, error)
	ObjectsToSend(repo, commit string, remoteHas []string) ([]Object, error)
}
