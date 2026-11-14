package keep

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/push"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/idxfile"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/storage/memory"
)

const (
	headsFile   = "heads"
	headsHeader = "sf-keep 1"
	maxHeads    = 128 << 10
	maxListed   = 5000
)

var (
	sidPattern  = regexp.MustCompile(`^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$`)
	sha1Pattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
	packPattern = regexp.MustCompile(`^pack-[0-9a-f]{40}\.pack$`)
	repoName    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
	branchName  = regexp.MustCompile(`^sf/[0-9a-f]{8}$`)
	stops       = map[session.KeepStop]bool{session.KeepSecret: true, session.KeepPassword: true, session.KeepLarge: true, session.KeepFull: true}
	errDir      = errors.New("the copy's folder must be a folder this user owns, mode 0700, not a symbolic link")
)

type Store struct {
	Dir       string
	Workspace string
	OpenTree  func(path string) (*os.Root, error)
}

var _ session.KeepStore = Store{}

func ownDir(dir string) bool {
	info, err := os.Lstat(dir)
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm() == 0o700 && ownedByMe(info)
}

func makeDir(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	if info, err := os.Lstat(dir); err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 && ownedByMe(info) && info.Mode().Perm() != 0o700 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
	}
	if !ownDir(dir) {
		return errDir
	}
	return nil
}

func (s Store) sessionDir(sid string, create bool) (string, error) {
	if !sidPattern.MatchString(sid) || s.Dir == "" {
		return "", errors.New("not a session the engine copies")
	}
	if create {
		if err := makeDir(s.Dir); err != nil {
			return "", err
		}
	} else if _, err := os.Lstat(s.Dir); errors.Is(err, fs.ErrNotExist) {
		return "", fs.ErrNotExist
	} else if !ownDir(s.Dir) {
		return "", errDir
	}
	dir := filepath.Join(s.Dir, sid)
	if create {
		return dir, makeDir(dir)
	}
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return "", fs.ErrNotExist
	}
	if !ownDir(dir) {
		return "", errDir
	}
	return dir, nil
}

func openOwned(name string) (*os.File, os.FileInfo, error) {
	f, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !ownedByMe(info) || info.Mode().Perm()&0o077 != 0 {
		_ = f.Close()
		return nil, nil, fmt.Errorf("%s isn't a 0600 file this user owns", filepath.Base(name))
	}
	return f, info, nil
}

func (s Store) Load(sid string) (session.KeepRecord, error) {
	dir, err := s.sessionDir(sid, false)
	if errors.Is(err, fs.ErrNotExist) {
		return session.KeepRecord{}, nil
	}
	if err != nil {
		return session.KeepRecord{}, err
	}
	f, info, err := openOwned(filepath.Join(dir, headsFile))
	if errors.Is(err, fs.ErrNotExist) {
		return session.KeepRecord{}, nil
	}
	if err != nil {
		return session.KeepRecord{}, err
	}
	defer func() { _ = f.Close() }()
	if info.Size() > maxHeads {
		return session.KeepRecord{}, errors.New("the list of copied heads is larger than the engine reads")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxHeads+1))
	if err != nil || len(data) > maxHeads {
		return session.KeepRecord{}, errors.New("the list of copied heads can't be read")
	}
	return parse(data)
}

