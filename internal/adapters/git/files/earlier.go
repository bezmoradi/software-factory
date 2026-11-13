package files

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"syscall"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const maxEarlier = 64 << 10

var earlierPath = regexp.MustCompile(`^earlier/[a-z0-9-]{1,100}\.md$`)

func writeEarlier(dir string, extra []session.SnapshotFile) error {
	if err := os.Mkdir(filepath.Join(dir, "earlier"), 0o755); err != nil {
		return err
	}
	for _, f := range extra {
		if !earlierPath.MatchString(f.Name) || len(f.Text) > maxEarlier {
			return errors.New("an earlier round's file has a name or size the engine doesn't write")
		}
		out, err := os.OpenFile(filepath.Join(dir, filepath.FromSlash(f.Name)), os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o644)
		if err != nil {
			return err
		}
		_, err = out.WriteString(f.Text)
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		if err := os.Chmod(filepath.Join(dir, filepath.FromSlash(f.Name)), 0o644); err != nil {
			return err
		}
	}
	return nil
}
