package files

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/bezmoradi/software-factory/internal/core/push"
)

const (
	maxSmallFile  = 4096
	maxConfig     = 1 << 20
	maxPackedRefs = 32 << 20
	maxSymrefs    = 5
)

type repo struct {
	root      *os.Root
	packed    []byte
	replaced  bool
	store     *store
	pathBytes int64
	heldBytes int64
}

func openRegular(root *os.Root, rel string, limit int64) (*os.File, int64, error) {
	info, err := root.Lstat(rel)
	if err != nil {
		return nil, 0, err
	}
	if !info.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("%w: %s isn't a regular file", push.ErrCorrupt, filepath.Base(rel))
	}
	f, err := root.OpenFile(rel, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, 0, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, err
	}
	if !st.Mode().IsRegular() {
		_ = f.Close()
		return nil, 0, fmt.Errorf("%w: %s isn't a regular file", push.ErrCorrupt, filepath.Base(rel))
	}
	if st.Size() > limit {
		_ = f.Close()
		return nil, 0, fmt.Errorf("%w: %s", push.ErrTooLarge, filepath.Base(rel))
	}
	return f, st.Size(), nil
}

func readSmall(root *os.Root, rel string, limit int64) ([]byte, error) {
	f, size, err := openRegular(root, rel, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, size+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: %s", push.ErrTooLarge, filepath.Base(rel))
	}
	return data, nil
}

func cause(err error) string {
	if pe, ok := errors.AsType[*fs.PathError](err); ok {
		return pe.Err.Error()
	}
	return err.Error()
}

func present(root *os.Root, rel string) bool {
	_, err := root.Lstat(rel)
	return err == nil || !errors.Is(err, fs.ErrNotExist)
}

func realFolderIn(root *os.Root, rel string) bool {
	info, err := root.Lstat(rel)
	return err == nil && info.IsDir()
}

func openRealFolder(path string) (*os.Root, error) {
	seen, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !seen.IsDir() {
		return nil, fmt.Errorf("%s isn't a folder: a link or a file stands in its place", filepath.Base(path))
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(seen, opened) {
		_ = root.Close()
		return nil, fmt.Errorf("%s changed while it was opened", filepath.Base(path))
	}
	return root, nil
}

func openGitFolder(top *os.Root) (*os.Root, error) {
	seen, err := top.Lstat(".git")
	if err != nil {
		return nil, err
	}
	if !seen.IsDir() {
		return nil, errors.New("only a clone's own .git folder is read, and .git here isn't a folder")
	}
	git, err := top.OpenRoot(".git")
	if err != nil {
		return nil, err
	}
	opened, err := git.Stat(".")
	if err != nil || !os.SameFile(seen, opened) {
		_ = git.Close()
		return nil, errors.New(".git changed while it was opened")
	}
	return git, nil
}

func locate(path string) (*os.Root, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", push.ErrNotRepository, err.Error())
	}
	top, err := openRealFolder(abs)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", push.ErrNotRepository, cause(err))
	}
	if present(top, ".git") {
		git, err := openGitFolder(top)
		_ = top.Close()
		if err != nil {
			return nil, fmt.Errorf("%w: %s", push.ErrNotRepository, cause(err))
		}
		return git, nil
	}
	if present(top, "commondir") || present(top, "gitdir") {
		_ = top.Close()
		return nil, fmt.Errorf("%w: a folder that names another repository's folder isn't read", push.ErrNotRepository)
	}
	if !present(top, "HEAD") {
		_ = top.Close()
		return nil, push.ErrNotRepository
	}
	return top, nil
}

func open(path string) (*repo, error) {
	root, err := locate(path)
	if err != nil {
		return nil, err
	}
	r := &repo{root: root}
	if err := r.check(); err != nil {
		_ = root.Close()
		return nil, err
	}
	r.store = newStore(root)
	return r, nil
}

func (r *repo) check() error {
	if !realFolderIn(r.root, "objects") {
		return push.ErrNotRepository
	}
	if err := checkFormat(r.root); err != nil {
		return err
	}
	if err := r.loadPacked(); err != nil {
		return err
	}
	return r.checkSubstitution()
}

func (r *repo) close() {
	if r.store != nil {
		r.store.close()
	}
	_ = r.root.Close()
}

func checkFormat(root *os.Root) error {
	data, err := readSmall(root, "config", maxConfig)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: the config can't be read: %s", push.ErrUnsupported, err.Error())
	}
	entries, err := parseConfig(data)
	if err != nil {
		return fmt.Errorf("%w: %s", push.ErrUnsupported, err.Error())
	}
	for _, e := range entries {
		if e.section != "extensions" || e.sub != "" {
			continue
		}
		switch e.key {
		case "objectformat":
			if e.value == "sha256" {
				return fmt.Errorf("%w: %s", push.ErrUnsupported, sha256Refused)
			}
			if e.value != "sha1" {
				return fmt.Errorf("%w: object format %q", push.ErrUnsupported, e.value)
			}
		case "refstorage":
			if e.value != "files" {
				return fmt.Errorf("%w: ref storage %q", push.ErrUnsupported, e.value)
			}
		}
	}
	return nil
}

