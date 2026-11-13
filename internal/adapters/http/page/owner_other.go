//go:build !unix

package page

import "os"

func ownedByMe(os.FileInfo) bool { return true }
