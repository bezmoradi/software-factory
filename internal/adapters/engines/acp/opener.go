package acp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/agent"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

type Opener struct {
	Logger shared.Logger
}

var _ session.Opener = Opener{}

type envVariable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type stdioServer struct {
	Name    string        `json:"name"`
	Command string        `json:"command"`
	Args    []string      `json:"args"`
	Env     []envVariable `json:"env"`
}

func mcpEntry(s *session.MCPServer) (json.RawMessage, error) {
	names := make([]string, 0, len(s.Env))
	for k := range s.Env {
		names = append(names, k)
	}
	sort.Strings(names)
	env := make([]envVariable, 0, len(names))
	for _, k := range names {
		env = append(env, envVariable{Name: k, Value: s.Env[k]})
	}
	args := s.Args
	if args == nil {
		args = []string{}
	}
	return json.Marshal(stdioServer{Name: s.Name, Command: s.Command, Args: args, Env: env})
}

func (o Opener) Open(ctx context.Context, rw io.ReadWriteCloser, l session.Launch) (agent.Session, error) {
	cfg := Config{Cwd: l.Cwd, AskingModes: l.AskingModes, ClientVersion: l.Version, Logger: o.Logger}
	if l.MCP != nil {
		entry, err := mcpEntry(l.MCP)
		if err != nil {
			return nil, err
		}
		cfg.MCPServers = []json.RawMessage{entry}
	}
	c := New(rw, cfg)
	if err := c.Start(ctx); err != nil {
		_ = c.Close()
		return nil, explain(err)
	}
	return c, nil
}

func explain(err error) error {
	var auth *AuthRequiredError
	switch {
	case errors.As(err, &auth):
		names := make([]string, 0, len(auth.Methods))
		for _, m := range auth.Methods {
			if m.Name != "" {
				names = append(names, m.Name)
			}
		}
		text := "the agent asks you to sign in first. Sign in inside the agent container with the agent's own login, then press Wake"
		if len(names) > 0 {
			text += " (it offers: " + strings.Join(names, ", ") + ")"
		}
		return errors.New(text)
	case errors.Is(err, ErrNotAsking):
		return errors.New("the agent offers no mode that asks before it acts, so Software Factory doesn't start it")
	case errors.Is(err, ErrProtocolVersion):
		return errors.New("the agent speaks another ACP protocol version")
	case errors.Is(err, context.DeadlineExceeded):
		return errors.New("the agent didn't answer in time")
	}
	return err
}