func (r *repo) checkSubstitution() error {
	if present(r.root, filepath.Join("info", "grafts")) {
		return push.ErrSubstituted
	}
	if info, err := r.root.Lstat(filepath.Join("refs", "replace")); err == nil {
		if !info.IsDir() {
			return push.ErrSubstituted
		}
		entries, err := fs.ReadDir(r.root.FS(), "refs/replace")
		if err != nil || len(entries) > 0 {
			return push.ErrSubstituted
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return push.ErrSubstituted
	}
	if r.replaced {
		return push.ErrSubstituted
	}
	return nil
}

func (r *repo) loadPacked() error {
	data, err := readSmall(r.root, "packed-refs", maxPackedRefs)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: packed-refs: %s", push.ErrCorrupt, err.Error())
	}
	for line := range bytes.SplitSeq(data, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		if line[0] == '^' {
			if !isHex(string(line[1:]), sha1Hex) {
				return fmt.Errorf("%w: packed-refs has a line it can't read", push.ErrCorrupt)
			}
			continue
		}
		sha, name, ok := strings.Cut(string(line), " ")
		if !ok || !push.ValidRefName(name) {
			return fmt.Errorf("%w: packed-refs has a line it can't read", push.ErrCorrupt)
		}
		if _, err := parseOID(sha); err != nil {
			return err
		}
		r.replaced = r.replaced || strings.HasPrefix(name, "refs/replace/")
	}
	r.packed = data
	return nil
}

func (r *repo) packedRef(want string) (string, bool) {
	for line := range bytes.SplitSeq(r.packed, []byte("\n")) {
		if len(line) <= sha1Hex+1 || line[0] == '#' || line[0] == '^' || line[sha1Hex] != ' ' {
			continue
		}
		if string(bytes.TrimSuffix(line[sha1Hex+1:], []byte("\r"))) == want {
			return strings.ToLower(string(line[:sha1Hex])), true
		}
	}
	return "", false
}

const sha1Hex = 40

func isHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

const sha256Refused = "SHA-256 repositories aren't supported: the engine pushes with go-git v5, which sets the object name's size for the whole program when it is built and doesn't negotiate the object format with the remote, so one engine can't push both SHA-1 and SHA-256 repositories"

func parseOID(s string) (string, error) {
	if isHex(s, sha1Hex) {
		return strings.ToLower(s), nil
	}
	if isHex(s, 64) {
		return "", fmt.Errorf("%w: %s", push.ErrUnsupported, sha256Refused)
	}
	return "", fmt.Errorf("%w: a ref holds something that isn't an object name", push.ErrCorrupt)
}

func validSHA(s string) bool {
	if len(s) != sha1Hex {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func short(s string) string {
	if len(s) > 80 {
		return s[:80]
	}
	return s
}

func perWorktree(name string) bool {
	return name == "HEAD" || strings.HasPrefix(name, "refs/worktree/") || strings.HasPrefix(name, "refs/bisect/") || strings.HasPrefix(name, "refs/rewritten/")
}

func (r *repo) resolve(name string) (string, error) {
	for range maxSymrefs + 1 {
		if !push.ValidRefName(name) {
			return "", fmt.Errorf("%w: %q isn't a ref name the engine reads", push.ErrNotFound, short(name))
		}
		rel := filepath.FromSlash(name)
		info, err := r.root.Lstat(rel)
		switch {
		case err == nil && info.IsDir():
		case err == nil:
			data, err := readSmall(r.root, rel, maxSmallFile)
			if err != nil {
				return "", fmt.Errorf("%w: ref %s: %s", push.ErrCorrupt, name, cause(err))
			}
			text := strings.TrimRight(string(data), " \t\r\n")
			if target, ok := strings.CutPrefix(text, "ref:"); ok {
				name = strings.TrimLeft(target, " \t")
				continue
			}
			return parseOID(text)
		case !errors.Is(err, fs.ErrNotExist):
			return "", fmt.Errorf("%w: ref %s: %s", push.ErrCorrupt, name, cause(err))
		}
		if perWorktree(name) {
			return "", fmt.Errorf("%w: %s", push.ErrNotFound, name)
		}
		if sha, ok := r.packedRef(name); ok {
			return sha, nil
		}
		return "", fmt.Errorf("%w: %s", push.ErrNotFound, name)
	}
	return "", fmt.Errorf("%w: symbolic refs nest more than %d deep", push.ErrCorrupt, maxSymrefs)
}
