package page

import (
	"errors"
	"io"
	"io/fs"
	"os"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

type DocFiles struct {
	Workspace string
	OpenBox   func(workspace, box string) (*os.Root, error)
	Folder    string
}

var _ session.DocFiles = DocFiles{}

func (d DocFiles) box(box string) (*os.Root, error) {
	if d.Workspace == "" || d.OpenBox == nil || !session.ValidBox(box) {
		return nil, errOutside
	}
	root, err := d.OpenBox(d.Workspace, box)
	if err != nil {
		return nil, errMissing
	}
	return root, nil
}

func (d DocFiles) folder() string {
	if d.Folder == "" {
		return session.DocsFolderName
	}
	return d.Folder
}

func (d DocFiles) top(box string) (*os.Root, error) {
	root, err := d.box(box)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return plainRoot(root, d.folder())
}

func (d DocFiles) Make(box, sid string) error {
	if !localName(sid) {
		return errOutside
	}
	root, err := d.box(box)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err := groupFolder(root, d.folder(), shotsTopMode); err != nil {
		return err
	}
	top, err := plainRoot(root, d.folder())
	if err != nil {
		return err
	}
	defer func() { _ = top.Close() }()
	return groupFolder(top, sid, shotsFolderMode)
}

func (d DocFiles) MakeTop(box string) error {
	root, err := d.box(box)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return groupFolder(root, d.folder(), shotsTopMode)
}

func (d DocFiles) Read(box, sid, name string, limit int64) ([]byte, error) {
	if !localName(sid) || !localName(name) {
		return nil, errOutside
	}
	top, err := d.top(box)
	if err != nil {
		return nil, session.ErrDocMissing
	}
	defer func() { _ = top.Close() }()
	folder, err := plainRoot(top, sid)
	if err != nil {
		return nil, session.ErrDocMissing
	}
	defer func() { _ = folder.Close() }()
	seen, err := folder.Lstat(name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, session.ErrDocMissing
	case err != nil:
		return nil, err
	case !seen.Mode().IsRegular():
		return nil, session.ErrDocNotFile
	case seen.Size() > limit:
		return nil, session.ErrDocTooBig
	}
	f, err := folder.OpenFile(name, openFlags, 0)
	if err != nil {
		return nil, session.ErrDocNotFile
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(seen, info) {
		return nil, session.ErrDocNotFile
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, session.ErrDocTooBig
	}
	return data, nil
}

func (d DocFiles) Drop(box, sid string) error {
	if !localName(sid) {
		return errOutside
	}
	top, err := d.top(box)
	if errors.Is(err, errMissing) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = top.Close() }()
	if _, err := top.Lstat(sid); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return top.RemoveAll(sid)
}
