package codex

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/bezmoradi/software-factory/internal/core/agent"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

type Opener struct {
	Logger shared.Logger
}

var _ session.Opener = Opener{}

type stdioServer struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env,omitempty"`
}

func (o Opener) Open(ctx context.Context, rw io.ReadWriteCloser, l session.Launch) (agent.Session, error) {
	cfg := Config{Cwd: l.Cwd, ClientVersion: l.Version, Logger: o.Logger}
	if l.MCP != nil {
		args := l.MCP.Args
		if args == nil {
			args = []string{}
		}
		raw, err := json.Marshal(stdioServer{Command: l.MCP.Command, Args: args, Env: l.MCP.Env})
		if err != nil {
			_ = rw.Close()
			return nil, err
		}
		cfg.MCPServers = map[string]json.RawMessage{l.MCP.Name: raw}
	}
	c, err := Start(ctx, rw, cfg)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, errors.New("codex app-server didn't answer in time")
		}
		return nil, err
	}
	return c, nil
}
