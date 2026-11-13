package files

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	fdiff "github.com/go-git/go-git/v5/plumbing/format/diff"
	"github.com/go-git/go-git/v5/utils/diff"
	"github.com/sergi/go-diff/diffmatchpatch"
)

const ChangeFile = "change.diff"

const (
	maxDiffedFile = 1 << 20
	maxChange     = 32 << 20
	maxChangeHeld = 32 << 20
	diffTimeout   = 30 * time.Second
)

var errChangeFull = errors.New("change.diff is full")

type side struct {
	mode    filemode.FileMode
	content []byte
	big     bool
	load    func() ([]byte, error)
}

func (s *side) fill() error {
	if s.load == nil {
		return nil
	}
	data, err := s.load()
	s.load = nil
	if err != nil {
		return err
	}
	if len(data) > maxDiffedFile {
		s.big = true
		return nil
	}
	s.content = data
	return nil
}

func writeChange(dir string) error {
	base, err := sides(filepath.Join(dir, "base"))
	if err != nil {
		return err
	}
	head, err := sides(filepath.Join(dir, "head"))
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, ChangeFile), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, err = encodeChange(f, base, head, changeLimits{out: maxChange, held: maxChangeHeld, note: "\n[Software Factory: the diff %s and stops here. Read base/ and head/ for the rest.]\n"})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

type changeLimits struct {
	out  int
	held int
	note string
}

func encodeChange(out io.Writer, base, head map[string]side, limits changeLimits) (bool, error) {
	paths := make([]string, 0, len(base)+len(head))
	for p := range base {
		paths = append(paths, p)
	}
	for p := range head {
		if _, ok := base[p]; !ok {
			paths = append(paths, p)
		}
	}
	slices.Sort(paths)
	var patches []fdiff.FilePatch
	held := 0
	full := false
	for _, p := range paths {
		from, inBase := base[p]
		to, inHead := head[p]
		if err := from.fill(); err != nil {
			return false, err
		}
		if err := to.fill(); err != nil {
			return false, err
		}
		if inBase && inHead && from.mode == to.mode && !from.big && !to.big && bytes.Equal(from.content, to.content) {
			continue
		}
		held += len(from.content) + len(to.content)
		if held > limits.held {
			full = true
			break
		}
		fp := &filePatch{}
		if inBase {
			fp.from = &diffFile{path: p, side: from}
		}
		if inHead {
			fp.to = &diffFile{path: p, side: to}
		}
		fp.binary = (inBase && (from.big || isBinary(from.content))) || (inHead && (to.big || isBinary(to.content)))
		if !fp.binary {
			fp.chunks = chunks(string(from.content), string(to.content))
		}
		patches = append(patches, fp)
	}
	w := &cappedWriter{w: out, left: limits.out}
	err := fdiff.NewUnifiedEncoder(w, fdiff.DefaultContextLines).Encode(&patch{files: patches})
	reason := ""
	switch {
	case errors.Is(err, errChangeFull):
		reason = fmt.Sprintf("is longer than %d MiB", limits.out>>20)
	case err == nil && full:
		reason = fmt.Sprintf("covers changed files holding more than %d MiB", limits.held>>20)
	default:
		return false, err
	}
	w.left = 1 << 10
	_, err = fmt.Fprintf(w, limits.note, reason)
	return true, err
}

func sides(root string) (map[string]side, error) {
	out := map[string]side{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		s := side{mode: filemode.Regular}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			s.mode = filemode.Symlink
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			s.content = []byte(target)
		case !info.Mode().IsRegular():
			return nil
		default:
			if info.Mode()&0o111 != 0 {
				s.mode = filemode.Executable
			}
			if info.Size() > maxDiffedFile {
				s.big = true
				break
			}
			s.load = func() ([]byte, error) { return os.ReadFile(p) }
		}
		out[filepath.ToSlash(rel)] = s
		return nil
	})
	return out, err
}

func isBinary(b []byte) bool {
	return bytes.IndexByte(b[:min(len(b), 8000)], 0) >= 0
}

func chunks(from, to string) []fdiff.Chunk {
	var out []fdiff.Chunk
	for _, d := range diff.DoWithTimeout(from, to, diffTimeout) {
		op := fdiff.Equal
		switch d.Type {
		case diffmatchpatch.DiffInsert:
			op = fdiff.Add
		case diffmatchpatch.DiffDelete:
			op = fdiff.Delete
		}
		out = append(out, chunk{content: d.Text, op: op})
	}
	return out
}

type patch struct{ files []fdiff.FilePatch }

func (p *patch) FilePatches() []fdiff.FilePatch { return p.files }
func (p *patch) Message() string                { return "" }

type filePatch struct {
	from, to *diffFile
	binary   bool
	chunks   []fdiff.Chunk
}

func (f *filePatch) IsBinary() bool        { return f.binary }
func (f *filePatch) Chunks() []fdiff.Chunk { return f.chunks }
func (f *filePatch) Files() (fdiff.File, fdiff.File) {
	var from, to fdiff.File
	if f.from != nil {
		from = f.from
	}
	if f.to != nil {
		to = f.to
	}
	return from, to
}

type diffFile struct {
	path string
	side side
}

func (f *diffFile) Hash() plumbing.Hash {
	if f.side.big {
		return plumbing.ZeroHash
	}
	return plumbing.ComputeHash(plumbing.BlobObject, f.side.content)
}
func (f *diffFile) Mode() filemode.FileMode { return f.side.mode }
func (f *diffFile) Path() string            { return f.path }

type chunk struct {
	content string
	op      fdiff.Operation
}

func (c chunk) Content() string       { return c.content }
func (c chunk) Type() fdiff.Operation { return c.op }

type cappedWriter struct {
	w    io.Writer
	left int
}

func (c *cappedWriter) Write(p []byte) (int, error) {
	if len(p) > c.left {
		n, _ := c.w.Write(p[:c.left])
		c.left = 0
		return n, errChangeFull
	}
	n, err := c.w.Write(p)
	c.left -= n
	return n, err
}
