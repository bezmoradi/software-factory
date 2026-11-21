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
	OpenBox   func(workspace, box string) (*os.Root, error)
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

func (s Screenshots) box(box string) (*os.Root, error) {
	if s.Workspace == "" || s.OpenBox == nil || !session.ValidBox(box) {
		return nil, errOutside
	}
	root, err := s.OpenBox(s.Workspace, box)
	if err != nil {
		return nil, errMissing
	}
	return root, nil
}

func (s Screenshots) Make(box, sid string) error {
	if !localName(sid) {
		return errOutside
	}
	root, err := s.box(box)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err := groupFolder(root, session.ScreenshotsFolder, shotsTopMode); err != nil {
		return err
	}
	top, err := plainRoot(root, session.ScreenshotsFolder)
	if err != nil {
		return err
	}
	defer func() { _ = top.Close() }()
	return groupFolder(top, sid, shotsFolderMode)
}

func (s Screenshots) Entries(box string) ([]string, error) {
	top, err := s.top(box)
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

func plainRoot(parent *os.Root, name string) (*os.Root, error) {
	seen, err := parent.Lstat(name)
	if err != nil || !seen.IsDir() || seen.Mode()&fs.ModeSymlink != 0 {
		return nil, errMissing
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, errMissing
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(seen, opened) {
		_ = root.Close()
		return nil, errMissing
	}
	return root, nil
}

func (s Screenshots) top(box string) (*os.Root, error) {
	root, err := s.box(box)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return plainRoot(root, session.ScreenshotsFolder)
}

func (s Screenshots) folder(box, sid string) (*checkout, error) {
	if !localName(sid) {
		return nil, errOutside
	}
	top, err := s.top(box)
	if err != nil {
		return nil, err
	}
	defer func() { _ = top.Close() }()
	root, err := plainRoot(top, sid)
	if err != nil {
		return nil, err
	}
	return &checkout{dir: sid, root: root}, nil
}

func (s Screenshots) List(box, sid string) ([]session.ScreenshotFile, error) {
	c, err := s.folder(box, sid)
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

func (s Screenshots) Read(box, sid, name string, limit int64) ([]byte, error) {
	if !localName(name) {
		return nil, errOutside
	}
	c, err := s.folder(box, sid)
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

func (s Screenshots) Drop(box, sid string) error {
	if !localName(sid) {
		return errOutside
	}
	top, err := s.top(box)
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

func (s Screenshots) Gone(box, name string) error {
	if !localName(name) {
		return errOutside
	}
	top, err := s.top(box)
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
		refuseFields(w)
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
		refuse(w, http.StatusBadRequest, "name", "Name a screenshot from the list. Reload it and try again.")
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
