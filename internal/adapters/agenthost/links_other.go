//go:build !unix

package agenthost

import "os"

func oneLink(os.FileInfo) bool { return false }
