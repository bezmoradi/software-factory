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
)

const gitTimeout = 60 * time.Second

var (
	repoName   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
	treeName   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	branchName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,99}$`)
	commitHash = regexp.MustCompile(`^[0-9a-f]{40}$`)
	dirtyText  = regexp.MustCompile(`(?i)contains modified or untracked files|is dirty|use --force`)
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
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	full := append([]string{"-c", "safe.directory=" + repo, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-C", repo}, args...)
	cmd := exec.CommandContext(ctx, s.cfg.Git, full...)
	cmd.Env = Environment(s.cfg.Environ, nil, map[string]string{"GIT_TERMINAL_PROMPT": "0"})
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
	if !commitHash.MatchString(q.Base) {
		return "", fail(http.StatusBadRequest, "base", "")
	}
	s.wtMu.Lock()
	defer s.wtMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fail(http.StatusInternalServerError, "worktree", "agent-host couldn't create the worktrees folder.")
	}
	if _, err := os.Lstat(path); err == nil || !errors.Is(err, os.ErrNotExist) {
		return "", fail(http.StatusConflict, "exists", "A worktree by that name already exists.")
	}
	out, err := s.git(ctx, repo, "worktree", "add", "-b", q.Branch, "--", path, q.Base)
	if err != nil {
		s.log.Warn("agent-host worktree", "event", "add-failed")
		return "", fail(http.StatusConflict, "git", gitMessage(out))
	}
	s.log.Info("agent-host worktree", "event", "added")
	return path, nil
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
