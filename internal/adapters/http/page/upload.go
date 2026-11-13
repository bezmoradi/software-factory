package page

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const uploadRoute = "/api/upload"

var uploadID = regexp.MustCompile(`^[0-9a-f]{24}$`)

type UploadDir struct {
	dir   string
	agent string
}

var _ session.UploadFiles = UploadDir{}

func NewUploadDir(dir, agent string) (UploadDir, error) {
	if !filepath.IsAbs(dir) || !filepath.IsAbs(agent) {
		return UploadDir{}, errors.New("the uploads folder must be an absolute path")
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 || !ownedByMe(info) {
		return UploadDir{}, fmt.Errorf("the uploads folder %s must be a folder this user owns", dir)
	}
	if err := os.Chmod(dir, 0o710); err != nil {
		return UploadDir{}, fmt.Errorf("the uploads folder %s: %w", dir, err)
	}
	u := UploadDir{dir: filepath.Clean(dir), agent: filepath.Clean(agent)}
	u.clear()
	return u, nil
}

func (u UploadDir) clear() {
	root, err := os.OpenRoot(u.dir)
	if err != nil {
		return
	}
	defer func() { _ = root.Close() }()
	f, err := root.Open(".")
	if err != nil {
		return
	}
	entries, _ := f.ReadDir(-1)
	_ = f.Close()
	for _, e := range entries {
		_ = root.RemoveAll(e.Name())
	}
}

func uploadPlace(folder, id, name string) bool {
	return uploadID.MatchString(folder) && uploadID.MatchString(id) && (name == "" || session.UploadName(name))
}

func (u UploadDir) Save(folder, id, name string, data []byte) (string, error) {
	if !uploadPlace(folder, id, name) || name == "" {
		return "", errors.New("upload: bad place")
	}
	root, err := os.OpenRoot(u.dir)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	if err := root.Mkdir(folder, 0o750); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", err
	}
	for _, d := range []string{folder, path.Join(folder, id)} {
		if d != folder {
			if err := root.Mkdir(d, 0o750); err != nil {
				return "", err
			}
		}
		info, err := root.Lstat(d)
		if err != nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
			return "", errors.New("upload: not a folder")
		}
		if err := root.Chmod(d, 0o750); err != nil {
			return "", err
		}
	}
	at := path.Join(folder, id, name)
	f, err := root.OpenFile(at, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = root.RemoveAll(path.Join(folder, id))
		return "", err
	}
	if err := f.Chmod(0o640); err != nil {
		_ = f.Close()
		_ = root.RemoveAll(path.Join(folder, id))
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = root.RemoveAll(path.Join(folder, id))
		return "", err
	}
	return path.Join(u.agent, at), nil
}

func (u UploadDir) Read(folder, id, name string, limit int64) ([]byte, error) {
	if !uploadPlace(folder, id, name) || name == "" {
		return nil, errors.New("upload: bad place")
	}
	root, err := os.OpenRoot(u.dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	f, err := root.OpenFile(path.Join(folder, id, name), openFlags, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("upload: not a file")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("upload: too big")
	}
	return data, nil
}

func (u UploadDir) Drop(folder, id string) error {
	if !uploadPlace(folder, id, "") {
		return errors.New("upload: bad place")
	}
	root, err := os.OpenRoot(u.dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return root.RemoveAll(path.Join(folder, id))
}

func (u UploadDir) DropFolder(folder string) error {
	if !uploadID.MatchString(folder) {
		return errors.New("upload: bad place")
	}
	root, err := os.OpenRoot(u.dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return root.RemoveAll(folder)
}

func uploadKind(r *http.Request) (string, bool) {
	values := r.Header.Values("Content-Type")
	if len(values) != 1 {
		return "", false
	}
	kind, params, err := mime.ParseMediaType(values[0])
	if err != nil || len(params) != 0 {
		return "", false
	}
	if _, ok := session.ImageTypes[kind]; ok || kind == "application/octet-stream" {
		return kind, true
	}
	return "", false
}

func oneHeader(r *http.Request, name string) string {
	values := r.Header.Values(name)
	if len(values) != 1 {
		return ""
	}
	return values[0]
}

func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	kind, ok := uploadKind(r)
	if !ok {
		refuse(w, http.StatusUnsupportedMediaType, "type")
		return
	}
	sid := oneHeader(r, "X-SF-Session")
	name := ""
	limit := int64(session.MaxImage)
	if kind == "application/octet-stream" {
		raw := oneHeader(r, "X-SF-Name")
		decoded, err := url.PathUnescape(raw)
		if err != nil || !session.UploadName(decoded) {
			refuse(w, http.StatusBadRequest, "name")
			return
		}
		name = decoded
		limit = session.MaxDrop
	} else if len(r.Header.Values("X-SF-Name")) != 0 {
		refuse(w, http.StatusBadRequest, "name")
		return
	}
	if sid == "" || len(sid) > 80 {
		refuse(w, http.StatusBadRequest, "session")
		return
	}
	if r.ContentLength > limit {
		refuse(w, http.StatusRequestEntityTooLarge, "body")
		return
	}
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(4 * readFor))
	_ = rc.SetWriteDeadline(time.Now().Add(4*readFor + writeFor))
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			refuse(w, http.StatusRequestEntityTooLarge, "body")
			return
		}
		refuse(w, http.StatusBadRequest, "body")
		return
	}
	id, err := s.reg.Upload(sid, kind, name, data)
	s.logger.Info("upload", "file", kind == "application/octet-stream", "ok", err == nil)
	if err != nil {
		code, p := status(err)
		send(w, code, p)
		return
	}
	send(w, http.StatusOK, map[string]string{"id": id})
}

