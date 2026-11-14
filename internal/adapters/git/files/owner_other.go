//go:build !unix

package files

import "os"

func ownedByMe(os.FileInfo) bool { return true }
