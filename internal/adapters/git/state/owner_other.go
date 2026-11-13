//go:build !unix

package state

import "os"

func ownedByMe(os.FileInfo) bool { return true }