func parse(data []byte) (session.KeepRecord, error) {
	var rec session.KeepRecord
	bad := errors.New("the list of copied heads isn't in the format this engine writes")
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 4096), 4096)
	first := true
	stopped := false
	for sc.Scan() {
		line := sc.Text()
		if first {
			if line != headsHeader {
				return session.KeepRecord{}, bad
			}
			first = false
			continue
		}
		f := strings.Split(line, " ")
		switch {
		case len(f) == 5 && f[0] == "pack" && !stopped:
			n, err1 := strconv.ParseInt(f[3], 10, 64)
			at, err2 := strconv.ParseInt(f[4], 10, 64)
			if !sha1Pattern.MatchString(f[1]) || !packPattern.MatchString(f[2]) || err1 != nil || err2 != nil || n <= 0 || n > session.KeepSessionCap || at < 0 || len(rec.Packs) >= session.MaxKeptPacks {
				return session.KeepRecord{}, bad
			}
			rec.Packs = append(rec.Packs, session.KeptPack{Commit: f[1], Pack: f[2], Bytes: n, At: time.Unix(at, 0)})
		case len(f) == 3 && f[0] == "stopped" && !stopped:
			if f[1] != "-" && !sha1Pattern.MatchString(f[1]) || !stops[session.KeepStop(f[2])] {
				return session.KeepRecord{}, bad
			}
			if f[1] != "-" {
				rec.Stopped = f[1]
			}
			rec.Why = session.KeepStop(f[2])
			stopped = true
		default:
			return session.KeepRecord{}, bad
		}
	}
	if sc.Err() != nil || first {
		return session.KeepRecord{}, bad
	}
	return rec, nil
}

func format(rec session.KeepRecord) ([]byte, error) {
	var b strings.Builder
	b.WriteString(headsHeader + "\n")
	if len(rec.Packs) > session.MaxKeptPacks {
		return nil, errors.New("more packs than the engine keeps for one session")
	}
	for _, p := range rec.Packs {
		if !sha1Pattern.MatchString(p.Commit) || !packPattern.MatchString(p.Pack) || p.Bytes <= 0 {
			return nil, errors.New("a copied head the engine can't write")
		}
		fmt.Fprintf(&b, "pack %s %s %d %d\n", p.Commit, p.Pack, p.Bytes, max(p.At.Unix(), 0))
	}
	if rec.Why != "" {
		at := rec.Stopped
		if at == "" {
			at = "-"
		}
		if at != "-" && !sha1Pattern.MatchString(at) || !stops[rec.Why] {
			return nil, errors.New("a stop the engine can't write")
		}
		fmt.Fprintf(&b, "stopped %s %s\n", at, rec.Why)
	}
	if b.Len() > maxHeads {
		return nil, errors.New("the list of copied heads would be larger than the engine reads")
	}
	return []byte(b.String()), nil
}

func writeOwned(dir, name string, data []byte) error {
	f, err := os.CreateTemp(dir, "."+name+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		return err
	}
	ok = true
	return nil
}

func (s Store) Save(sid string, rec session.KeepRecord) error {
	data, err := format(rec)
	if err != nil {
		return err
	}
	dir, err := s.sessionDir(sid, true)
	if err != nil {
		return err
	}
	return writeOwned(dir, headsFile, data)
}

func encode(objects []push.Object) ([]byte, string, error) {
	sto := memory.NewStorage()
	hashes := make([]plumbing.Hash, 0, len(objects))
	for i := range objects {
		o := objects[i]
		objects[i].Data = nil
		t, err := plumbing.ParseObjectType(o.Kind)
		if err != nil || t == plumbing.OFSDeltaObject || t == plumbing.REFDeltaObject {
			return nil, "", errors.New("the reader gave an object of an unknown type")
		}
		obj := &plumbing.MemoryObject{}
		obj.SetType(t)
		if _, err := obj.Write(o.Data); err != nil {
			return nil, "", errors.New("an object couldn't be prepared")
		}
		if obj.Hash().String() != o.SHA {
			return nil, "", errors.New("an object's content doesn't match its name")
		}
		h, err := sto.SetEncodedObject(obj)
		if err != nil {
			return nil, "", errors.New("an object couldn't be prepared")
		}
		hashes = append(hashes, h)
	}
	var buf bytes.Buffer
	sum, err := packfile.NewEncoder(&buf, sto, false).Encode(hashes, 0)
	if err != nil {
		return nil, "", errors.New("the pack couldn't be built")
	}
	return buf.Bytes(), sum.String(), nil
}

