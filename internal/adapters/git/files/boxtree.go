package files

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"regexp"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

var boxTree = regexp.MustCompile(`^(/.*?)/` + session.BoxesFolder + `/([0-9a-f]{16})(/.*)?$`)

var errBoxTree = errors.New("a session's folder in the workspace isn't one the engine opens")

func BoxPath(p string) (workspace, box, rest string, ok bool) {
	if !path.IsAbs(p) || path.Clean(p) != p || strings.ContainsRune(p, 0) {
		return "", "", "", false
	}
	m := boxTree.FindStringSubmatch(p)
	if m == nil || m[1] == "" {
		return "", "", "", false
	}
	return m[1], m[2], strings.TrimPrefix(m[3], "/"), true
}

func stepInto(parent *os.Root, name string) (*os.Root, fs.FileInfo, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
		return nil, nil, errBoxTree
	}
	seen, err := parent.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if !seen.IsDir() || seen.Mode()&fs.ModeSymlink != 0 {
		return nil, nil, fmt.Errorf("%s isn't a folder: a link or a file stands in its place", name)
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !opened.IsDir() || !os.SameFile(seen, opened) {
		_ = root.Close()
		return nil, nil, fmt.Errorf("%s changed while it was opened", name)
	}
	return root, opened, nil
}

func OpenBoxes(workspace string) (*os.Root, error) {
	if !path.IsAbs(workspace) || path.Clean(workspace) != workspace {
		return nil, errBoxTree
	}
	if _, _, _, inside := BoxPath(workspace); inside {
		return nil, errBoxTree
	}
	ws, err := os.OpenRoot(workspace)
	if err != nil {
		return nil, err
	}
	defer func() { _ = ws.Close() }()
	s, info, err := stepInto(ws, session.BoxesFolder)
	if err != nil {
		return nil, err
	}
	if !ownedByMe(info) || info.Mode().Perm()&0o022 != 0 {
		_ = s.Close()
		return nil, errors.New("the workspace's folder of sessions isn't the engine's own, unwritable by others")
	}
	return s, nil
}

func OpenBox(workspace, box string) (*os.Root, error) {
	if !session.ValidBox(box) {
		return nil, errBoxTree
	}
	s, err := OpenBoxes(workspace)
	if err != nil {
		return nil, err
	}
	defer func() { _ = s.Close() }()
	root, info, err := stepInto(s, box)
	if err != nil {
		return nil, err
	}
	if !ownedByMe(info) {
		_ = root.Close()
		return nil, errors.New("the session's folder isn't the engine's own")
	}
	return root, nil
}

func OpenTree(p string) (*os.Root, error) {
	workspace, box, rest, ok := BoxPath(p)
	if !ok {
		return nil, errBoxTree
	}
	root, err := OpenBox(workspace, box)
	if err != nil {
		return nil, err
	}
	if rest == "" {
		return root, nil
	}
	for part := range strings.SplitSeq(rest, "/") {
		next, _, err := stepInto(root, part)
		_ = root.Close()
		if err != nil {
			return nil, err
		}
		root = next
	}
	return root, nil
}

func openFolder(p string) (*os.Root, error) {
	if _, _, _, ok := BoxPath(p); ok {
		return OpenTree(p)
	}
	return openRealFolder(p)
}

const (
	boxesMode = 0o755
	boxMode   = 0o775 | os.ModeSetgid
)

func PrepareBoxes(workspace string) (string, error) {
	if !path.IsAbs(workspace) || path.Clean(workspace) != workspace {
		return "", errBoxTree
	}
	ws, err := os.OpenRoot(workspace)
	if err != nil {
		return "", fmt.Errorf("the workspace %s can't be opened: %w", workspace, err)
	}
	defer func() { _ = ws.Close() }()
	moved := ""
	info, err := ws.Lstat(session.BoxesFolder)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return "", fmt.Errorf("the workspace's folder of sessions can't be read: %w", err)
	case !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 || !ownedByMe(info) || info.Mode().Perm()&0o022 != 0:
		moved = ".s-refused-" + randomName()
		if err := ws.Rename(session.BoxesFolder, moved); err != nil {
			return "", errors.New("the workspace's folder of sessions isn't the engine's own, and it can't be moved aside")
		}
	}
	if err := ws.Mkdir(session.BoxesFolder, boxesMode); err != nil && !errors.Is(err, fs.ErrExist) {
		return moved, fmt.Errorf("the workspace's folder of sessions can't be made: %w", err)
	}
	if err := ws.Chmod(session.BoxesFolder, boxesMode); err != nil {
		return moved, fmt.Errorf("the workspace's folder of sessions can't be made the engine's alone: %w", err)
	}
	s, err := OpenBoxes(workspace)
	if err != nil {
		return moved, err
	}
	return moved, s.Close()
}

func randomName() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func MakeBox(workspace, box string) error {
	if !session.ValidBox(box) {
		return errBoxTree
	}
	s, err := OpenBoxes(workspace)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()
	if err := s.Mkdir(box, 0o775); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	root, info, err := stepInto(s, box)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if !ownedByMe(info) {
		return errors.New("the session's folder isn't the engine's own")
	}
	if info.Mode()&(fs.ModePerm|os.ModeSetgid) != boxMode {
		return s.Chmod(box, boxMode)
	}
	return nil
}

func RemoveBox(workspace, box string) error {
	if !session.ValidBox(box) {
		return errBoxTree
	}
	s, err := OpenBoxes(workspace)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()
	if _, err := s.Lstat(box); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return s.RemoveAll(box)
}
