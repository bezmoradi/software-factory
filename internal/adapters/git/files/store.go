package files

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"syscall"

	"github.com/bezmoradi/software-factory/internal/core/push"
	"github.com/pjbgf/sha1cd"
)

const (
	maxObject            = 100 << 20
	maxIndex             = 256 << 20
	maxIndexTotal        = 256 << 20
	maxWork              = 4 << 30
	maxPacks             = 1000
	maxPackFolderEntries = 4 * maxPacks
	packFolderBatch      = 256
	maxReads             = 2_000_000
	maxDeltaDepth        = 4096
	cacheLimit           = 64 << 20
	cacheEntry           = 4 << 20
	idxMagic             = 0xff744f63
)

type kind byte

const (
	kindCommit kind = 1
	kindTree   kind = 2
	kindBlob   kind = 3
	kindTag    kind = 4
	kindOfs    kind = 6
	kindRef    kind = 7
)

func (k kind) String() string {
	switch k {
	case kindCommit:
		return "commit"
	case kindTree:
		return "tree"
	case kindBlob:
		return "blob"
	case kindTag:
		return "tag"
	}
	return ""
}

func kindOf(name string) kind {
	switch name {
	case "commit":
		return kindCommit
	case "tree":
		return kindTree
	case "blob":
		return kindBlob
	case "tag":
		return kindTag
	}
	return 0
}

type object struct {
	kind kind
	data []byte
}

type pack struct {
	idx    []byte
	count  int
	file   *os.File
	size   int64
	opened bool
	path   string
	root   *os.Root
	broken error
}

type store struct {
	root    *os.Root
	packs   []*pack
	loaded  bool
	reads   int
	work    int64
	indexed int64
	cache   map[string]object
	bases   map[baseKey]object
	cached  int
}

type baseKey struct {
	pack   *pack
	offset int64
}

func newStore(root *os.Root) *store {
	return &store{root: root, cache: map[string]object{}, bases: map[baseKey]object{}}
}

func (s *store) close() {
	for _, p := range s.packs {
		if p.file != nil {
			_ = p.file.Close()
		}
	}
}

func (s *store) count() error {
	s.reads++
	if s.reads > maxReads {
		return fmt.Errorf("%w: the repository needs more than %d object reads", push.ErrTooLarge, maxReads)
	}
	return nil
}

func (s *store) remember(sha string, o object) {
	if len(o.data) > cacheEntry {
		return
	}
	if s.cached+len(o.data) > cacheLimit {
		s.cache = map[string]object{}
		s.bases = map[baseKey]object{}
		s.cached = 0
	}
	s.cache[sha] = o
	s.cached += len(o.data)
}

func (s *store) rememberBase(k baseKey, o object) {
	if len(o.data) > cacheEntry {
		return
	}
	if s.cached+len(o.data) > cacheLimit {
		s.cache = map[string]object{}
		s.bases = map[baseKey]object{}
		s.cached = 0
	}
	s.bases[k] = o
	s.cached += len(o.data)
}

func (s *store) read(sha string) (object, error) {
	return s.readDepth(sha, 0)
}

func (s *store) readDepth(sha string, depth int) (o object, err error) {
	if !validSHA(sha) {
		return object{}, fmt.Errorf("%w: %q isn't an object name", push.ErrNotFound, short(sha))
	}
	if o, ok := s.cache[sha]; ok {
		return o, nil
	}
	if err := s.count(); err != nil {
		return object{}, err
	}
	defer func() {
		if p := recover(); p != nil {
			o, err = object{}, fmt.Errorf("%w: object %s can't be parsed", push.ErrCorrupt, sha)
		}
	}()
	var bad error
	o, err = s.loose(sha)
	if err == nil {
		s.remember(sha, o)
		return o, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		if errors.Is(err, push.ErrTooLarge) {
			return object{}, err
		}
		bad = err
	}
	if err := s.loadPacks(); err != nil {
		return object{}, err
	}
	name, _ := hex.DecodeString(sha)
	for _, p := range s.packs {
		offset, ok := p.find(name)
		if !ok {
			continue
		}
		o, err := s.packed(p, offset, depth)
		if err == nil && digest(o.kind, o.data) == sha {
			s.remember(sha, o)
			return o, nil
		}
		if err != nil && (errors.Is(err, push.ErrTooLarge) || errors.Is(err, push.ErrUnsupported)) {
			return object{}, err
		}
		if err != nil {
			bad = err
		} else {
			bad = fmt.Errorf("%w: object %s in a pack has other content than its name says", push.ErrCorrupt, sha)
		}
	}
	if bad != nil {
		return object{}, bad
	}
	return object{}, fmt.Errorf("%w: object %s", push.ErrNotFound, sha)
}

