//go:build !unix

package remote

import "os"

func ownedByMe(os.FileInfo) bool { return true }
