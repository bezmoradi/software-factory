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
	"strings"
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
	maxScanned    = 64 << 20
	diffTimeout   = 30 * time.Second
)

var errChangeFull = errors.New("change.diff is full")

type side struct {
	mode    filemode.FileMode
	content []byte
	big     bool
	limit   int
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
	limit := s.limit
	if limit == 0 {
		limit = maxDiffedFile
	}
	if len(data) > limit {
		s.big = true
		return nil
	}
	s.content = data
	return nil
}

type changeSides func(limit int) (base, head map[string]side, err error)

func folderSides(dir string) changeSides {
	return func(limit int) (map[string]side, map[string]side, error) {
		base, err := sides(filepath.Join(dir, "base"), limit)
		if err != nil {
			return nil, nil, err
		}
		head, err := sides(filepath.Join(dir, "head"), limit)
		if err != nil {
			return nil, nil, err
		}
		return base, head, nil
	}
}

func treeSides(r *repo, base, head string) (changeSides, error) {
	hc, err := r.commit(head)
	if err != nil {
		return nil, err
	}
	left := ""
	if base != "" {
		bc, err := r.commit(base)
		if err != nil {
			return nil, err
		}
		left = bc.Tree
	}
	d := differ{r: r}
	if err := d.trees(left, hc.Tree, "", 0); err != nil {
		return nil, err
	}
	return func(limit int) (map[string]side, map[string]side, error) {
		from, to := map[string]side{}, map[string]side{}
		for _, c := range d.out {
			if c.OldSHA != "" {
				s := blobSide(r, c.OldMode, c.OldSHA)
				s.limit = limit
				from[c.Path] = s
			}
			if c.NewSHA != "" {
				s := blobSide(r, c.NewMode, c.NewSHA)
				s.limit = limit
				to[c.Path] = s
			}
		}
		return from, to, nil
	}, nil
}

func changeFiles(dir string, from changeSides) (string, bool, error) {
	if err := writeChange(dir, from); err != nil {
		return "", false, fmt.Errorf("the review's change.diff can't be written: %s", cause(err))
	}
	added, tooLarge, err := wholeChange(from)
	if err != nil {
		return "", false, fmt.Errorf("the review's change can't be read in full: %s", cause(err))
	}
	return added, tooLarge, nil
}

func writeChange(dir string, from changeSides) error {
	base, head, err := from(maxDiffedFile)
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

func wholeChange(from changeSides) (string, bool, error) {
	base, head, err := from(maxScanned)
	if err != nil {
		return "", false, err
	}
	var b strings.Builder
	cut, err := encodeChange(&b, base, head, changeLimits{out: 2 * maxScanned, held: maxScanned, note: "\n[cut: %s]\n", text: true})
	if err != nil {
		return "", false, err
	}
	return b.String(), cut, nil
}

type changeLimits struct {
	out  int
	held int
	note string
	text bool
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
		if limits.text && (from.big || to.big) {
			full = true
			break
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
		fp.binary = !limits.text && ((inBase && (from.big || isBinary(from.content))) || (inHead && (to.big || isBinary(to.content))))
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

func sides(root string, limit int) (map[string]side, error) {
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
		s := side{mode: filemode.Regular, limit: limit}
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
			if info.Size() > int64(limit) {
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
