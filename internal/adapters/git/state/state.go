package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

const maxFile = 4 << 20

var errDir = errors.New("the state folder must be a folder this user owns, mode 0700, not a symbolic link")

func ownDir(dir string) bool {
	info, err := os.Lstat(dir)
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm() == 0o700 && ownedByMe(info)
}

func read(dir, name string) ([]byte, error) {
	if !ownDir(dir) {
		return nil, errDir
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s can't be opened", name)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !ownedByMe(info) || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s isn't a 0600 file this user owns", name)
	}
	if info.Size() > maxFile {
		return nil, fmt.Errorf("%s is larger than %d MiB, more than this engine reads", name, maxFile>>20)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxFile+1))
	if err != nil || len(data) > maxFile {
		return nil, fmt.Errorf("%s can't be read", name)
	}
	return data, nil
}

func encode(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func write(dir, name string, v any) error {
	data, err := encode(v)
	if err != nil {
		return err
	}
	return writeBytes(dir, name, data)
}

func writeBytes(dir, name string, data []byte) error {
	if !ownDir(dir) {
		return errDir
	}
	if len(data) > maxFile {
		return fmt.Errorf("%s would be larger than %d MiB, which this engine can't read back", name, maxFile>>20)
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
