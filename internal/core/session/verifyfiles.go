package session

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	MaxVerifyFiles     = 20
	MaxVerifyTrace     = 50 << 20
	MaxVerifyRunFiles  = 100 << 20
	MaxVerifyOut       = 128 << 20
	VerifyFilesKept    = 3
	MaxVerifyFilesAll  = 1 << 30
	VerifyFileImage    = "image"
	VerifyFileTrace    = "trace"
	verifyCollectFor   = 3 * time.Minute
	verifyDroppedShown = 10
)

var (
	verifyFileName  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,119}\.(png|jpe?g|gif|zip)$`)
	verifyShownName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,120}$`)
	zipStarts       = [][]byte{[]byte("PK\x03\x04"), []byte("PK\x05\x06")}
)

type VerifyAttachment struct {
	Name   string
	Kind   string
	Bytes  int64
	SHA256 string
	Width  int
	Height int
}

type VerifyFiles interface {
	Put(run, name string, r io.Reader) (int64, string, error)
	Open(run, name string) (io.ReadCloser, error)
	Remove(run string) error
	Runs() ([]string, error)
}

func ValidVerifyFile(name string) bool {
	return verifyFileName.MatchString(name)
}

type outDrops struct {
	named []string
	more  int
}

func (d *outDrops) add(name, why string) {
	if len(d.named) >= verifyDroppedShown {
		d.more++
		return
	}
	if !verifyShownName.MatchString(name) {
		name = "a file whose name the engine doesn't show"
	}
	d.named = append(d.named, name+" ("+why+")")
}

func (d *outDrops) text(tail string) string {
	var parts []string
	if len(d.named) > 0 {
		s := "Dropped from /out: " + strings.Join(d.named, ", ")
		if d.more > 0 {
			s += ", and " + strconv.Itoa(d.more) + " more"
		}
		parts = append(parts, s+".")
	}
	if tail != "" {
		parts = append(parts, tail)
	}
	out := strings.Join(parts, " ")
	if secretText(out) {
		return "Some files in /out were dropped, and the engine doesn't show their names, since they held a token-shaped string."
	}
	return clip(out, maxVerifyReason*2)
}

func (v *Verifier) collect(ctx context.Context, run string) ([]VerifyAttachment, string) {
	if v.cfg.Files == nil {
		return nil, ""
	}
	cctx, cancel := context.WithTimeout(ctx, verifyCollectFor)
	defer cancel()
	body, err := v.cfg.Host.VerifierOut(cctx, run)
	if err != nil {
		return nil, "The engine couldn't read the run's /out folder: " + cleanReason(errText(err))
	}
	defer func() { _ = body.Close() }()
	return v.attach(run, io.LimitReader(body, MaxVerifyOut))
}

func (v *Verifier) attach(run string, r io.Reader) ([]VerifyAttachment, string) {
	tr := tar.NewReader(r)
	var out []VerifyAttachment
	var drops outDrops
	folders := map[string]bool{}
	seen := map[string]bool{}
	var total int64
	tail := ""
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			tail = "The rest of /out wasn't read: it is past the 128 MiB the engine reads, or it isn't a folder Docker sent whole."
			break
		}
		name := strings.TrimPrefix(h.Name, "./")
		if name == "out" || name == "out/" {
			continue
		}
		rest, inOut := strings.CutPrefix(name, "out/")
		base := strings.TrimSuffix(rest, "/")
		if !inOut || base == "" {
			drops.add(name, "outside /out")
			continue
		}
		if top, _, nested := strings.Cut(base, "/"); nested {
			if !folders[top] {
				folders[top] = true
				drops.add(top, "a folder")
			}
			continue
		}
		switch h.Typeflag {
		case tar.TypeReg:
		case tar.TypeDir:
			if !folders[base] {
				folders[base] = true
				drops.add(base, "a folder")
			}
			continue
		case tar.TypeSymlink, tar.TypeLink:
			drops.add(base, "a link")
			continue
		default:
			drops.add(base, "not a regular file")
			continue
		}
		switch {
		case !verifyFileName.MatchString(base):
			drops.add(base, "not a .png, .jpg, .jpeg, .gif or .zip with a plain name")
			continue
		case seen[base]:
			drops.add(base, "a second copy")
			continue
		case len(out) >= MaxVerifyFiles:
			drops.add(base, "past the first 20 files")
			continue
		}
		seen[base] = true
		var a VerifyAttachment
		var why string
		if strings.HasSuffix(base, ".zip") {
			a, why = v.keepTrace(run, base, h.Size, total, tr)
		} else {
			a, why = v.keepImage(run, base, h.Size, total, tr)
		}
		if why != "" {
			drops.add(base, why)
			continue
		}
		total += a.Bytes
		out = append(out, a)
	}
	return out, drops.text(tail)
}

