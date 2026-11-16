//go:build !linux && !darwin

package workspace

import (
	"errors"
	"os"
)

const openDir = os.O_RDONLY

func (Disk) Space(string) (uint64, uint64, error) {
	return 0, 0, errors.New("free space isn't read on this platform")
}
