//go:build !unix

package remote

import (
	"errors"
	"os"
)

const noFollow = 0

func linkAt(*os.File, *os.File, string) error {
	return errors.New("hard links aren't made on this system")
}
