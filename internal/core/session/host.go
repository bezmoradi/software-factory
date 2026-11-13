package session

import (
	"context"
	"io"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/agent"
)

type Command struct {
	Argv    []string
	Dir     string
	Pass    []string
	Env     map[string]string
	Timeout time.Duration
}

type RunResult struct {
	Code     int
	Stdout   string
	Stderr   string
	TimedOut bool
}

type Process interface {
	io.ReadWriteCloser
	ID() string
}

type HostError struct {
	Code    string
	Message string
}

func (e *HostError) Error() string {
	if e.Message == "" {
		return "agent-host: " + e.Code
	}
	return "agent-host: " + e.Code + ": " + e.Message
}

const HostDirty = "dirty"

type Host interface {
	AddWorktree(ctx context.Context, repo, name, branch, base string) (string, error)
	RemoveWorktree(ctx context.Context, repo, name string, force bool) error
	Run(ctx context.Context, c Command) (RunResult, error)
	Start(ctx context.Context, c Command) (Process, error)
	Stop(ctx context.Context, id string) error
}

type MCPServer struct {
	Name    string
	Command string
	Args    []string
	Env     map[string]string
}

type Launch struct {
	Cwd         string
	AskingModes []string
	MCP         *MCPServer
	Version     string
}

type Opener interface {
	Open(ctx context.Context, rw io.ReadWriteCloser, l Launch) (agent.Session, error)
}

type Repository struct {
	Name    string
	Path    string
	URL     string
	Branch  string
	AddedAt time.Time
}

type Repositories interface {
	List() ([]Repository, error)
	Fresh(ctx context.Context, name string) (string, error)
}
