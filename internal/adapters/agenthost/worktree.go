package agenthost

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	gitTimeout    = 60 * time.Second
	identityRoute = "/v1/worktrees/identity"
)

var (
	repoName   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
	treeName   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	branchName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,99}$`)
	commitHash = regexp.MustCompile(`^[0-9a-f]{40}$`)
	dirtyText  = regexp.MustCompile(`(?i)contains modified or untracked files|is dirty|use --force`)
	usedText   = regexp.MustCompile(`(?i)is already (checked out|used by worktree)`)
)

func validBranch(b string) bool {
	return branchName.MatchString(b) && !strings.Contains(b, "..") && !strings.Contains(b, "//") &&
		!strings.HasSuffix(b, "/") && !strings.HasSuffix(b, ".lock") && !strings.Contains(b, "/.") && !strings.HasSuffix(b, ".")
}

func (s *Server) repoPath(name string) (string, *problem) {
	if !repoName.MatchString(name) || strings.Contains(name, "..") {
		return "", fail(http.StatusBadRequest, "repo", "")
	}
	p := filepath.Join(s.cfg.Workspace, "repos", name)
	info, err := os.Lstat(p)
	if err != nil || !info.IsDir() {
		return "", fail(http.StatusNotFound, "repo", "There's no repository by that name in the workspace.")
	}
	return p, nil
}

func (s *Server) treePath(name string) (string, *problem) {
	if !treeName.MatchString(name) || strings.Contains(name, "..") {
		return "", fail(http.StatusBadRequest, "name", "")
	}
	return filepath.Join(s.cfg.Workspace, "worktrees", name), nil
}

func (s *Server) git(ctx context.Context, repo string, args ...string) (string, error) {
	return s.gitIn(ctx, repo, repo, args...)
}

func (s *Server) gitIn(ctx context.Context, repo, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	safe := []string{"-c", "safe.directory=" + repo}
	if dir != repo {
		safe = append(safe, "-c", "safe.directory="+dir)
	}
	full := append(append(safe, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-C", dir), args...)
	cmd := exec.CommandContext(ctx, s.cfg.Git, full...)
	cmd.Env = Environment(s.cfg.Environ, nil, map[string]string{"GIT_TERMINAL_PROMPT": "0", "GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1"})
	cmd.Dir = s.cfg.Workspace
	ownGroup(cmd)
	cmd.Cancel = func() error { return signalGroup(cmd.Process.Pid, true) }
	cmd.WaitDelay = drainWait
	out := &capped{limit: 8 << 10}
	cmd.Stdout, cmd.Stderr = out, out
	err := cmd.Run()
	return strings.TrimSpace(string(out.buf)), err
}

func gitMessage(out string) string {
	for l := range strings.SplitSeq(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			if len(l) > 300 {
				l = l[:300]
			}
			return l
		}
	}
	return "git failed."
}

func (s *Server) addWorktree(ctx context.Context, q worktreeJSON) (string, *problem) {
	repo, p := s.repoPath(q.Repo)
	if p != nil {
		return "", p
	}
	path, p := s.treePath(q.Name)
	if p != nil {
		return "", p
	}
	if !validBranch(q.Branch) {
		return "", fail(http.StatusBadRequest, "branch", "")
	}
	if q.Existing && q.Base != "" || !q.Existing && !commitHash.MatchString(q.Base) {
		return "", fail(http.StatusBadRequest, "base", "")
	}
	id, p := identityIn(q.Identity, false)
	if p != nil {
		return "", p
	}
	s.wtMu.Lock()
	defer s.wtMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fail(http.StatusInternalServerError, "worktree", "agent-host couldn't create the worktrees folder.")
	}
	if _, err := os.Lstat(path); err == nil || !errors.Is(err, os.ErrNotExist) {
		return "", fail(http.StatusConflict, "exists", "A worktree by that name already exists.")
	}
	args := []string{"worktree", "add", "-b", q.Branch, "--", path, q.Base}
	if q.Existing {
		if p := s.existingBranch(ctx, repo, q.Branch); p != nil {
			return "", p
		}
		args = []string{"worktree", "add", "--", path, q.Branch}
	}
	out, err := s.git(ctx, repo, args...)
	if err != nil {
		s.log.Warn("agent-host worktree", "event", "add-failed")
		if q.Existing && usedText.MatchString(out) {
			return "", fail(http.StatusConflict, "checked-out", "That branch is checked out in another worktree.")
		}
		return "", fail(http.StatusConflict, "git", gitMessage(out))
	}
	if id.Set() {
		if p := s.setIdentity(ctx, repo, path, id); p != nil {
			if _, err := s.git(ctx, repo, "worktree", "remove", "--force", "--force", "--", path); err != nil {
				s.log.Warn("agent-host worktree", "event", "remove-failed")
			}
			return "", p
		}
	}
	s.log.Info("agent-host worktree", "event", "added", "identity", id.Set(), "existing", q.Existing)
	return path, nil
}

func (s *Server) existingBranch(ctx context.Context, repo, branch string) *problem {
	if out, err := s.git(ctx, repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch+"^{commit}"); err != nil || !commitHash.MatchString(out) {
		return fail(http.StatusNotFound, "branch", "The clone has no branch by that name.")
	}
	used, sure := checkedOut(repo, branch)
	if used || !sure {
		return fail(http.StatusConflict, "checked-out", "That branch is checked out in another worktree.")
	}
	return nil
}

func checkedOut(repo, branch string) (bool, bool) {
	root, err := openPlainFolder(filepath.Join(repo, ".git"))
	if err != nil {
		return false, false
	}
	defer func() { _ = root.Close() }()
	want := "ref: refs/heads/" + branch
	heads := []string{"HEAD"}
	entries, err := readDirCapped(root, worktreesFolder, maxWorktreeListed+1)
	if err != nil && !errors.Is(err, os.ErrNotExist) || len(entries) > maxWorktreeListed {
		return false, false
	}
	for _, e := range entries {
		if e.IsDir() {
			heads = append(heads, worktreesFolder+"/"+e.Name()+"/HEAD")
		}
	}
	for _, h := range heads {
		data, _, err := readPlain(root, h, maxGitFile)
		if err == nil && strings.TrimSpace(string(data)) == want {
			return true, true
		}
	}
	return false, true
}

func identityIn(q *identityJSON, required bool) (session.CommitIdentity, *problem) {
	if q == nil {
		if required {
			return session.CommitIdentity{}, fail(http.StatusBadRequest, "identity", "Give the name and email to commit with.")
		}
		return session.CommitIdentity{}, nil
	}
	id, err := session.CheckIdentity(q.Name, q.Email)
	if err != nil {
		msg := ""
		if e, ok := errors.AsType[*session.Error](err); ok {
			msg = e.Message
		}
		return session.CommitIdentity{}, fail(http.StatusBadRequest, "identity", msg)
	}
	return id, nil
}

func (s *Server) setIdentity(ctx context.Context, repo, path string, id session.CommitIdentity) *problem {
	if on, err := s.git(ctx, repo, "config", "--bool", "--get", "extensions.worktreeConfig"); err != nil || on != "true" {
		if out, err := s.git(ctx, repo, "config", "extensions.worktreeConfig", "true"); err != nil {
			s.log.Warn("agent-host worktree", "event", "identity-failed")
			return fail(http.StatusConflict, "git", gitMessage(out))
		}
	}
	for _, kv := range [][2]string{{"user.name", id.Name}, {"user.email", id.Email}} {
		if out, err := s.gitIn(ctx, repo, path, "config", "--worktree", kv[0], kv[1]); err != nil {
			s.log.Warn("agent-host worktree", "event", "identity-failed")
			return fail(http.StatusConflict, "git", gitMessage(out))
		}
	}
	return nil
}

func (s *Server) worktreeIdentity(ctx context.Context, q worktreeJSON) *problem {
	repo, p := s.repoPath(q.Repo)
	if p != nil {
		return p
	}
	path, p := s.treePath(q.Name)
	if p != nil {
		return p
	}
	if q.Branch != "" || q.Base != "" || q.Force || q.Existing {
		return fail(http.StatusBadRequest, "fields", "")
	}
	id, p := identityIn(q.Identity, true)
	if p != nil {
		return p
	}
	s.wtMu.Lock()
	defer s.wtMu.Unlock()
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		return fail(http.StatusNotFound, "worktree", "The session's worktree isn't in the workspace any more.")
	}
	if p := s.setIdentity(ctx, repo, path, id); p != nil {
		return p
	}
	s.log.Info("agent-host worktree", "event", "identity-set")
	return nil
}

func (s *Server) removeWorktree(ctx context.Context, q worktreeJSON) *problem {
	repo, p := s.repoPath(q.Repo)
	if p != nil {
		return p
	}
	path, p := s.treePath(q.Name)
	if p != nil {
		return p
	}
	s.wtMu.Lock()
	defer s.wtMu.Unlock()
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		_, _ = s.git(ctx, repo, "worktree", "prune")
		return nil
	}
	args := []string{"worktree", "remove"}
	if q.Force {
		args = append(args, "--force", "--force")
	}
	out, err := s.git(ctx, repo, append(args, "--", path)...)
	if err != nil {
		if !q.Force && dirtyText.MatchString(out) {
			return fail(http.StatusConflict, "dirty", "The worktree has changes that aren't committed.")
		}
		s.log.Warn("agent-host worktree", "event", "remove-failed")
		return fail(http.StatusConflict, "git", gitMessage(out))
	}
	_, _ = s.git(ctx, repo, "worktree", "prune")
	s.log.Info("agent-host worktree", "event", "removed")
	return nil
}
