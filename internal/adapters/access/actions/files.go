package actions

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	SpecFile    = "spec.json"
	SpecVersion = 1
	maxSpec     = 1 << 20
	maxFiles    = 16
	runDirMode  = 0o750
	specMode    = 0o640
)

var runID = regexp.MustCompile(`^[0-9a-f]{16}$`)

type fileJSON struct {
	Path string `json:"path"`
	Mode uint32 `json:"mode"`
	Text string `json:"text"`
}

type agentJSON struct {
	Key  string `json:"key"`
	User string `json:"user"`
}

type specJSON struct {
	Version int        `json:"version"`
	Argv    []string   `json:"argv"`
	Dir     string     `json:"dir"`
	Env     []string   `json:"env"`
	Files   []fileJSON `json:"files"`
	Askpass bool       `json:"askpass,omitempty"`
	Agent   *agentJSON `json:"agent,omitempty"`
}

type Files struct {
	Dir string
}

var _ session.ActionFiles = Files{}

func (f Files) runDir(run string) (string, error) {
	if !filepath.IsAbs(f.Dir) {
		return "", errors.New("the actions folder isn't an absolute path")
	}
	if !runID.MatchString(run) {
		return "", errors.New("an action run's id is 16 hexadecimal digits")
	}
	return filepath.Join(f.Dir, run), nil
}

func homePath(p string) bool {
	return p != "" && !path.IsAbs(p) && path.Clean(p) == p && !strings.HasPrefix(p, "../") && p != ".."
}

func (f Files) Write(run string, s session.ActionSpec) error {
	dir, err := f.runDir(run)
	if err != nil {
		return err
	}
	if len(s.Argv) == 0 || len(s.Files) > maxFiles || !path.IsAbs(s.Dir) {
		return errors.New("the run's command isn't one the engine writes")
	}
	j := specJSON{Version: SpecVersion, Argv: s.Argv, Dir: s.Dir, Env: s.Env, Files: make([]fileJSON, 0, len(s.Files)), Askpass: s.Askpass}
	if (s.AgentKey == "") != (s.AgentUser == "") {
		return errors.New("the run's SSH key comes without its user")
	}
	if s.AgentKey != "" {
		j.Agent = &agentJSON{Key: s.AgentKey, User: s.AgentUser}
	}
	for _, x := range s.Files {
		if !homePath(x.Path) || x.Mode&^0o644 != 0 {
			return errors.New("a file of the run isn't one the engine writes")
		}
		j.Files = append(j.Files, fileJSON{Path: x.Path, Mode: x.Mode, Text: x.Text})
	}
	data, err := json.Marshal(j)
	if err != nil || len(data) > maxSpec {
		return errors.New("the run's files are larger than the engine writes")
	}
	if err := os.Mkdir(dir, runDirMode); err != nil {
		return fmt.Errorf("the run's folder couldn't be made: %w", errors.Unwrap(err))
	}
	if err := os.Chmod(dir, runDirMode); err != nil {
		_ = os.RemoveAll(dir)
		return errors.New("the run's folder couldn't be given its mode")
	}
	name := filepath.Join(dir, SpecFile)
	out, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, specMode)
	if err != nil {
		_ = os.RemoveAll(dir)
		return errors.New("the run's file couldn't be made")
	}
	_, werr := out.Write(data)
	cerr := out.Close()
	if werr != nil || cerr != nil || os.Chmod(name, specMode) != nil {
		_ = os.RemoveAll(dir)
		return errors.New("the run's file couldn't be written")
	}
	return nil
}

func (f Files) Withdraw(run string) error {
	dir, err := f.runDir(run)
	if err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(dir, SpecFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return errors.New("the run's file couldn't be removed")
	}
	return nil
}

func (f Files) Remove(run string) error {
	dir, err := f.runDir(run)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return errors.New("the run's folder couldn't be removed")
	}
	return nil
}

func (f Files) Prune(keep func(run string) bool) error {
	if !filepath.IsAbs(f.Dir) {
		return errors.New("the actions folder isn't an absolute path")
	}
	entries, err := os.ReadDir(f.Dir)
	if err != nil {
		return errors.New("the actions folder couldn't be read")
	}
	var failed []string
	for _, e := range entries {
		if !runID.MatchString(e.Name()) || keep(e.Name()) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(f.Dir, e.Name())); err != nil {
			failed = append(failed, e.Name())
		}
	}
	if len(failed) > 0 {
		slices.Sort(failed)
		return errors.New("some runs' folders couldn't be removed: " + strings.Join(failed, ", "))
	}
	return nil
}
