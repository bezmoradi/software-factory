package actions

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	In               = "/in"
	AskpassVariable  = "SF_ACTION_ASKPASS"
	homeDirMode      = 0o700
	askpassMarkValue = "1"
	withdrawPoll     = 100 * time.Millisecond
	withdrawFor      = 2 * time.Minute
)

func readSpec(dir string) (specJSON, error) {
	f, err := os.Open(filepath.Join(dir, SpecFile))
	if err != nil {
		return specJSON{}, errors.New("the run's command isn't in " + dir)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxSpec+1))
	if err != nil || len(data) > maxSpec {
		return specJSON{}, errors.New("the run's command couldn't be read")
	}
	var s specJSON
	if json.Unmarshal(data, &s) != nil || s.Version != SpecVersion || len(s.Argv) == 0 || !filepath.IsAbs(s.Dir) || len(s.Files) > maxFiles || s.Agent != nil && (s.Agent.Key == "" || s.Agent.User == "") {
		return specJSON{}, errors.New("the run's command isn't one this runner reads")
	}
	return s, nil
}

func placeFiles(home string, files []fileJSON) error {
	root, err := os.OpenRoot(home)
	if err != nil {
		return errors.New("the run's home folder couldn't be opened")
	}
	defer func() { _ = root.Close() }()
	for _, x := range files {
		if !homePath(x.Path) || x.Mode&^0o644 != 0 {
			return errors.New("a file of the run isn't one this runner writes")
		}
		if dir := filepath.Dir(x.Path); dir != "." {
			if err := root.MkdirAll(dir, homeDirMode); err != nil {
				return errors.New("a folder for the run's files couldn't be made")
			}
		}
		f, err := root.OpenFile(x.Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, os.FileMode(x.Mode))
		if err != nil {
			return errors.New("a file of the run couldn't be made")
		}
		_, werr := io.WriteString(f, x.Text)
		if cerr := f.Close(); werr != nil || cerr != nil {
			return errors.New("a file of the run couldn't be written")
		}
	}
	return nil
}

func envValue(env []string, key string) string {
	for _, e := range env {
		if k, v, ok := strings.Cut(e, "="); ok && k == key {
			return v
		}
	}
	return ""
}

func program(s specJSON) (string, error) {
	name := s.Argv[0]
	resolved := name
	if !strings.Contains(name, "/") {
		if err := os.Setenv("PATH", envValue(s.Env, "PATH")); err != nil {
			return "", errors.New("the program's search path couldn't be set")
		}
		found, err := exec.LookPath(name)
		if err != nil {
			return "", fmt.Errorf("%s isn't a program in this image", name)
		}
		resolved = found
	} else if !filepath.IsAbs(name) {
		resolved = filepath.Join(s.Dir, name)
	}
	final, err := filepath.EvalSymlinks(resolved)
	if err != nil {
		return "", fmt.Errorf("%s isn't there", name)
	}
	if why := session.RefusedProgram(final); why != "" {
		return "", fmt.Errorf("%s is %s, which an action never runs: %s", name, filepath.Base(final), why)
	}
	for _, dir := range []string{In, session.ActionHome} {
		if final == dir || strings.HasPrefix(final, dir+"/") {
			return "", fmt.Errorf("%s is in %s, where no action program lives", name, dir)
		}
	}
	return resolved, nil
}

func awaitWithdrawn(dir string) error {
	deadline := time.Now().Add(withdrawFor)
	for {
		_, err := os.Lstat(filepath.Join(dir, SpecFile))
		if errors.Is(err, fs.ErrNotExist) {
			break
		}
		if time.Now().After(deadline) {
			return errors.New("the engine didn't take the run's command back from " + dir + ", so the command doesn't run")
		}
		time.Sleep(withdrawPoll)
	}
	left, err := os.ReadDir(dir)
	if err != nil || len(left) > 0 {
		return errors.New(dir + " still holds files the engine wrote, so the command doesn't run")
	}
	return nil
}

func Run(stderr io.Writer) error {
	s, err := readSpec(In)
	if err == nil {
		err = placeFiles(session.ActionHome, s.Files)
	}
	if err == nil && s.Agent != nil {
		err = startAgent(*s.Agent)
	}
	if err == nil {
		if _, err = fmt.Fprintln(stderr, session.ActionReadMark); err == nil {
			err = awaitWithdrawn(In)
		}
	}
	env := s.Env
	if err == nil && s.Askpass {
		self, serr := os.Executable()
		if serr != nil {
			err = errors.New("the runner couldn't find itself to answer ssh's password prompt")
		}
		env = append(env, "SSH_ASKPASS="+self, AskpassVariable+"="+askpassMarkValue)
	}
	if err == nil {
		if cerr := os.Chdir(s.Dir); cerr != nil {
			err = fmt.Errorf("the run's folder %s isn't there", s.Dir)
		}
	}
	var path string
	if err == nil {
		path, err = program(s)
	}
	if err == nil {
		err = syscall.Exec(path, s.Argv, env)
		err = fmt.Errorf("%s couldn't be started: %w", s.Argv[0], err)
	}
	_, _ = fmt.Fprintln(stderr, "software-factory action: "+err.Error())
	return err
}

func Askpassing() bool {
	return os.Getenv(AskpassVariable) == askpassMarkValue
}

func Askpass(out io.Writer) error {
	_, err := fmt.Fprintln(out, os.Getenv(session.SSHPassVariable))
	return err
}
