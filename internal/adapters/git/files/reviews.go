package files

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"syscall"

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

func (v Reviews) Write(repoPath, base, head string, extra []session.SnapshotFile) (session.Snapshot, error) {
	return v.write(func(dir string) error {
		for _, side := range []struct{ name, commit string }{{"base", base}, {"head", head}} {
			if side.commit == "" && side.name == "base" {
				continue
			}
			if err := (Reader{}).WriteTree(repoPath, side.commit, filepath.Join(dir, side.name)); err != nil {
				return fmt.Errorf("%s: %w", side.name, err)
			}
		}
		return nil
	}, func(dir string) (string, bool, error) {
		c, err := with(repoPath, func(r *repo) (madeChange, error) {
			from, err := treeSides(r, base, head)
			if err != nil {
				return madeChange{}, fmt.Errorf("the review's change.diff can't be written: %s", cause(err))
			}
			added, tooLarge, err := changeFiles(dir, from)
			return madeChange{added: added, tooLarge: tooLarge}, err
		})
		return c.added, c.tooLarge, err
	}, func(dir string) (string, error) { return writeCommits(repoPath, dir, base, head) }, extra)
}

type madeChange struct {
	added    string
	tooLarge bool
}

func (v Reviews) WriteMade(base, head []session.SnapshotFile, commits string) (session.Snapshot, error) {
	return v.write(func(dir string) error {
		for _, side := range []struct {
			name  string
			files []session.SnapshotFile
		}{{"base", base}, {"head", head}} {
			if err := writeMade(filepath.Join(dir, side.name), side.files); err != nil {
				return fmt.Errorf("%s: %w", side.name, err)
			}
		}
		return nil
	}, func(dir string) (string, bool, error) {
		return changeFiles(dir, folderSides(dir))
	}, func(dir string) (string, error) {
		return commits, writeNew(filepath.Join(dir, session.CommitsFile), commits)
	}, nil)
}

func writeMade(dest string, files []session.SnapshotFile) error {
	root, err := os.OpenRoot(dest)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	for _, f := range files {
		name := filepath.FromSlash(f.Name)
		if !filepath.IsLocal(name) {
			return unsafe("%s isn't a path inside the snapshot", short(f.Name))
		}
		if d := filepath.Dir(name); d != "." {
			if err := root.MkdirAll(d, 0o755); err != nil {
				return unsafe("%s can't be created: %v", short(d), cause(err))
			}
		}
		file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return unsafe("%s can't be created: %v", short(f.Name), cause(err))
		}
		_, werr := file.WriteString(f.Text)
		if cerr := file.Close(); werr != nil || cerr != nil {
			return unsafe("%s can't be written", short(f.Name))
		}
	}
	return nil
}

func (v Reviews) write(sides func(dir string) error, change func(dir string) (string, bool, error), listCommits func(dir string) (string, error), extra []session.SnapshotFile) (session.Snapshot, error) {
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
	for _, side := range []string{"base", "head"} {
		if err := os.Mkdir(filepath.Join(dir, side), 0o755); err != nil {
			return session.Snapshot{}, fmt.Errorf("the review's %s folder can't be made: %s", side, cause(err))
		}
	}
	if err := sides(dir); err != nil {
		return session.Snapshot{}, err
	}
	added, tooLarge, err := change(dir)
	if err != nil {
		return session.Snapshot{}, err
	}
	commits, err := listCommits(dir)
	if err != nil {
		return session.Snapshot{}, fmt.Errorf("the review's %s can't be written: %s", session.CommitsFile, cause(err))
	}
	folders := []string{dir, filepath.Join(dir, "base"), filepath.Join(dir, "head")}
	if len(extra) > 0 {
		made, err := writeExtra(dir, extra)
		if err != nil {
			return session.Snapshot{}, fmt.Errorf("the review's earlier findings, approved text or the agent's message can't be written: %s", cause(err))
		}
		folders = append(folders, made...)
	}
	for _, p := range folders {
		if err := os.Chmod(p, 0o755); err != nil {
			return session.Snapshot{}, fmt.Errorf("the review's folder can't be made readable: %s", cause(err))
		}
	}
	ok = true
	return session.Snapshot{ID: id, Path: dir, Added: added, TooLarge: tooLarge, Commits: commits}, nil
}

func writeCommits(repoPath, dir, base, head string) (string, error) {
	text, err := with(repoPath, func(r *repo) (string, error) {
		commits, err := newCommits(r, head, base)
		if err != nil {
			return "", err
		}
		unlisted := max(len(commits)-session.MaxListedCommits, 0)
		commits = commits[:len(commits)-unlisted]
		if unlisted > 0 && len(commits)+unlisted > maxScanCommits {
			unlisted = -1
		}
		list := make([]session.PushedCommit, 0, len(commits))
		for _, commit := range slices.Backward(commits) {
			c, _, err := pushedCommit(r, commit.SHA)
			if err != nil {
				return "", err
			}
			list = append(list, session.PushedCommit{SHA: c.SHA, Message: c.Message})
		}
		return session.CommitsText(list, unlisted), nil
	})
	if err != nil {
		return "", err
	}
	return text, writeNew(filepath.Join(dir, session.CommitsFile), text)
}

func writeNew(name, text string) error {
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		return err
	}
	_, err = f.WriteString(text)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Chmod(name, 0o644)
}

func (v Reviews) Remove(id string) error {
	if !snapshotID.MatchString(id) || !filepath.IsAbs(v.Dir) {
		return errors.New("not a review folder")
	}
	return os.RemoveAll(filepath.Join(v.Dir, id))
}
