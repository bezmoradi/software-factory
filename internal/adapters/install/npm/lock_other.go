//go:build !unix

package npm

import "context"

func lock(context.Context, string) (func(), error) {
	return func() {}, nil
}
