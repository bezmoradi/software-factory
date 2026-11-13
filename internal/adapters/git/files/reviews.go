package files

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/bezmoradi/software-factory/internal/core/push"
	"github.com/bezmoradi/software-factory/internal/core/session"
)

var snapshotID = regexp.MustCompile(`^[0-9a-f]{16}$`)

type Reviews struct {
	Dir string
}

var _ session.Snapshots = Reviews{}

func (v Reviews) Head(repo, branch string) (string, error) {
	if !push.ValidBranch(branch) {
		return "", fmt.Errorf("%w: %q isn't a branch name the engine reads", push.ErrNotFound, short(branch))
	}
	sha, err := Reader{}.ResolveRef(repo, "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	if _, err := (Reader{}).Commit(repo, sha); err != nil {
		return "", err
	}
	return sha, nil
}

func (v Reviews) Write(repo, base, head string, extra []session.SnapshotFile) (session.Snapshot, error) {
	if !filepath.IsAbs(v.Dir) {
		return session.Snapshot{}, errors.New("the reviews folder isn't an absolute path")
	}
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	id := hex.EncodeToString(b)
	dir := filepath.Join(v.Dir, id)
	if err := os.Mkdir(dir, 0o755); err != nil {
		return session.Snapshot{}, fmt.Errorf("the review's folder can't be made: %s", cause(err))
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(dir)
		}
	}()
	for _, side := range []struct{ name, commit string }{{"base", base}, {"head", head}} {
		dest := filepath.Join(dir, side.name)
		if err := os.Mkdir(dest, 0o755); err != nil {
			return session.Snapshot{}, fmt.Errorf("the review's %s folder can't be made: %s", side.name, cause(err))
		}
		if side.commit == "" && side.name == "base" {
			continue
		}
		if err := (Reader{}).WriteTree(repo, side.commit, dest); err != nil {
			return session.Snapshot{}, fmt.Errorf("%s: %w", side.name, err)
		}
	}
	if err := writeChange(dir); err != nil {
		return session.Snapshot{}, fmt.Errorf("the review's change.diff can't be written: %s", cause(err))
	}
	folders := []string{dir, filepath.Join(dir, "base"), filepath.Join(dir, "head")}
	if len(extra) > 0 {
		if err := writeEarlier(dir, extra); err != nil {
			return session.Snapshot{}, fmt.Errorf("the review's earlier findings can't be written: %s", cause(err))
		}
		folders = append(folders, filepath.Join(dir, "earlier"))
	}
	for _, p := range folders {
		if err := os.Chmod(p, 0o755); err != nil {
			return session.Snapshot{}, fmt.Errorf("the review's folder can't be made readable: %s", cause(err))
		}
	}
	ok = true
	return session.Snapshot{ID: id, Path: dir}, nil
}

func (v Reviews) Remove(id string) error {
	if !snapshotID.MatchString(id) || !filepath.IsAbs(v.Dir) {
		return errors.New("not a review folder")
	}
	return os.RemoveAll(filepath.Join(v.Dir, id))
}
