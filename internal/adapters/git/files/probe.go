package files

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strconv"

	"github.com/bezmoradi/software-factory/internal/core/push"
	"github.com/pjbgf/sha1cd"
)

const (
	sniffBytes = 8000
	hashChunk  = 1 << 20
)

type probed struct {
	size   int64
	whole  bool
	head   []byte
	cached bool
	pack   *pack
	offset int64
	kind   kind
}

func (s *store) probe(sha string) (probed, error) {
	if !validSHA(sha) {
		return probed{}, fmt.Errorf("%w: %q isn't an object name", push.ErrNotFound, short(sha))
	}
	if o, ok := s.cache[sha]; ok {
		return probed{size: int64(len(o.data)), whole: true, head: o.data[:min(len(o.data), sniffBytes)], cached: true, kind: o.kind}, nil
	}
	p, err := s.probeLoose(sha)
	if err == nil || !errors.Is(err, fs.ErrNotExist) {
		return p, err
	}
	if err := s.loadPacks(); err != nil {
		return probed{}, err
	}
	name, _ := hex.DecodeString(sha)
	for _, pk := range s.packs {
		if offset, ok := pk.find(name); ok {
			p, err := s.probePacked(pk, offset)
			p.pack, p.offset = pk, offset
			return p, err
		}
	}
	return probed{}, fmt.Errorf("%w: object %s", push.ErrNotFound, sha)
}

func (s *store) probeLoose(sha string) (probed, error) {
	f, _, err := openRegular(s.root, filepath.Join("objects", sha[:2], sha[2:]), maxObject+(1<<20))
	if err != nil {
		return probed{}, err
	}
	defer func() { _ = f.Close() }()
	z, err := zlib.NewReader(f)
	if err != nil {
		return probed{}, fmt.Errorf("%w: loose object %s isn't compressed data", push.ErrCorrupt, sha)
	}
	head := make([]byte, 0, 32)
	one := make([]byte, 1)
	for {
		if _, err := io.ReadFull(z, one); err != nil {
			return probed{}, fmt.Errorf("%w: loose object %s is truncated", push.ErrCorrupt, sha)
		}
		if one[0] == 0 {
			break
		}
		if len(head) >= 30 {
			return probed{}, fmt.Errorf("%w: loose object %s has no header", push.ErrCorrupt, sha)
		}
		head = append(head, one[0])
	}
	name, sizeText, ok := bytes.Cut(head, []byte(" "))
	size, err := strconv.ParseInt(string(sizeText), 10, 64)
	if !ok || err != nil || size < 0 {
		return probed{}, fmt.Errorf("%w: loose object %s has a bad size", push.ErrCorrupt, sha)
	}
	p := probed{size: size, whole: true, kind: kindOf(string(name))}
	if size > maxDiffedFile {
		p.head = sniff(z, size)
	}
	return p, nil
}

func sniff(r io.Reader, size int64) []byte {
	buf := make([]byte, min(size, sniffBytes))
	n, _ := io.ReadFull(r, buf)
	return buf[:n]
}

func (s *store) probePacked(p *pack, offset int64) (probed, error) {
	if err := p.open(); err != nil {
		return probed{}, err
	}
	end := p.size - 20
	bad := fmt.Errorf("%w: a pack entry has a bad header", push.ErrCorrupt)
	if offset < 12 || offset >= end {
		return probed{}, bad
	}
	head := make([]byte, 64)
	n, _ := p.file.ReadAt(head, offset)
	head = head[:n]
	i := 0
	next := func() (byte, bool) {
		if i >= len(head) {
			return 0, false
		}
		i++
		return head[i-1], true
	}
	b, ok := next()
	if !ok {
		return probed{}, bad
	}
	k := kind((b >> 4) & 7)
	size := uint64(b & 0x0f)
	shift := uint(4)
	for b&0x80 != 0 {
		if b, ok = next(); !ok || shift > 56 {
			return probed{}, bad
		}
		size |= uint64(b&0x7f) << shift
		shift += 7
	}
	whole := false
	switch k {
	case kindCommit, kindTree, kindBlob, kindTag:
		whole = true
	case kindOfs:
		for {
			if b, ok = next(); !ok {
				return probed{}, bad
			}
			if b&0x80 == 0 {
				break
			}
		}
	case kindRef:
		i += 20
	default:
		return probed{}, bad
	}
	start := offset + int64(i)
	if start >= end {
		return probed{}, bad
	}
	z, err := zlib.NewReader(io.NewSectionReader(p.file, start, end-start))
	if err != nil {
		return probed{}, fmt.Errorf("%w: a pack entry isn't compressed data", push.ErrCorrupt)
	}
	if whole {
		p := probed{size: int64(min(size, 1<<62)), whole: true, kind: k}
		if p.size > maxDiffedFile {
			p.head = sniff(z, int64(min(size, sniffBytes)))
		}
		return p, nil
	}
	return probed{size: deltaResult(sniff(z, 20))}, nil
}

