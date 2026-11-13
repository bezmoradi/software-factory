//go:build !unix

package page

import "os"

const openFlags = os.O_RDONLY
