//go:build !linux && !darwin

package workspace

import (
	"errors"
	"io/fs"
	"os"
)

const openDir = os.O_RDONLY

func (Disk) Space(string) (uint64, uint64, error) {
	return 0, 0, errors.New("free space isn't read on this platform")
}

func fileSize(_ *os.File, e fs.DirEntry, _ bool) (int64, bool, bool) {
	info, err := e.Info()
	if err != nil {
		return 0, false, false
	}
	return info.Size(), false, false
}

func openChild(root *os.Root, _ *os.File, at, _ string) (*os.File, error) {
	return root.OpenFile(at, openDir, 0)
}

func replacedChild(*os.File, string, error) (int64, bool) {
	return 0, true
}
