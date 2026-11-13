package session

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxImage       = 5 << 19
	MaxDrop        = 10 << 20
	MaxUnsent      = 40
	MaxAttached    = 4
	MaxUploadBytes = 256 << 20
	UnsentKeep     = time.Hour
	EndedKeep      = time.Minute
	maxUploadName  = 255
)

var ImageTypes = map[string]string{"image/png": "png", "image/jpeg": "jpg", "image/gif": "gif", "image/webp": "webp"}

type UploadFiles interface {
	Save(folder, id, name string, data []byte) (string, error)
	Read(folder, id, name string, limit int64) ([]byte, error)
	Drop(folder, id string) error
	DropFolder(folder string) error
}

type Upload struct {
	ID    string
	SID   string
	Image bool
	Type  string
	Name  string
	Path  string
	Size  int64
	At    time.Time
	Sent  bool
}

type Attachment struct {
	Image bool
	Type  string
	Name  string
	Path  string
	Data  []byte
}

type AttachingSession interface {
	TakesImages() bool
	PromptWith(ctx context.Context, text string, parts []Attachment) error
}

type uploadBook struct {
	files   UploadFiles
	items   map[string]*Upload
	folders map[string]string
	ended   map[string]time.Time
	bytes   int64
	noImage map[string]bool
}

func newUploadBook() *uploadBook {
	return &uploadBook{items: map[string]*Upload{}, folders: map[string]string{}, ended: map[string]time.Time{}, noImage: map[string]bool{}}
}

func (r *Registry) UseUploads(files UploadFiles) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.uploads.files = files
}

func (r *Registry) Uploads() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.uploads.files != nil
}

func imageMatches(kind string, data []byte) bool {
	switch kind {
	case "image/png":
		return bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n"))
	case "image/jpeg":
		return bytes.HasPrefix(data, []byte("\xff\xd8\xff"))
	case "image/gif":
		return bytes.HasPrefix(data, []byte("GIF87a")) || bytes.HasPrefix(data, []byte("GIF89a"))
	case "image/webp":
		return len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP"
	}
	return false
}

func UploadName(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > maxUploadName || !utf8.ValidString(name) {
		return false
	}
	for _, c := range name {
		if c < 0x20 || c == 0x7f || c == '/' || c == '\\' || (c >= 0x80 && c <= 0x9f) || c == 0x2028 || c == 0x2029 {
			return false
		}
	}
	return true
}

func (r *Registry) Upload(sid, kind, name string, data []byte) (string, error) {
	image := kind != "application/octet-stream"
	if image {
		ext, ok := ImageTypes[kind]
		if !ok {
			return "", invalid("upload")
		}
		name = "image." + ext
		if len(data) > MaxImage {
			return "", explained(invalid("too big"), "An image may be at most 2.5 MB.")
		}
		if !imageMatches(kind, data) {
			return "", explained(invalid("upload"), "That isn't the kind of image it says it is.")
		}
	} else {
		if !UploadName(name) {
			return "", explained(invalid("name"), "That file's name can't be used.")
		}
		if len(data) > MaxDrop {
			return "", explained(invalid("too big"), "A file may be at most 10 MB.")
		}
	}
	if len(data) == 0 {
		return "", explained(invalid("upload"), "The file is empty.")
	}
	r.mu.Lock()
	files := r.uploads.files
	a := r.admitted[sid]
	switch {
	case files == nil:
		r.mu.Unlock()
		return "", explained(conflict("no uploads"), "This engine has no uploads folder, so it takes no images or files.")
	case a == nil:
		r.mu.Unlock()
		return "", explained(notFound("session"), "Only a session this engine started takes images and files.")
	case !a.running:
		r.mu.Unlock()
		return "", explained(conflict("not running"), "Images and files need a running session. Wake it first.")
	case image && r.uploads.noImage[sid]:
		r.mu.Unlock()
		return "", explained(conflict("no images"), noImages)
	}
	r.trimUnsent(sid)
	if r.uploads.bytes+int64(len(data)) > MaxUploadBytes {
		r.mu.Unlock()
		return "", explained(limited("uploads"), "The uploads folder is full. Send or remove what's waiting, or wait for older uploads to be deleted.")
	}
	folder := r.uploads.folders[sid]
	if folder == "" {
		folder = randomHex(12)
		r.uploads.folders[sid] = folder
	}
	id := randomHex(12)
	u := &Upload{ID: id, SID: sid, Image: image, Type: kind, Name: name, Size: int64(len(data)), At: r.now()}
	r.uploads.items[id] = u
	r.uploads.bytes += u.Size
	r.mu.Unlock()

	path, err := files.Save(folder, id, name, data)

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.uploads.items[id] != u {
		go func() { _ = files.Drop(folder, id) }()
		return "", explained(conflict("gone"), "The session ended while the file was saved.")
	}
	if err != nil {
		r.dropUpload(id)
		return "", explained(conflict("save"), "The engine couldn't save the file.")
	}
	u.Path = path
	return id, nil
}

