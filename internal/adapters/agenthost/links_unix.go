//go:build unix

package agenthost

import (
	"os"
	"syscall"
)

func oneLink(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Nlink == 1
}
