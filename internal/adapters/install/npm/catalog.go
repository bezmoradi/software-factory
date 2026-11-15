package npm

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

var (
	errNotRegular = errors.New("isn't a regular file")
	errLinked     = errors.New("goes through a symbolic link")
)

func (p *Plugins) Catalog(id, rel string, limit int64) session.CatalogFile {
	pl, ok := p.find(id)
	if !ok || !p.Installed(id) {
		return session.CatalogFile{Pending: true}
	}
	data, err := readInside(filepath.Join(p.folder(pl), pl.Commit), rel, limit)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return session.CatalogFile{Missing: true}
	case err != nil:
		return session.CatalogFile{Unread: rel + " in " + pl.Name + " " + pl.Version + " " + err.Error()}
	}
	return session.CatalogFile{Data: data}
}

func readInside(dir, rel string, limit int64) ([]byte, error) {
	clean := path.Clean(rel)
	if clean != rel || !filepath.IsLocal(rel) {
		return nil, errors.New("isn't a plain path inside the folder")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, errors.New("couldn't be opened: the folder isn't readable")
	}
	defer func() { _ = root.Close() }()
	parts := strings.Split(rel, "/")
	var seen fs.FileInfo
	for i := range parts {
		info, err := root.Lstat(strings.Join(parts[:i+1], "/"))
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fs.ErrNotExist
		}
		if err != nil {
			return nil, errors.New("couldn't be read")
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return nil, errLinked
		}
		seen = info
	}
	if !seen.Mode().IsRegular() {
		return nil, errNotRegular
	}
	tooLarge := errors.New("is over " + strconv.FormatInt(limit>>10, 10) + " KiB")
	if seen.Size() > limit {
		return nil, tooLarge
	}
	f, err := root.OpenFile(rel, readFlags, 0)
	if err != nil {
		return nil, errors.New("couldn't be opened")
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(seen, info) {
		return nil, errors.New("changed while it was read")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, errors.New("couldn't be read")
	}
	if int64(len(data)) > limit {
		return nil, tooLarge
	}
	return data, nil
}