const noImages = "This agent doesn't take images in its messages (it doesn't offer ACP's image prompt capability), so send the text alone, or with files: the agent gets those as paths it can read."

func (r *Registry) trimUnsent(sid string) {
	var unsent []*Upload
	for _, u := range r.uploads.items {
		if !u.Sent {
			unsent = append(unsent, u)
		}
	}
	for len(unsent) >= MaxUnsent {
		oldest := 0
		for i, u := range unsent {
			if u.At.Before(unsent[oldest].At) {
				oldest = i
			}
		}
		r.dropUpload(unsent[oldest].ID)
		unsent = append(unsent[:oldest], unsent[oldest+1:]...)
	}
}

func (r *Registry) dropUpload(id string) {
	u := r.uploads.items[id]
	if u == nil {
		return
	}
	delete(r.uploads.items, id)
	r.uploads.bytes -= u.Size
	folder, files := r.uploads.folders[u.SID], r.uploads.files
	if folder != "" && files != nil && u.Path != "" {
		go func() { _ = files.Drop(folder, id) }()
	}
}

func (r *Registry) dropUploads(sid string) {
	for id, u := range r.uploads.items {
		if u.SID == sid {
			delete(r.uploads.items, id)
			r.uploads.bytes -= u.Size
		}
	}
	folder, files := r.uploads.folders[sid], r.uploads.files
	delete(r.uploads.folders, sid)
	delete(r.uploads.ended, sid)
	if folder != "" && files != nil {
		go func() { _ = files.DropFolder(folder) }()
	}
}

func (r *Registry) sweepUploads(now time.Time) {
	for id, u := range r.uploads.items {
		if !u.Sent && now.Sub(u.At) > UnsentKeep {
			r.dropUpload(id)
		}
	}
	for sid := range r.uploads.folders {
		a := r.admitted[sid]
		if a == nil {
			r.dropUploads(sid)
			continue
		}
		if a.running || r.queued[sid] != nil {
			delete(r.uploads.ended, sid)
			continue
		}
		at, ok := r.uploads.ended[sid]
		if !ok {
			r.uploads.ended[sid] = now
			continue
		}
		if now.Sub(at) > EndedKeep {
			r.dropUploads(sid)
		}
	}
}

func (r *Registry) takeUploads(sid string, ids []string, image bool) ([]Upload, error) {
	if len(ids) > MaxAttached {
		return nil, invalid("attachments")
	}
	seen := map[string]bool{}
	out := make([]Upload, 0, len(ids))
	for _, id := range ids {
		u := r.uploads.items[id]
		if seen[id] || u == nil || u.SID != sid || u.Sent || u.Image != image || u.Path == "" {
			if image {
				return nil, explained(invalid("images"), "An image is no longer waiting to be sent. Attach it again.")
			}
			return nil, explained(invalid("files"), "A file is no longer waiting to be sent. Attach it again.")
		}
		seen[id] = true
		out = append(out, *u)
	}
	return out, nil
}

func (r *Registry) markSent(list []Upload) {
	for _, u := range list {
		if k := r.uploads.items[u.ID]; k != nil {
			k.Sent = true
		}
	}
}

func (r *Registry) setImages(sid string, takes bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if takes {
		delete(r.uploads.noImage, sid)
	} else {
		r.uploads.noImage[sid] = true
	}
}