func (s Store) WritePack(sid string, objects []push.Object, limit int64) (string, int64, error) {
	if len(objects) == 0 {
		return "", 0, errors.New("no objects to copy")
	}
	pack, sum, err := encode(objects)
	if err != nil {
		return "", 0, err
	}
	if int64(len(pack)) > limit {
		return "", 0, session.ErrKeepFull
	}
	dir, err := s.sessionDir(sid, true)
	if err != nil {
		return "", 0, err
	}
	name := "pack-" + sum + ".pack"
	if err := writeOwned(dir, name, pack); err != nil {
		return "", 0, err
	}
	return name, int64(len(pack)), nil
}

func (s Store) Sessions() ([]string, error) {
	if _, err := os.Lstat(s.Dir); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if !ownDir(s.Dir) {
		return nil, errDir
	}
	entries, err := readDir(s.Dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && sidPattern.MatchString(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

func readDir(dir string) ([]os.DirEntry, error) {
	f, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	entries, err := f.ReadDir(maxListed + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > maxListed {
		return nil, errors.New("the copy's folder holds more entries than the engine reads")
	}
	return entries, nil
}

func (s Store) Used() (int64, error) {
	sids, err := s.Sessions()
	if err != nil {
		return 0, err
	}
	var total int64
	for _, sid := range sids {
		dir := filepath.Join(s.Dir, sid)
		if !ownDir(dir) {
			return 0, errDir
		}
		entries, err := readDir(dir)
		if err != nil {
			return 0, err
		}
		for _, e := range entries {
			if !packPattern.MatchString(e.Name()) {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			total += info.Size()
		}
	}
	return total, nil
}

func (s Store) Remove(sid string) error {
	if !sidPattern.MatchString(sid) || s.Dir == "" {
		return errors.New("not a session the engine copies")
	}
	if _, err := os.Lstat(s.Dir); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if !ownDir(s.Dir) {
		return errDir
	}
	return os.RemoveAll(filepath.Join(s.Dir, sid))
}

func random() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s Store) clone(box, repo string) (*os.Root, func(), error) {
	if !repoName.MatchString(repo) || strings.Contains(repo, "..") || s.Workspace == "" || !session.ValidBox(box) || s.OpenTree == nil {
		return nil, nil, errors.New("the repository's folder name isn't one the engine uses")
	}
	clone := session.BoxClone(s.Workspace, box, repo)
	if clone == "" {
		return nil, nil, errors.New("the repository's folder name isn't one the engine uses")
	}
	git, err := s.OpenTree(path.Join(clone, ".git"))
	if err != nil {
		return nil, nil, errors.New("the session clone's .git folder can't be opened without following a link")
	}
	return git, func() { _ = git.Close() }, nil
}

func plainFolders(git *os.Root, names ...string) error {
	for _, d := range names {
		info, err := git.Lstat(d)
		if errors.Is(err, fs.ErrNotExist) {
			if err := git.Mkdir(d, 0o775); err != nil {
				return errors.New("the clone's " + d + " folder couldn't be made")
			}
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("the clone's " + d + " isn't a plain folder")
		}
	}
	return nil
}

func (s Store) RestoreObjects(sid, box, repo, commit string) error {
	if !sha1Pattern.MatchString(commit) {
		return errors.New("not a full commit id")
	}
	rec, err := s.Load(sid)
	if err != nil {
		return err
	}
	if len(rec.Packs) == 0 {
		return errors.New("the copy holds no packs")
	}
	dir, err := s.sessionDir(sid, false)
	if err != nil {
		return err
	}
	git, done, err := s.clone(box, repo)
	if err != nil {
		return err
	}
	defer done()
	if err := plainFolders(git, "objects", "objects/pack"); err != nil {
		return err
	}
	found := false
	seen := map[string]bool{}
	for _, p := range rec.Packs {
		if seen[p.Pack] {
			continue
		}
		seen[p.Pack] = true
		has, err := copyPack(git, filepath.Join(dir, p.Pack), p.Pack, commit)
		if err != nil {
			return err
		}
		found = found || has
	}
	if !found {
		return errors.New("the copy doesn't hold that commit")
	}
	return nil
}

func copyPack(git *os.Root, from, name, commit string) (bool, error) {
	f, _, err := openOwned(from)
	if err != nil {
		return false, errors.New("a pack of the copy can't be opened")
	}
	defer func() { _ = f.Close() }()
	w := new(idxfile.Writer)
	parser, err := packfile.NewParser(packfile.NewScanner(f), w)
	if err != nil {
		return false, errors.New("a pack of the copy can't be read")
	}
	sum, err := parser.Parse()
	if err != nil || "pack-"+sum.String()+".pack" != name {
		return false, errors.New("a pack of the copy doesn't match its name")
	}
	idx, err := w.Index()
	if err != nil {
		return false, errors.New("a pack's index couldn't be built")
	}
	has, err := idx.Contains(plumbing.NewHash(commit))
	if err != nil {
		return false, errors.New("a pack's index couldn't be read")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return false, errors.New("a pack of the copy can't be read")
	}
	base := "objects/pack/pack-" + sum.String()
	for _, target := range []string{base + ".pack", base + ".idx"} {
		if info, err := git.Lstat(target); err == nil && !info.Mode().IsRegular() {
			return false, errors.New("the clone has something other than a file at " + target)
		}
	}
	tmp := "objects/pack/tmp_pack_" + random()
	out, err := git.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return false, errors.New("the pack couldn't be written into the clone")
	}
	_, err = io.Copy(out, f)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = git.Remove(tmp)
		return false, errors.New("the pack couldn't be written into the clone")
	}
	tmpIdx := "objects/pack/tmp_idx_" + random()
	g, err := git.OpenFile(tmpIdx, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		_ = git.Remove(tmp)
		return false, errors.New("the pack's index couldn't be written into the clone")
	}
	_, err = idxfile.NewEncoder(g).Encode(idx)
	if cerr := g.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = git.Remove(tmp)
		_ = git.Remove(tmpIdx)
		return false, errors.New("the pack's index couldn't be written into the clone")
	}
	_ = git.Chmod(tmp, 0o444)
	_ = git.Chmod(tmpIdx, 0o444)
	if err := git.Rename(tmp, base+".pack"); err != nil {
		_ = git.Remove(tmp)
		_ = git.Remove(tmpIdx)
		return false, errors.New("the pack couldn't be put in place in the clone")
	}
	if err := git.Rename(tmpIdx, base+".idx"); err != nil {
		_ = git.Remove(tmpIdx)
		return false, errors.New("the pack's index couldn't be put in place in the clone")
	}
	return has, nil
}

func (s Store) WriteBranch(box, repo, branch, commit string) error {
	if !branchName.MatchString(branch) || !push.ValidBranch(branch) || !sha1Pattern.MatchString(commit) {
		return errors.New("the engine restores only a session branch at a full commit id")
	}
	git, done, err := s.clone(box, repo)
	if err != nil {
		return err
	}
	defer done()
	ref := "refs/heads/" + branch
	dir := path.Dir(ref)
	if err := plainFolders(git, "refs", "refs/heads", dir); err != nil {
		return err
	}
	if info, err := git.Lstat(ref); err == nil && !info.Mode().IsRegular() {
		return errors.New("the clone has a symbolic link or a folder at " + ref)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return errors.New("the clone's " + ref + " can't be read")
	}
	tmp := path.Join(dir, ".sf-"+random())
	f, err := git.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o664)
	if err != nil {
		return errors.New("the branch couldn't be written into the clone")
	}
	_, err = f.Write([]byte(commit + "\n"))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = git.Rename(tmp, ref)
	}
	if err != nil {
		_ = git.Remove(tmp)
		return errors.New("the branch couldn't be written into the clone")
	}
	return nil
}