func digest(k kind, data []byte) string {
	head := k.String() + " " + strconv.Itoa(len(data)) + "\x00"
	buf := make([]byte, 0, len(head)+len(data))
	buf = append(append(buf, head...), data...)
	sum, collision := sha1cd.Sum(buf)
	if collision {
		return ""
	}
	return hex.EncodeToString(sum[:])
}

func (s *store) loose(sha string) (object, error) {
	f, _, err := openRegular(s.root, filepath.Join("objects", sha[:2], sha[2:]), maxObject+(1<<20))
	if err != nil {
		return object{}, err
	}
	defer func() { _ = f.Close() }()
	z, err := zlib.NewReader(f)
	if err != nil {
		return object{}, fmt.Errorf("%w: loose object %s isn't compressed data", push.ErrCorrupt, sha)
	}
	head := make([]byte, 0, 32)
	one := make([]byte, 1)
	for {
		if _, err := io.ReadFull(z, one); err != nil {
			return object{}, fmt.Errorf("%w: loose object %s is truncated", push.ErrCorrupt, sha)
		}
		if one[0] == 0 {
			break
		}
		if len(head) >= 30 {
			return object{}, fmt.Errorf("%w: loose object %s has no header", push.ErrCorrupt, sha)
		}
		head = append(head, one[0])
	}
	name, sizeText, ok := bytes.Cut(head, []byte(" "))
	k := kindOf(string(name))
	if !ok || k == 0 {
		return object{}, fmt.Errorf("%w: loose object %s has an unknown type", push.ErrCorrupt, sha)
	}
	size, err := strconv.ParseInt(string(sizeText), 10, 64)
	if err != nil || size < 0 || (len(sizeText) > 1 && sizeText[0] == '0') {
		return object{}, fmt.Errorf("%w: loose object %s has a bad size", push.ErrCorrupt, sha)
	}
	if size > maxObject {
		return object{}, fmt.Errorf("%w: object %s is over %d MiB", push.ErrTooLarge, sha, maxObject>>20)
	}
	if s.work += size; s.work > maxWork {
		return object{}, fmt.Errorf("%w: reading the repository's objects needs more than %d GiB of work", push.ErrTooLarge, maxWork>>30)
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(z, data); err != nil {
		return object{}, fmt.Errorf("%w: loose object %s is truncated", push.ErrCorrupt, sha)
	}
	if n, err := z.Read(one); n > 0 || !errors.Is(err, io.EOF) {
		return object{}, fmt.Errorf("%w: loose object %s doesn't end where its size says", push.ErrCorrupt, sha)
	}
	if digest(k, data) != sha {
		return object{}, fmt.Errorf("%w: loose object %s has other content than its name says", push.ErrCorrupt, sha)
	}
	return object{kind: k, data: data}, nil
}

func (s *store) loadPacks() error {
	if s.loaded {
		return nil
	}
	s.loaded = true
	dir := filepath.Join("objects", "pack")
	if info, err := s.root.Lstat(dir); err == nil && !info.IsDir() {
		return fmt.Errorf("%w: the pack folder isn't a folder", push.ErrCorrupt)
	}
	names, err := s.packIndexNames(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	sort.Strings(names)
	if len(names) > maxPacks {
		return fmt.Errorf("%w: more than %d packs", push.ErrTooLarge, maxPacks)
	}
	for _, n := range names {
		idx, err := readSmall(s.root, filepath.Join(dir, n), maxIndex)
		if err != nil {
			if errors.Is(err, push.ErrTooLarge) {
				return err
			}
			continue
		}
		if s.indexed += int64(len(idx)); s.indexed > maxIndexTotal {
			return fmt.Errorf("%w: the pack indexes are over %d MiB together", push.ErrTooLarge, maxIndexTotal>>20)
		}
		p, ok := parseIndex(idx)
		if !ok {
			continue
		}
		p.path = filepath.Join(dir, n[:len(n)-4]+".pack")
		p.root = s.root
		s.packs = append(s.packs, p)
	}
	return nil
}

func (s *store) packIndexNames(dir string) ([]string, error) {
	d, err := s.root.OpenFile(dir, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("%w: the pack folder can't be read", push.ErrCorrupt)
	}
	defer func() { _ = d.Close() }()
	var names []string
	listed := 0
	for {
		batch, err := d.Readdirnames(packFolderBatch)
		listed += len(batch)
		if listed > maxPackFolderEntries {
			return nil, fmt.Errorf("%w: the pack folder holds more than %d entries", push.ErrTooLarge, maxPackFolderEntries)
		}
		for _, n := range batch {
			if len(n) > len("pack-.idx") && n[:5] == "pack-" && filepath.Ext(n) == ".idx" {
				names = append(names, n)
			}
		}
		if errors.Is(err, io.EOF) {
			return names, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%w: the pack folder can't be read", push.ErrCorrupt)
		}
	}
}

func parseIndex(idx []byte) (*pack, bool) {
	if len(idx) < 8+256*4+40 || binary.BigEndian.Uint32(idx) != idxMagic || binary.BigEndian.Uint32(idx[4:]) != 2 {
		return nil, false
	}
	prev := uint32(0)
	for i := range 256 {
		v := binary.BigEndian.Uint32(idx[8+i*4:])
		if v < prev {
			return nil, false
		}
		prev = v
	}
	n := int(prev)
	if n < 0 || n > (len(idx)-8-256*4-40)/28 {
		return nil, false
	}
	need := 8 + 256*4 + n*28
	large := 0
	offsets := need - n*4
	for i := range n {
		if binary.BigEndian.Uint32(idx[offsets+i*4:])&0x80000000 != 0 {
			large++
		}
	}
	if len(idx) != need+large*8+40 {
		return nil, false
	}
	return &pack{idx: idx, count: n}, true
}

func (p *pack) find(name []byte) (int64, bool) {
	first := int(name[0])
	lo := 0
	if first > 0 {
		lo = int(binary.BigEndian.Uint32(p.idx[8+(first-1)*4:]))
	}
	hi := int(binary.BigEndian.Uint32(p.idx[8+first*4:]))
	names := 8 + 256*4
	for lo < hi {
		mid := (lo + hi) / 2
		c := bytes.Compare(name, p.idx[names+mid*20:names+mid*20+20])
		if c == 0 {
			offsets := names + p.count*24
			small := binary.BigEndian.Uint32(p.idx[offsets+mid*4:])
			if small&0x80000000 == 0 {
				return int64(small), true
			}
			at := offsets + p.count*4 + int(small&0x7fffffff)*8
			if at+8 > len(p.idx)-40 {
				return 0, false
			}
			v := binary.BigEndian.Uint64(p.idx[at:])
			if v > 1<<62 {
				return 0, false
			}
			return int64(v), true
		}
		if c < 0 {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	return 0, false
}

func (p *pack) open() error {
	if p.opened {
		return p.broken
	}
	p.opened = true
	f, size, err := openRegular(p.root, p.path, 1<<62)
	if err != nil {
		p.broken = fmt.Errorf("%w: a pack can't be opened", push.ErrCorrupt)
		return p.broken
	}
	head := make([]byte, 12)
	if size < 32 {
		_ = f.Close()
		p.broken = fmt.Errorf("%w: a pack is truncated", push.ErrCorrupt)
		return p.broken
	}
	if _, err := f.ReadAt(head, 0); err != nil || string(head[:4]) != "PACK" {
		_ = f.Close()
		p.broken = fmt.Errorf("%w: a pack has no header", push.ErrCorrupt)
		return p.broken
	}
	if v := binary.BigEndian.Uint32(head[4:]); v != 2 && v != 3 {
		_ = f.Close()
		p.broken = fmt.Errorf("%w: a pack has an unknown version", push.ErrCorrupt)
		return p.broken
	}
	trailer := make([]byte, 20)
	if _, err := f.ReadAt(trailer, size-20); err != nil || int(binary.BigEndian.Uint32(head[8:])) != p.count || !bytes.Equal(trailer, p.idx[len(p.idx)-40:len(p.idx)-20]) {
		_ = f.Close()
		p.broken = fmt.Errorf("%w: a pack and its index disagree", push.ErrCorrupt)
		return p.broken
	}
	p.file, p.size = f, size
	return nil
}

func (s *store) packed(p *pack, offset int64, depth int) (object, error) {
	if depth > maxDeltaDepth {
		return object{}, fmt.Errorf("%w: a delta chain is deeper than %d", push.ErrCorrupt, maxDeltaDepth)
	}
	if o, ok := s.bases[baseKey{p, offset}]; ok {
		return o, nil
	}
	if err := s.count(); err != nil {
		return object{}, err
	}
	if err := p.open(); err != nil {
		return object{}, err
	}
	end := p.size - 20
	if offset < 12 || offset >= end {
		return object{}, fmt.Errorf("%w: a pack offset is out of range", push.ErrCorrupt)
	}
	head := make([]byte, 64)
	n, _ := p.file.ReadAt(head, offset)
	head = head[:n]
	i := 0
	next := func() (byte, bool) {
		if i >= len(head) {
			return 0, false
		}
		b := head[i]
		i++
		return b, true
	}
	b, ok := next()
	if !ok {
		return object{}, fmt.Errorf("%w: a pack entry is truncated", push.ErrCorrupt)
	}
	k := kind((b >> 4) & 7)
	size := uint64(b & 0x0f)
	shift := uint(4)
	for b&0x80 != 0 {
		if b, ok = next(); !ok || shift > 56 {
			return object{}, fmt.Errorf("%w: a pack entry has a bad size", push.ErrCorrupt)
		}
		size |= uint64(b&0x7f) << shift
		shift += 7
	}
	if size > maxObject {
		return object{}, fmt.Errorf("%w: a packed object is over %d MiB", push.ErrTooLarge, maxObject>>20)
	}
	var base object
	switch k {
	case kindCommit, kindTree, kindBlob, kindTag:
	case kindOfs:
		b, ok := next()
		if !ok {
			return object{}, fmt.Errorf("%w: a delta is truncated", push.ErrCorrupt)
		}
		rel := uint64(b & 0x7f)
		for b&0x80 != 0 {
			if b, ok = next(); !ok || rel > 1<<55 {
				return object{}, fmt.Errorf("%w: a delta has a bad offset", push.ErrCorrupt)
			}
			rel = ((rel + 1) << 7) | uint64(b&0x7f)
		}
		if rel == 0 || rel > uint64(offset-12) {
			return object{}, fmt.Errorf("%w: a delta has a bad offset", push.ErrCorrupt)
		}
		var err error
		if base, err = s.packed(p, offset-int64(rel), depth+1); err != nil {
			return object{}, err
		}
	case kindRef:
		if i+20 > len(head) {
			return object{}, fmt.Errorf("%w: a delta is truncated", push.ErrCorrupt)
		}
		name := hex.EncodeToString(head[i : i+20])
		i += 20
		var err error
		if base, err = s.readDepth(name, depth+1); err != nil {
			return object{}, err
		}
	default:
		return object{}, fmt.Errorf("%w: a pack entry has an unknown type", push.ErrCorrupt)
	}
	start := offset + int64(i)
	if start >= end {
		return object{}, fmt.Errorf("%w: a pack entry is truncated", push.ErrCorrupt)
	}
	if s.work += int64(size); s.work > maxWork {
		return object{}, fmt.Errorf("%w: reading the repository's objects needs more than %d GiB of work", push.ErrTooLarge, maxWork>>30)
	}
	z, err := zlib.NewReader(io.NewSectionReader(p.file, start, end-start))
	if err != nil {
		return object{}, fmt.Errorf("%w: a pack entry isn't compressed data", push.ErrCorrupt)
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(z, data); err != nil {
		return object{}, fmt.Errorf("%w: a pack entry is truncated", push.ErrCorrupt)
	}
	o := object{kind: k, data: data}
	if k == kindOfs || k == kindRef {
		out, err := applyDelta(base.data, data)
		if err != nil {
			return object{}, err
		}
		if s.work += int64(len(out)); s.work > maxWork {
			return object{}, fmt.Errorf("%w: reading the repository's objects needs more than %d GiB of work", push.ErrTooLarge, maxWork>>30)
		}
		o = object{kind: base.kind, data: out}
	}
	s.rememberBase(baseKey{p, offset}, o)
	return o, nil
}

func applyDelta(base, delta []byte) ([]byte, error) {
	bad := fmt.Errorf("%w: a delta can't be applied", push.ErrCorrupt)
	i := 0
	varint := func() (uint64, bool) {
		var v uint64
		var shift uint
		for {
			if i >= len(delta) || shift > 63 {
				return 0, false
			}
			b := delta[i]
			i++
			v |= uint64(b&0x7f) << shift
			shift += 7
			if b&0x80 == 0 {
				return v, true
			}
		}
	}
	srcSize, ok := varint()
	if !ok || srcSize != uint64(len(base)) {
		return nil, bad
	}
	size, ok := varint()
	if !ok {
		return nil, bad
	}
	if size > maxObject {
		return nil, fmt.Errorf("%w: a delta result is over %d MiB", push.ErrTooLarge, maxObject>>20)
	}
	out := make([]byte, 0, size)
	for i < len(delta) {
		op := delta[i]
		i++
		switch {
		case op&0x80 != 0:
			var off, n uint64
			for k := range 4 {
				if op&(1<<k) != 0 {
					if i >= len(delta) {
						return nil, bad
					}
					off |= uint64(delta[i]) << (8 * k)
					i++
				}
			}
			for k := range 3 {
				if op&(1<<(4+k)) != 0 {
					if i >= len(delta) {
						return nil, bad
					}
					n |= uint64(delta[i]) << (8 * k)
					i++
				}
			}
			if n == 0 {
				n = 0x10000
			}
			if off+n > uint64(len(base)) || uint64(len(out))+n > size {
				return nil, bad
			}
			out = append(out, base[off:off+n]...)
		case op != 0:
			n := int(op)
			if i+n > len(delta) || uint64(len(out)+n) > size {
				return nil, bad
			}
			out = append(out, delta[i:i+n]...)
			i += n
		default:
			return nil, bad
		}
	}
	if uint64(len(out)) != size {
		return nil, bad
	}
	return out, nil
}
