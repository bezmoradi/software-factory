package state

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const VerifyFilesFolder = "verify-files"

type VerifyFiles struct {
	Dir string
}

var _ session.VerifyFiles = VerifyFiles{}

var errVerifyFile = errors.New("not a verification run's file")

func (s VerifyFiles) root(create bool) (*os.Root, error) {
	if !ownDir(s.Dir) {
		return nil, errDir
	}
	dir := filepath.Join(s.Dir, VerifyFilesFolder)
	if create {
		if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
	}
	if !ownDir(dir) {
		return nil, errDir
	}
	return os.OpenRoot(dir)
}

func (s VerifyFiles) runRoot(run string, create bool) (*os.Root, error) {
	if !verifyRunID.MatchString(run) {
		return nil, errVerifyFile
	}
	root, err := s.root(create)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	if create {
		if err := root.Mkdir(run, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
	}
	info, err := root.Lstat(run)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 || !ownedByMe(info) {
		return nil, errVerifyFile
	}
	return root.OpenRoot(run)
}

func (s VerifyFiles) Put(run, name string, r io.Reader) (int64, string, error) {
	if !session.ValidVerifyFile(name) {
		return 0, "", errVerifyFile
	}
	sub, err := s.runRoot(run, true)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = sub.Close() }()
	tmp := "." + name + "." + rand.Text()
	f, err := sub.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, "", err
	}
	sum := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, sum), r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = sub.Rename(tmp, name)
	}
	if err != nil {
		_ = sub.Remove(tmp)
		return 0, "", err
	}
	return n, hex.EncodeToString(sum.Sum(nil)), nil
}

func (s VerifyFiles) Open(run, name string) (io.ReadCloser, error) {
	if !session.ValidVerifyFile(name) {
		return nil, errVerifyFile
	}
	sub, err := s.runRoot(run, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = sub.Close() }()
	info, err := sub.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || !ownedByMe(info) {
		return nil, errVerifyFile
	}
	return sub.Open(name)
}

func (s VerifyFiles) Remove(run string) error {
	if !verifyRunID.MatchString(run) {
		return errVerifyFile
	}
	root, err := s.root(false)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, errDir) && !present(filepath.Join(s.Dir, VerifyFilesFolder)) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return root.RemoveAll(run)
}

func (s VerifyFiles) Runs() ([]string, error) {
	root, err := s.root(false)
	if err != nil {
		if !present(filepath.Join(s.Dir, VerifyFilesFolder)) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = root.Close() }()
	f, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	names, err := f.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, n := range names {
		if verifyRunID.MatchString(n) {
			out = append(out, n)
		}
	}
	return out, nil
}

func present(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
