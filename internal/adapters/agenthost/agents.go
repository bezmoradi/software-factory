package agenthost

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	MaxAgentsBody  = 1 << 20
	maxAgentsSent  = 4
	maxAgentsKept  = 32
	agentsFolder   = "agents"
	agentTempNames = ".sf-agent-"
)

type agentOutJSON struct {
	Name string `json:"name"`
	Data []byte `json:"data"`
}

type agentsJSON struct {
	Files []agentOutJSON `json:"files"`
	Keep  []string       `json:"keep"`
}

func (s *Server) agentsStep(ctx context.Context, q agentsJSON) *problem {
	if !s.cfg.Review {
		return fail(http.StatusNotFound, "route", "")
	}
	if len(q.Files) > maxAgentsSent || len(q.Keep) > maxAgentsKept {
		return fail(http.StatusBadRequest, "agents", "Too many agent files in one request.")
	}
	keep := map[string]bool{}
	for _, name := range q.Keep {
		if !session.AgentFileName.MatchString(name) {
			return fail(http.StatusBadRequest, "agents", "An agent file name isn't one the engine writes.")
		}
		keep[name] = true
	}
	for _, f := range q.Files {
		if why := session.CheckAgentFileOut(f.Name, f.Data); why != "" {
			return fail(http.StatusBadRequest, "agents", "An agent file was refused: "+why+".")
		}
		keep[f.Name] = true
	}
	home := os.Getenv("HOME")
	if !filepath.IsAbs(home) {
		return fail(http.StatusConflict, "home", "The container has no usable home folder for Claude Code's agents.")
	}
	s.agentsMu.Lock()
	defer s.agentsMu.Unlock()
	dir := filepath.Join(claudeConfigDir(home), agentsFolder)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fail(http.StatusConflict, "agents-folder", "Claude Code's user agents folder couldn't be made in the container.")
	}
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() || !ownedByMe(info) {
		return fail(http.StatusConflict, "agents-folder", "Claude Code's user agents folder in the container isn't a folder of this user.")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fail(http.StatusConflict, "agents-folder", "Claude Code's user agents folder couldn't be opened.")
	}
	defer func() { _ = root.Close() }()
	for _, f := range q.Files {
		if err := putAgent(root, f.Name, f.Data); err != nil {
			return fail(http.StatusConflict, "agent-file", "An agent file couldn't be written in the container.")
		}
	}
	removed, err := dropAgents(root, keep)
	if err != nil {
		return fail(http.StatusConflict, "agent-file", "An earlier agent file couldn't be removed from the container.")
	}
	shared.For(ctx, s.log).Info("agent files", "written", len(q.Files), "removed", removed)
	return nil
}

func putAgent(root *os.Root, name string, data []byte) error {
	if info, err := root.Lstat(name); err == nil && !info.Mode().IsRegular() {
		return errors.New("not a file")
	}
	tmp := agentTempNames + randomID()
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = root.Remove(tmp) }()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return root.Rename(tmp, name)
}

func dropAgents(root *os.Root, keep map[string]bool) (int, error) {
	d, err := root.Open(".")
	if err != nil {
		return 0, err
	}
	entries, err := d.ReadDir(-1)
	_ = d.Close()
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, e := range entries {
		name := e.Name()
		ours := session.AgentFileName.MatchString(name) || strings.HasPrefix(name, agentTempNames)
		if !ours || keep[name] || e.Type()&fs.ModeType != 0 {
			continue
		}
		if err := root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

func (c *Client) PutAgents(ctx context.Context, files []session.AgentFileOut, keep []string) error {
	q := agentsJSON{Files: make([]agentOutJSON, 0, len(files)), Keep: slices.Clone(keep)}
	if q.Keep == nil {
		q.Keep = []string{}
	}
	for _, f := range files {
		q.Files = append(q.Files, agentOutJSON{Name: f.Name, Data: f.Data})
	}
	return c.call(ctx, http.MethodPost, "/v1/agents", q, nil)
}

var _ session.AgentFileHost = (*Client)(nil)
