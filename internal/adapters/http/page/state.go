package page

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

var errStateDir = errors.New("the state folder must be a folder this user owns, mode 0700, not a symbolic link")

func EnsureStateDir(dir string) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("the state folder must be an absolute path")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create the state folder: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ownedByMe(info) {
		return errStateDir
	}
	if info.Mode().Perm() != 0o700 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return errStateDir
		}
	}
	if !ownDir(dir) {
		return errStateDir
	}
	return nil
}

func ownDir(dir string) bool {
	info, err := os.Lstat(dir)
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm() == 0o700 && ownedByMe(info)
}

func readOwned(dir, name string, limit int64) ([]byte, bool) {
	if !ownDir(dir) {
		return nil, false
	}
	at := filepath.Join(dir, name)
	info, err := os.Lstat(at)
	if err != nil || !info.Mode().IsRegular() || !ownedByMe(info) || info.Size() > limit {
		return nil, false
	}
	f, err := os.Open(at)
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, false
	}
	return data, true
}

func writeAtomic(dir, name string, data []byte) error {
	if !ownDir(dir) {
		return errStateDir
	}
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
