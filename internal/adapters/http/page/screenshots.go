package page

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	screenshotsRoute = "/api/screenshots"
	screenshotRoute  = "/api/screenshot"
)

var shotRoutes = map[string]bool{screenshotsRoute: true, screenshotRoute: true}

type Screenshots struct {
	Workspace string
}

var _ session.ScreenshotFiles = Screenshots{}

func localName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00")
}

const (
	shotsTopMode    = 0o775 | os.ModeSetgid | os.ModeSticky
	shotsFolderMode = 0o775 | os.ModeSetgid
)

func groupFolder(root *os.Root, name string, mode fs.FileMode) error {
	if err := root.Mkdir(name, 0o775); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return errMissing
	}
	if info.Mode()&(fs.ModePerm|os.ModeSetgid|os.ModeSticky) == mode {
		return nil
	}
	if err := root.Chmod(name, mode); err == nil || mode&os.ModeSetgid == 0 {
		return err
	}
	return root.Chmod(name, mode&^os.ModeSetgid)
}

func (s Screenshots) Prepare() error {
	if s.Workspace == "" {
		return errOutside
	}
	ws, err := os.OpenRoot(s.Workspace)
	if err != nil {
		return errOutside
	}
	defer func() { _ = ws.Close() }()
	return groupFolder(ws, session.ScreenshotsFolder, shotsTopMode)
}

func (s Screenshots) Make(sid string) error {
	if !localName(sid) {
		return errOutside
	}
	if err := s.Prepare(); err != nil {
		return err
	}
	top, err := s.top()
	if err != nil {
		return err
	}
	defer func() { _ = top.Close() }()
	return groupFolder(top, sid, shotsFolderMode)
}

func (s Screenshots) Entries() ([]string, error) {
	top, err := s.top()
	if errors.Is(err, errMissing) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = top.Close() }()
	f, err := top.Open(".")
	if err != nil {
		return nil, errMissing
	}
	entries, _ := folderEntries(f)
	_ = f.Close()
	out := []string{}
	for _, e := range entries {
		if localName(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

func (s Screenshots) top() (*os.Root, error) {
	if s.Workspace == "" {
		return nil, errOutside
	}
	ws, err := os.OpenRoot(s.Workspace)
	if err != nil {
		return nil, errOutside
	}
	defer func() { _ = ws.Close() }()
	info, err := ws.Lstat(session.ScreenshotsFolder)
	if err != nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return nil, errMissing
	}
	return ws.OpenRoot(session.ScreenshotsFolder)
}

func (s Screenshots) folder(sid string) (*checkout, error) {
	if !localName(sid) {
		return nil, errOutside
	}
	top, err := s.top()
	if err != nil {
		return nil, err
	}
	defer func() { _ = top.Close() }()
	info, err := top.Lstat(sid)
	if err != nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return nil, errMissing
	}
	root, err := top.OpenRoot(sid)
	if err != nil {
		return nil, errMissing
	}
	return &checkout{dir: sid, root: root}, nil
}

func (s Screenshots) List(sid string) ([]session.ScreenshotFile, error) {
	c, err := s.folder(sid)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	f, err := c.root.Open(".")
	if err != nil {
		return nil, errMissing
	}
	entries, _ := folderEntries(f)
	_ = f.Close()
	out := []session.ScreenshotFile{}
	for _, e := range entries {
		if !e.Type().IsRegular() || !session.ScreenshotName(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		out = append(out, session.ScreenshotFile{Name: e.Name(), Size: info.Size(), At: info.ModTime()})
	}
	return out, nil
}

func (s Screenshots) Read(sid, name string, limit int64) ([]byte, error) {
	if !localName(name) {
		return nil, errOutside
	}
	c, err := s.folder(sid)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	data, err := c.read(name, limit)
	if errors.Is(err, errLarge) {
		return nil, session.ErrScreenshotTooBig
	}
	return data, err
}

func (s Screenshots) Drop(sid string) error {
	if !localName(sid) {
		return errOutside
	}
	top, err := s.top()
	if errors.Is(err, errMissing) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = top.Close() }()
	if _, err := top.Lstat(sid); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return top.RemoveAll(sid)
}

var errShotStays = errors.New("screenshots entry still there")

func (s Screenshots) Gone(name string) error {
	if !localName(name) {
		return errOutside
	}
	top, err := s.top()
	if errors.Is(err, errMissing) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = top.Close() }()
	_, err = top.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err == nil {
		return errShotStays
	}
	return err
}

type screenshotJSON struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	At   int64  `json:"at"`
}

type screenshotImageJSON struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Data   []byte `json:"data"`
}

func (s *Server) screenshots(w http.ResponseWriter, route string, body map[string]any) {
	allowed := map[string][]string{screenshotsRoute: {"sid"}, screenshotRoute: {"sid", "name"}}
	sid, ok := body["sid"].(string)
	if !keysWithin(body, allowed[route]...) || !ok || sid == "" || len(sid) > 80 {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	if route == screenshotsRoute {
		list, err := s.reg.Screenshots(sid)
		if err != nil {
			code, p := status(err)
			send(w, code, p)
			return
		}
		out := make([]screenshotJSON, 0, len(list))
		for _, f := range list {
			out = append(out, screenshotJSON{Name: f.Name, Size: f.Size, At: f.At.UnixMilli()})
		}
		send(w, http.StatusOK, map[string]any{"screenshots": out, "max": session.MaxScreenshotsShown})
		return
	}
	name, ok := body["name"].(string)
	if !ok || name == "" || len(name) > 255 {
		refuse(w, http.StatusBadRequest, "name")
		return
	}
	shot, err := s.reg.Screenshot(sid, name)
	s.logger.Info("screenshot read", "ok", err == nil)
	if err != nil {
		code, p := status(err)
		send(w, code, p)
		return
	}
	send(w, http.StatusOK, screenshotImageJSON{Name: shot.Name, Type: "image/png", Width: shot.Width, Height: shot.Height, Data: shot.PNG})
}
