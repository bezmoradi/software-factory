package agenthost

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	clearRoute     = "/v1/workspace/clear"
	maxClearListed = 10000
	maxClearDepth  = 64
)

type clearReply struct {
	OK   bool `json:"ok"`
	Left int  `json:"left"`
}

var _ session.WorkspaceClearer = (*Client)(nil)

func (s *Server) clearWorkspace() (int, *problem) {
	root, err := os.OpenRoot(s.cfg.Workspace)
	if err != nil {
		return 0, fail(http.StatusInternalServerError, "workspace", "agent-host couldn't open its workspace folder.")
	}
	defer func() { _ = root.Close() }()
	f, err := root.Open(".")
	if err != nil {
		return 0, fail(http.StatusInternalServerError, "workspace", "agent-host couldn't read its workspace folder.")
	}
	entries, err := f.ReadDir(maxClearListed)
	_ = f.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		return 0, fail(http.StatusInternalServerError, "workspace", "agent-host couldn't read its workspace folder.")
	}
	s.wtMu.Lock()
	defer s.wtMu.Unlock()
	left := 0
	for _, e := range entries {
		left += clearEntry(root, e.Name(), 0)
	}
	s.log.Info("agent-host workspace", "event", "cleared", "left", left)
	return left, nil
}

func clearEntry(root *os.Root, name string, depth int) int {
	if root.RemoveAll(name) == nil {
		return 0
	}
	info, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return 0
	}
	if err != nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 || depth >= maxClearDepth {
		return 1
	}
	_ = root.Chmod(name, 0o770|os.ModeSetgid)
	f, err := root.Open(name)
	if err != nil {
		return 1
	}
	inner, _ := f.ReadDir(maxClearListed)
	_ = f.Close()
	left := 0
	for _, e := range inner {
		left += clearEntry(root, name+"/"+e.Name(), depth+1)
	}
	if left == 0 {
		if err := root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return 1
		}
	}
	return left
}

type clearJSON struct {
	Box string `json:"box"`
}

func (s *Server) ownBox() string {
	box := filepath.Base(s.cfg.Workspace)
	if filepath.Base(filepath.Dir(s.cfg.Workspace)) != session.BoxesFolder || !session.ValidBox(box) {
		return ""
	}
	return box
}

func (s *Server) sameBox(box string) *problem {
	if box != s.ownBox() {
		s.log.Warn("agent-host", "event", "other-box-refused")
		return fail(http.StatusConflict, "box", "This agent-host serves another session's container.")
	}
	return nil
}

func (s *Server) clear(w http.ResponseWriter, r *http.Request) {
	var q clearJSON
	if p := decode(w, r, &q); p != nil {
		refuse(w, p)
		return
	}
	if p := s.sameBox(q.Box); p != nil {
		refuse(w, p)
		return
	}
	left, p := s.clearWorkspace()
	if p != nil {
		refuse(w, p)
		return
	}
	send(w, http.StatusOK, clearReply{OK: true, Left: left})
}

func (c *Client) ClearWorkspace(ctx context.Context, box string) error {
	var out clearReply
	return c.call(ctx, http.MethodPost, clearRoute, clearJSON{Box: box}, &out)
}