func (v *Verifier) keepImage(run, name string, size, total int64, r io.Reader) (VerifyAttachment, string) {
	if size > MaxScreenshot {
		return VerifyAttachment{}, "over 10 MiB"
	}
	data, err := io.ReadAll(io.LimitReader(r, MaxScreenshot+1))
	if err != nil || int64(len(data)) > MaxScreenshot {
		return VerifyAttachment{}, "unreadable or over 10 MiB"
	}
	w, h, png, bad := reencoded(data)
	if bad != nil {
		return VerifyAttachment{}, "not an image the engine shows"
	}
	if total+int64(len(png)) > MaxVerifyRunFiles {
		return VerifyAttachment{}, "past the 100 MiB a run keeps"
	}
	n, sum, err := v.cfg.Files.Put(run, name, bytes.NewReader(png))
	if err != nil {
		return VerifyAttachment{}, "the engine couldn't save it"
	}
	return VerifyAttachment{Name: name, Kind: VerifyFileImage, Bytes: n, SHA256: sum, Width: w, Height: h}, ""
}

func (v *Verifier) keepTrace(run, name string, size, total int64, r io.Reader) (VerifyAttachment, string) {
	switch {
	case size > MaxVerifyTrace:
		return VerifyAttachment{}, "over 50 MiB"
	case total+size > MaxVerifyRunFiles:
		return VerifyAttachment{}, "past the 100 MiB a run keeps"
	}
	start := make([]byte, 4)
	if _, err := io.ReadFull(r, start); err != nil || !slices.ContainsFunc(zipStarts, func(z []byte) bool { return bytes.Equal(z, start) }) {
		return VerifyAttachment{}, "not a zip file"
	}
	n, sum, err := v.cfg.Files.Put(run, name, io.LimitReader(io.MultiReader(bytes.NewReader(start), r), MaxVerifyTrace))
	if err != nil {
		return VerifyAttachment{}, "the engine couldn't save it"
	}
	return VerifyAttachment{Name: name, Kind: VerifyFileTrace, Bytes: n, SHA256: sum}, ""
}

func attachedBytes(list []VerifyAttachment) int64 {
	var n int64
	for _, a := range list {
		n += a.Bytes
	}
	return n
}

func (v *Verifier) retain() {
	v.mu.Lock()
	var gone []string
	kept := map[ProjectID]int{}
	var total int64
	for i := len(v.runs) - 1; i >= 0; i-- {
		r := &v.runs[i]
		if len(r.Attached) == 0 {
			continue
		}
		size := attachedBytes(r.Attached)
		if kept[r.Project] >= VerifyFilesKept || total+size > MaxVerifyFilesAll {
			gone = append(gone, r.ID)
			r.Attached = nil
			r.AttachNote = strings.TrimSpace(r.AttachNote + " Its files were removed: the engine keeps those of the latest 3 runs with files per project, 1 GiB in all.")
			continue
		}
		kept[r.Project]++
		total += size
	}
	v.mu.Unlock()
	v.dropFiles(gone)
}

func (v *Verifier) dropFiles(runs []string) {
	if v.cfg.Files == nil {
		return
	}
	for _, run := range runs {
		if err := v.cfg.Files.Remove(run); err != nil {
			v.cfg.Logger.Warn("verify", "event", "attachments-not-removed", "run", run)
		}
	}
}

func (v *Verifier) pruneFiles() {
	if v.cfg.Files == nil {
		return
	}
	runs, err := v.cfg.Files.Runs()
	if err != nil {
		v.cfg.Logger.Warn("verify", "event", "attachments-not-listed")
		return
	}
	var gone []string
	v.mu.Lock()
	for _, run := range runs {
		if _, live := v.live[run]; live {
			continue
		}
		if !slices.ContainsFunc(v.runs, func(r VerifyRun) bool { return r.ID == run && len(r.Attached) > 0 }) {
			gone = append(gone, run)
		}
	}
	v.mu.Unlock()
	v.dropFiles(gone)
}

func (v *Verifier) Attachment(_ context.Context, sid, id, name string) (VerifyAttachment, io.ReadCloser, error) {
	if !ValidBox(id) {
		return VerifyAttachment{}, nil, explained(invalid("id"), "A verification run's id is 16 hexadecimal digits.")
	}
	if !ValidVerifyFile(name) {
		return VerifyAttachment{}, nil, explained(invalid("name"), "A browser run's file is a .png, .jpg, .jpeg, .gif or .zip with a plain name of at most 124 bytes.")
	}
	if _, ok := v.targetOf(sid); !ok {
		return VerifyAttachment{}, nil, explained(conflict("not started here"), "Only a session this engine started has verification runs here.")
	}
	v.mu.Lock()
	var found *VerifyAttachment
	for _, r := range v.runs {
		if r.ID != id || r.SID != sid {
			continue
		}
		for _, a := range r.Attached {
			if a.Name == name {
				found = &a
			}
		}
	}
	v.mu.Unlock()
	if found == nil || v.cfg.Files == nil {
		return VerifyAttachment{}, nil, explained(notFound("attachment"), "That verification run kept no file of that name.")
	}
	rc, err := v.cfg.Files.Open(id, name)
	if err != nil {
		return VerifyAttachment{}, nil, explained(conflict("files"), "The engine couldn't read that file.")
	}
	return *found, rc, nil
}
