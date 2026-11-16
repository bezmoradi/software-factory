//go:build !unix

package npm

import "os"

const readFlags = os.O_RDONLY
