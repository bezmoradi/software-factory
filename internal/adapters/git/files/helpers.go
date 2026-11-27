package files

import (
	"errors"
	"io"
	"io/fs"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const maxHelpersRead = 1024

func HelperCount(workspace, box string) (int, error) {
	root, err := OpenBox(workspace, box)
	if err != nil {
		return 0, err
	}
	defer func() { _ = root.Close() }()
	helpers, _, err := stepInto(root, session.HelpersFolder)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer func() { _ = helpers.Close() }()
	dir, err := helpers.Open(".")
	if err != nil {
		return 0, err
	}
	defer func() { _ = dir.Close() }()
	entries, err := dir.ReadDir(maxHelpersRead)
	if err != nil && !errors.Is(err, io.EOF) {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), session.HelperFolderPrefix) {
			n++
		}
	}
	if len(entries) == maxHelpersRead {
		n = max(n, session.MaxHelperCopies)
	}
	return n, nil
}
