package files

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	VerifyFilesDir    = "files"
	VerifyCommandFile = "command"
	verifyReserve     = 1 << 30
	verifyLeastRoom   = 64 << 20
	verifyMaxEntries  = 200_000
	inodeReserve      = 65_536
	inodeLeast        = 1_000
	leastBlock        = 512
	mostBlock         = 1 << 20
)

type Verify struct {
	Dir    string
	Volume func(dir string) (free, block, inodes, files uint64, err error)
}

type verifyRoom struct {
	bytes   int64
	block   int64
	entries int
}

func (v Verify) room() (verifyRoom, error) {
	if v.Volume == nil {
		return verifyRoom{bytes: maxSnapshotBytes, block: diskBlock, entries: verifyMaxEntries}, nil
	}
	free, block, inodes, files, err := v.Volume(v.Dir)
	if err != nil {
		return verifyRoom{}, fmt.Errorf("the verify volume's free space can't be read, so the engine writes no files there: %s", cause(err))
	}
	if free < verifyReserve+verifyLeastRoom {
		return verifyRoom{}, fmt.Errorf("not enough space on the verify volume: %d MiB free, and the engine keeps %d MiB free there", free>>20, verifyReserve>>20)
	}
	if block < leastBlock || block > mostBlock {
		block = diskBlock
	}
	entries := uint64(verifyMaxEntries)
	if files > 0 {
		reserve := max(uint64(inodeReserve), inodes/10)
		if inodes < reserve+inodeLeast {
			return verifyRoom{}, fmt.Errorf("not enough space on the verify volume: %d free inodes, and the engine keeps %d free there", inodes, reserve)
		}
		entries = min(entries, (inodes-reserve)/session.MaxVerifyRuns)
	}
	return verifyRoom{bytes: int64((free - verifyReserve) / session.MaxVerifyRuns), block: int64(block), entries: int(entries)}, nil
}

var _ session.VerifyTrees = Verify{}

func (v Verify) runDir(run string) (string, error) {
	if !filepath.IsAbs(v.Dir) {
		return "", errors.New("the verification folder isn't an absolute path")
	}
	if !snapshotID.MatchString(run) {
		return "", errors.New("a verification run's id is 16 hexadecimal digits")
	}
	return filepath.Join(v.Dir, run), nil
}

func (v Verify) Write(repo, commit, run, command string) error {
	dir, err := v.runDir(run)
	if err != nil {
		return err
	}
	if _, ok := session.CheckProjectCommand(command); !ok || command == "" {
		return errors.New("the check command isn't one line the engine runs")
	}
	limit, err := v.room()
	if err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		return fmt.Errorf("the run's folder can't be made: %s", cause(err))
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(dir)
		}
	}()
	tree := filepath.Join(dir, VerifyFilesDir)
	if err := os.Mkdir(tree, 0o755); err != nil {
		return fmt.Errorf("the run's files folder can't be made: %s", cause(err))
	}
	if err := (Reader{}).WriteTreeBounded(repo, commit, tree, limit.bytes, limit.block, limit.entries); err != nil {
		return err
	}
	if err := writeNew(filepath.Join(dir, VerifyCommandFile), command); err != nil {
		return fmt.Errorf("the run's command can't be written: %s", cause(err))
	}
	for _, p := range []string{dir, tree} {
		if err := os.Chmod(p, 0o755); err != nil {
			return fmt.Errorf("the run's folder can't be made readable: %s", cause(err))
		}
	}
	if err := os.Chmod(filepath.Join(dir, VerifyCommandFile), 0o644); err != nil {
		return fmt.Errorf("the run's command can't be made readable: %s", cause(err))
	}
	ok = true
	return nil
}

func (v Verify) Remove(run string) error {
	dir, err := v.runDir(run)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

func (v Verify) Prune(keep func(run string) bool) error {
	if !filepath.IsAbs(v.Dir) {
		return errors.New("the verification folder isn't an absolute path")
	}
	entries, err := os.ReadDir(v.Dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var failed error
	for _, e := range entries {
		name := e.Name()
		if !snapshotID.MatchString(name) || keep(name) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(v.Dir, name)); err != nil {
			failed = errors.Join(failed, err)
		}
	}
	return failed
}
