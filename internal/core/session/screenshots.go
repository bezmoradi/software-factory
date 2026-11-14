package session

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	ScreenshotsFolder    = "screenshots"
	ScreenshotsVariable  = "SF_SCREENSHOTS"
	MaxScreenshot        = 10 << 20
	MaxScreenshotSide    = 8192
	MaxScreenshotsShown  = 50
	maxScreenshotDecoded = 256 << 20
	maxScreenshotPNG     = 24 << 20
)

var (
	ErrScreenshotTooBig = errors.New("screenshot too big")
	screenshotName      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._ -]{0,119}\.(?i:png|jpe?g|gif)$`)
)

type ScreenshotFile struct {
	Name string
	Size int64
	At   time.Time
}

type ScreenshotFiles interface {
	List(sid string) ([]ScreenshotFile, error)
	Read(sid, name string, limit int64) ([]byte, error)
	Drop(sid string) error
	Make(sid string) error
	Entries() ([]string, error)
}

type Screenshot struct {
	Name   string
	Width  int
	Height int
	PNG    []byte
}

type screenshotBook struct {
	files    ScreenshotFiles
	logger   shared.Logger
	decoding sync.Mutex
}

func ScreenshotName(name string) bool {
	return screenshotName.MatchString(name)
}

func ScreenshotFolder(workspace, sid string) string {
	if workspace == "" || !sidPattern.MatchString(sid) {
		return ""
	}
	return path.Join(workspace, ScreenshotsFolder, sid) + "/"
}

func (r *Registry) UseScreenshots(files ScreenshotFiles, logger shared.Logger) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.shots.files = files
	r.shots.logger = logger
}

func (r *Registry) shotFiles() (ScreenshotFiles, shared.Logger) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.shots.files, r.shots.logger
}

func (r *Registry) makeScreenshots(sid string) {
	files, logger := r.shotFiles()
	if files == nil || !sidPattern.MatchString(sid) {
		return
	}
	if err := files.Make(sid); err != nil && logger != nil {
		logger.Warn("screenshots", "event", "folder-not-made", "sid", sid)
	}
}

func removeScreenshots(files ScreenshotFiles, logger shared.Logger, sid string) bool {
	err := files.Drop(sid)
	if err != nil && logger != nil {
		logger.Warn("screenshots", "event", "not-removed", "sid", sid)
	}
	return err == nil
}

func (r *Registry) hasAdmitted(sid string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.admitted[sid] != nil
}

func (d *Dispatcher) hasSession(sid string) bool {
	return d.hasRun(sid) || d.reg.hasAdmitted(sid)
}

func (d *Dispatcher) DropStrayScreenshots(ctx context.Context, files ScreenshotFiles) int {
	_, logger := d.reg.shotFiles()
	if files == nil {
		return 0
	}
	names, err := files.Entries()
	if err != nil {
		if logger != nil {
			logger.Warn("screenshots", "event", "unlisted")
		}
		return 0
	}
	dropped, failed := 0, 0
	for i, name := range names {
		if ctx.Err() != nil {
			if logger != nil {
				logger.Warn("screenshots", "event", "sweep-stopped", "entries", len(names)-i)
			}
			break
		}
		switch {
		case !sidPattern.MatchString(name):
			if files.Drop(name) == nil {
				dropped++
			} else {
				failed++
			}
		case !d.hasSession(name) && removeScreenshots(files, logger, name):
			dropped++
		}
	}
	if failed > 0 && logger != nil {
		logger.Warn("screenshots", "event", "strays-not-removed", "entries", failed)
	}
	return dropped
}

func (r *Registry) screenshotFiles(sid string) (ScreenshotFiles, error) {
	if !sidPattern.MatchString(sid) {
		return nil, invalid("sid")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.shots.files == nil {
		return nil, explained(conflict("no screenshots"), "This engine has no workspace folder, so it shows no screenshots.")
	}
	if r.admitted[sid] == nil {
		return nil, explained(notFound("session"), "Only a session this engine started shows screenshots.")
	}
	return r.shots.files, nil
}

func (r *Registry) Screenshots(sid string) ([]ScreenshotFile, error) {
	files, err := r.screenshotFiles(sid)
	if err != nil {
		return nil, err
	}
	list, err := files.List(sid)
	if err != nil {
		return []ScreenshotFile{}, nil
	}
	out := make([]ScreenshotFile, 0, len(list))
	for _, f := range list {
		if ScreenshotName(f.Name) && f.Size >= 0 {
			out = append(out, f)
		}
	}
	slices.SortFunc(out, func(a, b ScreenshotFile) int {
		if c := b.At.Compare(a.At); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	if len(out) > MaxScreenshotsShown {
		out = out[:MaxScreenshotsShown]
	}
	return out, nil
}

func (r *Registry) Screenshot(sid, name string) (Screenshot, error) {
	files, err := r.screenshotFiles(sid)
	if err != nil {
		return Screenshot{}, err
	}
	if !ScreenshotName(name) {
		return Screenshot{}, explained(invalid("name"), "A screenshot is a .png, .jpg, .jpeg or .gif file directly in the session's screenshots folder.")
	}
	if !r.shots.decoding.TryLock() {
		return Screenshot{}, explained(limited("busy"), "Another screenshot is being read. Try again in a moment.")
	}
	defer r.shots.decoding.Unlock()
	data, err := files.Read(sid, name, MaxScreenshot)
	if errors.Is(err, ErrScreenshotTooBig) {
		return Screenshot{}, explained(invalid("too big"), "A screenshot may be at most 10 MiB.")
	}
	if err != nil {
		return Screenshot{}, explained(notFound("screenshot"), "That screenshot isn't in the session's screenshots folder.")
	}
	if len(data) > MaxScreenshot {
		return Screenshot{}, explained(invalid("too big"), "A screenshot may be at most 10 MiB.")
	}
	w, h, out, bad := reencoded(data)
	if bad != nil {
		return Screenshot{}, bad
	}
	return Screenshot{Name: name, Width: w, Height: h, PNG: out}, nil
}

func (r *Registry) dropScreenshots(sid string) {
	files, logger := r.shots.files, r.shots.logger
	if files == nil || !sidPattern.MatchString(sid) {
		return
	}
	go func() { _ = removeScreenshots(files, logger, sid) }()
}

func screenshotKind(data []byte) string {
	for _, kind := range []string{"image/png", "image/jpeg", "image/gif"} {
		if imageMatches(kind, data) {
			return kind
		}
	}
	return ""
}

func notImage() *Error {
	return explained(invalid("not image"), "That file isn't a PNG, JPEG or GIF image the engine can read.")
}

func decodedBytes(kind string, cfg image.Config) int64 {
	per := int64(4)
	switch {
	case kind == "image/jpeg":
		per = 16
	case kind == "image/png" && (cfg.ColorModel == color.RGBA64Model || cfg.ColorModel == color.NRGBA64Model || cfg.ColorModel == color.Gray16Model):
		per = 8
	}
	return int64(cfg.Width) * int64(cfg.Height) * per
}

func reencoded(data []byte) (int, int, []byte, *Error) {
	kind := screenshotKind(data)
	var cfg image.Config
	var err error
	switch kind {
	case "image/png":
		cfg, err = png.DecodeConfig(bytes.NewReader(data))
	case "image/jpeg":
		cfg, err = jpeg.DecodeConfig(bytes.NewReader(data))
	case "image/gif":
		cfg, err = gif.DecodeConfig(bytes.NewReader(data))
	default:
		return 0, 0, nil, notImage()
	}
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return 0, 0, nil, notImage()
	}
	if cfg.Width > MaxScreenshotSide || cfg.Height > MaxScreenshotSide {
		return 0, 0, nil, explained(invalid("too large"), "A screenshot may be at most 8,192 pixels wide and 8,192 pixels high.")
	}
	if decodedBytes(kind, cfg) > maxScreenshotDecoded {
		return 0, 0, nil, explained(invalid("too large"), "That screenshot would take more than 256 MiB to decode. Save a smaller one, or save it as a PNG with 8 bits per channel.")
	}
	var img image.Image
	switch kind {
	case "image/png":
		img, err = png.Decode(bytes.NewReader(data))
	case "image/jpeg":
		img, err = jpeg.Decode(bytes.NewReader(data))
	case "image/gif":
		img, err = gif.Decode(bytes.NewReader(data))
	}
	if err != nil || img == nil {
		return 0, 0, nil, notImage()
	}
	b := img.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 || b.Dx() > cfg.Width || b.Dy() > cfg.Height {
		return 0, 0, nil, notImage()
	}
	var out bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.DefaultCompression}).Encode(&out, img); err != nil {
		return 0, 0, nil, notImage()
	}
	if out.Len() > maxScreenshotPNG {
		return 0, 0, nil, explained(invalid("too large"), "That screenshot is more than 24 MiB as a PNG. Save a smaller one.")
	}
	return b.Dx(), b.Dy(), out.Bytes(), nil
}
