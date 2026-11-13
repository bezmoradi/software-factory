//go:build !unix

package agenthost

import "os"

func ownedByMe(os.FileInfo) bool { return true }
