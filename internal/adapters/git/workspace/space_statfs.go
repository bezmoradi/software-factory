//go:build linux || darwin

package workspace

import (
	"os"
	"syscall"
)

const openDir = os.O_RDONLY | syscall.O_NONBLOCK | syscall.O_DIRECTORY

func (Disk) Space(dir string) (uint64, uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, 0, err
	}
	block := uint64(st.Bsize)
	return st.Bavail * block, st.Blocks * block, nil
}