func (r *Registry) Image(given, id string) (Upload, []byte, error) {
	r.mu.Lock()
	c := r.connOf(given, r.now())
	if c == nil {
		r.mu.Unlock()
		return Upload{}, nil, unauthorized()
	}
	u := r.uploads.items[id]
	if u == nil || u.SID != c.sid || !u.Image || !u.Sent {
		r.mu.Unlock()
		return Upload{}, nil, notFound("image")
	}
	got, folder, files := *u, r.uploads.folders[u.SID], r.uploads.files
	r.mu.Unlock()
	if files == nil || folder == "" {
		return Upload{}, nil, notFound("image")
	}
	data, err := files.Read(folder, got.ID, got.Name, MaxImage)
	if err != nil {
		return Upload{}, nil, notFound("image")
	}
	return got, data, nil
}

func (r *Registry) attachments(sid string, msg Fields) ([]Attachment, bool) {
	var out []Attachment
	r.mu.Lock()
	folder, files := r.uploads.folders[sid], r.uploads.files
	r.mu.Unlock()
	if list, ok := msg["images"].([]Fields); ok {
		for _, m := range list {
			id, _ := m["id"].(string)
			kind, _ := m["type"].(string)
			path, _ := m["path"].(string)
			name := "image." + ImageTypes[kind]
			if files == nil || folder == "" {
				return nil, false
			}
			data, err := files.Read(folder, id, name, MaxImage)
			if err != nil {
				return nil, false
			}
			out = append(out, Attachment{Image: true, Type: kind, Name: name, Path: path, Data: data})
		}
	}
	if list, ok := msg["files"].([]Fields); ok {
		for _, m := range list {
			name, _ := m["name"].(string)
			path, _ := m["path"].(string)
			out = append(out, Attachment{Name: name, Path: path})
		}
	}
	return out, true
}

func uploadFields(images, files []Upload) (Fields, Fields) {
	var ims, fs []Fields
	for _, u := range images {
		ims = append(ims, Fields{"id": u.ID, "type": u.Type, "path": u.Path})
	}
	for _, u := range files {
		fs = append(fs, Fields{"name": u.Name, "path": u.Path})
	}
	out := Fields{}
	shown := Fields{}
	if len(ims) > 0 {
		out["images"] = ims
		shown["images"] = float64(len(ims))
	}
	if len(fs) > 0 {
		out["files"] = fs
		names := make([]any, 0, len(fs))
		for _, f := range files {
			names = append(names, f.Name)
		}
		shown["files"] = names
	}
	return out, shown
}

func AttachedNote(parts []Attachment, inline bool) string {
	var images, files []Attachment
	for _, p := range parts {
		if p.Image {
			images = append(images, p)
		} else {
			files = append(files, p)
		}
	}
	var notes []string
	if len(images) > 0 {
		where := make([]string, 0, len(images))
		for _, p := range images {
			where = append(where, p.Path)
		}
		what := "an image"
		if len(images) > 1 {
			what = strconv.Itoa(len(images)) + " images"
		}
		if inline {
			notes = append(notes, "I pasted "+what+" on the Software Factory page; "+pronoun(len(images), "it is", "they are")+" in this message, and also saved at "+strings.Join(where, ", ")+".")
		} else {
			notes = append(notes, "I pasted "+what+" on the Software Factory page, saved at "+strings.Join(where, ", ")+". Open "+pronoun(len(images), "it", "them")+" there.")
		}
	}
	if len(files) > 0 {
		where := make([]string, 0, len(files))
		for _, p := range files {
			where = append(where, p.Name+" at "+p.Path)
		}
		what := "a file"
		if len(files) > 1 {
			what = strconv.Itoa(len(files)) + " files"
		}
		notes = append(notes, "I dropped "+what+" on the Software Factory page that "+pronoun(len(files), "isn't", "aren't")+" in this project, so the page saved a read-only copy: "+strings.Join(where, ", ")+". Read "+pronoun(len(files), "it", "them")+" there. The copies are deleted when this session ends.")
	}
	return strings.Join(notes, "\n\n")
}

func pronoun(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func (r *Registry) Worktree(sid string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.admitted[sid]
	if a == nil || a.Cwd == "" {
		return "", false
	}
	return a.Cwd, true
}