func (s *Server) image(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	id := q.Get("id")
	if len(q) != 1 || len(q["id"]) != 1 || !uploadID.MatchString(id) {
		refuse(w, http.StatusBadRequest, "id")
		return
	}
	u, data, err := s.reg.Image(connSecret(r), id)
	if err != nil {
		s.agentError(w, r, err)
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(2 * writeFor))
	send(w, http.StatusOK, map[string]string{"type": u.Type, "path": u.Path, "base64": base64.StdEncoding.EncodeToString(data)})
}

var fileRoutes = map[string]bool{"/api/file": true, "/api/file-check": true, "/api/files": true, "/api/file-match": true}

func idList(v any) ([]string, bool) {
	if v == nil {
		return nil, true
	}
	list, ok := v.([]any)
	if !ok || len(list) > session.MaxAttached {
		return nil, false
	}
	out := make([]string, 0, len(list))
	for _, x := range list {
		id, ok := x.(string)
		if !ok || !uploadID.MatchString(id) {
			return nil, false
		}
		out = append(out, id)
	}
	return out, true
}

func stopped(err error) bool {
	var e *session.Error
	return errors.As(err, &e) && e.Kind == session.Conflict && (e.Reason == "offline" || e.Reason == "not running")
}

func (s *Server) message(w http.ResponseWriter, r *http.Request, body map[string]any) {
	if !keysWithin(body, "sid", "text", "images", "files", "confirm", "approve", "costOk") {
		refuse(w, http.StatusBadRequest, "text")
		return
	}
	sid := str(body["sid"], 80)
	text, ok := body["text"].(string)
	images, okImages := idList(body["images"])
	files, okFiles := idList(body["files"])
	consent, okConsent := consentOf(body)
	if !ok || !okImages || !okFiles || !okConsent {
		refuse(w, http.StatusBadRequest, "text")
		return
	}
	confirm := body["confirm"] == true
	err := s.reg.Message(sid, text, confirm, images, files)
	waker, canWake := s.cfg.Starter.(session.MessageWaker)
	if err != nil && stopped(err) && canWake && waker.Asleep(sid) {
		if present(body["images"]) || present(body["files"]) {
			send(w, http.StatusConflict, problem{Error: "asleep", Message: "Images and files need a running session. Send a text message first, which wakes it, or choose Wake it without a message in the ⋯ menu."})
			return
		}
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(manageFor + writeFor))
		ctx, cancel := context.WithTimeout(r.Context(), manageFor)
		defer cancel()
		err = waker.WakeWithMessage(ctx, sid, text, confirm, consent)
		s.logger.Info("message", "woke", true, "ok", err == nil)
		if err == nil {
			send(w, http.StatusAccepted, map[string]bool{"ok": true, "woke": true})
			return
		}
		code, p := status(err)
		send(w, code, p)
		return
	}
	if err == nil {
		s.logger.Info("message sent", "images", len(images), "files", len(files))
	}
	reply(w, err, http.StatusAccepted)
}
