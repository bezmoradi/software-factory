package files

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"

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
		if err := writeNew(filepath.Join(dir, filepath.FromSlash(f.Name)), f.Text); err != nil {
			return err
		}
	}
	return nil
}