func (s *store) verifiedBinary(ctx context.Context, sha string, p probed) (bool, int64, error) {
	if p.kind != kindBlob || !p.whole {
		return false, 0, nil
	}
	if p.cached {
		o, ok := s.cache[sha]
		if !ok || o.kind != kindBlob || !isBinary(o.data) {
			return false, 0, nil
		}
		return true, lineCount(o.data), nil
	}
	lines := &newlines{}
	head, ok, err := s.streamWhole(ctx, sha, p, lines)
	if err != nil || !ok || !isBinary(head) {
		return false, 0, err
	}
	return true, lines.count(), nil
}

func (s *store) streamWhole(ctx context.Context, sha string, p probed, w io.Writer) ([]byte, bool, error) {
	if p.kind != kindBlob || !p.whole || p.cached || p.size < 0 || p.size > maxObject || p.size > s.workMax()-s.work {
		return nil, false, nil
	}
	s.work += p.size
	z, done, ok := s.openWhole(sha, p)
	if !ok {
		return nil, false, nil
	}
	defer done()
	h := sha1cd.New()
	_, _ = io.WriteString(h, "blob "+strconv.FormatInt(p.size, 10)+"\x00")
	head := make([]byte, min(p.size, sniffBytes))
	if _, err := io.ReadFull(z, head); err != nil {
		return nil, false, nil
	}
	_, _ = h.Write(head)
	if _, err := w.Write(head); err != nil {
		return nil, false, err
	}
	out := io.MultiWriter(h, w)
	for rest := p.size - int64(len(head)); rest > 0; {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		n, err := io.CopyN(out, z, min(rest, hashChunk))
		if err != nil {
			return nil, false, nil
		}
		rest -= n
	}
	if extra, err := io.ReadAll(io.LimitReader(z, 1)); err != nil || len(extra) > 0 {
		return nil, false, nil
	}
	sum, collision := h.(sha1cd.CollisionResistantHash).CollisionResistantSum(nil)
	if collision || hex.EncodeToString(sum) != sha {
		return nil, false, nil
	}
	return head, true, nil
}

func (s *store) openWhole(sha string, p probed) (io.Reader, func(), bool) {
	if p.pack != nil {
		start, ok := s.entryData(p.pack, p.offset)
		if !ok {
			return nil, nil, false
		}
		zr, err := zlib.NewReader(io.NewSectionReader(p.pack.file, start, p.pack.size-20-start))
		if err != nil {
			return nil, nil, false
		}
		return zr, func() {}, true
	}
	f, _, err := openRegular(s.root, filepath.Join("objects", sha[:2], sha[2:]), maxObject+(1<<20))
	if err != nil {
		return nil, nil, false
	}
	zr, err := zlib.NewReader(f)
	if err != nil {
		_ = f.Close()
		return nil, nil, false
	}
	head := make([]byte, 0, 32)
	one := make([]byte, 1)
	for {
		if _, err := io.ReadFull(zr, one); err != nil || len(head) > 30 {
			_ = f.Close()
			return nil, nil, false
		}
		if one[0] == 0 {
			break
		}
		head = append(head, one[0])
	}
	if string(head) != "blob "+strconv.FormatInt(p.size, 10) {
		_ = f.Close()
		return nil, nil, false
	}
	return zr, func() { _ = f.Close() }, true
}

func (s *store) entryData(p *pack, offset int64) (int64, bool) {
	head := make([]byte, 16)
	n, _ := p.file.ReadAt(head, offset)
	for i := range n {
		if head[i]&0x80 == 0 {
			start := offset + int64(i) + 1
			return start, start < p.size-20
		}
	}
	return 0, false
}

type newlines struct {
	n    int64
	last byte
	any  bool
}

func (c *newlines) Write(p []byte) (int, error) {
	if len(p) > 0 {
		c.n += int64(bytes.Count(p, []byte{'\n'}))
		c.last, c.any = p[len(p)-1], true
	}
	return len(p), nil
}

func (c *newlines) count() int64 {
	if c.any && c.last != '\n' {
		return c.n + 1
	}
	return c.n
}
