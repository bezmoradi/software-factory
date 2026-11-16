//go:build !unix

package keep

import "os"

func ownedByMe(os.FileInfo) bool { return true }
