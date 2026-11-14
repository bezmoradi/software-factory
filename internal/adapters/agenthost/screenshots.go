package agenthost

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	screenshotsRemoveRoute = "/v1/screenshots/remove"
	maxShotEntryName       = 255
	maxShotDepth           = 64
)

type screenshotsRemoveJSON struct {
	Name string `json:"name"`
}

type screenshotsRemoveReply struct {
	OK      bool `json:"ok"`
	Removed bool `json:"removed"`
}

func shotEntryName(name string) bool {
	return name != "" && name != "." && name != ".." && len(name) <= maxShotEntryName && utf8.ValidString(name) && !strings.ContainsAny(name, "/\\\x00")
}

func (s *Server) screenshotsTop() (*os.Root, *problem) {
	if s.cfg.Workspace == "" {
		return nil, fail(http.StatusConflict, "workspace", "agent-host has no workspace folder.")
	}
	ws, err := os.OpenRoot(s.cfg.Workspace)
	if err != nil {
		return nil, fail(http.StatusInternalServerError, "workspace", "agent-host couldn't open its workspace folder.")
	}
	defer func() { _ = ws.Close() }()
	info, err := ws.Lstat(session.ScreenshotsFolder)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return nil, fail(http.StatusConflict, "screenshots", "The workspace's screenshots folder isn't a plain folder.")
	}
	top, err := ws.OpenRoot(session.ScreenshotsFolder)
	if err != nil {
		return nil, fail(http.StatusInternalServerError, "screenshots", "agent-host couldn't open the screenshots folder.")
	}
	return top, nil
}

func ownerWritable(top *os.Root, name string) {
	_ = fs.WalkDir(top.FS(), name, func(p string, d fs.DirEntry, err error) error {
		if d == nil || !d.IsDir() || err != nil {
			return nil
		}
		if strings.Count(p, "/") >= maxShotDepth {
			return fs.SkipDir
		}
		_ = top.Chmod(p, 0o700)
		return nil
	})
}

func (s *Server) removeScreenshots(q screenshotsRemoveJSON) (bool, *problem) {
	if !shotEntryName(q.Name) {
		return false, fail(http.StatusBadRequest, "name", "")
	}
	top, p := s.screenshotsTop()
	if p != nil {
		return false, p
	}
	if top == nil {
		return true, nil
	}
	defer func() { _ = top.Close() }()
	info, err := top.Lstat(q.Name)
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, fail(http.StatusInternalServerError, "screenshots", "agent-host couldn't read the entry.")
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return false, fail(http.StatusConflict, "link", "That entry is a symbolic link; agent-host doesn't remove links.")
	}
	if top.RemoveAll(q.Name) != nil && info.IsDir() {
		ownerWritable(top, q.Name)
		_ = top.RemoveAll(q.Name)
	}
	_, err = top.Lstat(q.Name)
	removed := errors.Is(err, fs.ErrNotExist)
	s.log.Info("agent-host screenshots", "event", shotOutcome(top, q.Name, removed, info.IsDir()))
	return removed, nil
}

func shotOutcome(top *os.Root, name string, removed, folder bool) string {
	if removed {
		return "removed"
	}
	if folder {
		if f, err := top.Open(name); err == nil {
			left, _ := f.ReadDir(1)
			_ = f.Close()
			if len(left) == 0 {
				return "emptied"
			}
		}
	}
	return "not-removed"
}

func (c *Client) RemoveScreenshots(ctx context.Context, name string) (bool, error) {
	var out screenshotsRemoveReply
	if err := c.call(ctx, http.MethodPost, screenshotsRemoveRoute, screenshotsRemoveJSON{Name: name}, &out); err != nil {
		return false, err
	}
	return out.Removed, nil
}
