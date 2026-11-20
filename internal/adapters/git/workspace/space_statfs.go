//go:build linux || darwin

package workspace

import (
	"errors"
	"io/fs"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
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

func fileSize(parent *os.File, e fs.DirEntry, whole bool) (int64, bool, bool) {
	var st unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), e.Name(), &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return 0, whole && !errors.Is(err, unix.ENOENT), false
	}
	if st.Mode&unix.S_IFMT == unix.S_IFDIR {
		return 0, false, true
	}
	return statSize(&st, whole), false, false
}

func statSize(st *unix.Stat_t, whole bool) int64 {
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return 0
	}
	if !whole {
		return st.Size
	}
	return st.Size / max(int64(st.Nlink), 1)
}

func openChild(_ *os.Root, parent *os.File, _, name string) (*os.File, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), parent.Name()+"/"+name), nil
}

func replacedChild(parent *os.File, name string, err error) (int64, bool) {
	switch {
	case errors.Is(err, unix.ENOENT):
		return 0, false
	case errors.Is(err, unix.ENOTDIR), errors.Is(err, unix.ELOOP):
	default:
		return 0, true
	}
	var st unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return 0, !errors.Is(err, unix.ENOENT)
	}
	if st.Mode&unix.S_IFMT == unix.S_IFDIR {
		return 0, true
	}
	return statSize(&st, true), false
}
