package agenthost

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	helpersRoute       = "/v1/helpers"
	helpersRemoveRoute = "/v1/helpers/remove"
	helpersSweepRoute  = "/v1/helpers/sweep"
	maxHelpersListed   = 256
	helperGrace        = 2 * time.Minute
	debrisFolder       = ".claude/worktrees"
)

type helperJSON struct {
	Repo     string        `json:"repo"`
	Name     string        `json:"name"`
	Branch   string        `json:"branch"`
	Base     string        `json:"base,omitempty"`
	Identity *identityJSON `json:"identity,omitempty"`
}

type helperRemoveJSON struct {
	Repo    string `json:"repo"`
	Name    string `json:"name"`
	Session string `json:"session"`
}

type sweepJSON struct {
	Repo    string `json:"repo"`
	Session string `json:"session"`
}

type sweepReply struct {
	Removed []string `json:"removed"`
	Kept    int      `json:"kept"`
	Debris  int      `json:"debris"`
}

var _ session.HelperHost = (*Client)(nil)

func helperBranch(name string) string {
	return session.HelperBranchPrefix + strings.TrimPrefix(name, session.HelperFolderPrefix)
}

func (s *Server) helpersDir() string {
	return filepath.Join(s.cfg.Workspace, session.HelpersFolder)
}

func (s *Server) helperPath(name, branch string) (string, *problem) {
	if !session.HelperFolderName(name) {
		return "", fail(http.StatusBadRequest, "name", "A helper copy's folder is h- and a lower-case name.")
	}
	if branch != helperBranch(name) || !validBranch(branch) {
		return "", fail(http.StatusBadRequest, "branch", "A helper copy's branch is helper/ and its name.")
	}
	return filepath.Join(s.helpersDir(), name), nil
}

func (s *Server) plainHelpers(create bool) (bool, *problem) {
	dir := s.helpersDir()
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		if !create {
			return false, nil
		}
		if err := os.Mkdir(dir, 0o775); err != nil && !errors.Is(err, os.ErrExist) {
			return false, fail(http.StatusInternalServerError, "helpers", "agent-host couldn't create the helpers folder.")
		}
		info, err = os.Lstat(dir)
	}
	if err != nil || !info.IsDir() {
		return false, fail(http.StatusConflict, "helpers", "The helpers folder isn't a plain folder.")
	}
	return true, nil
}

func (s *Server) sessionHead(ctx context.Context, repo, branch string) (string, *problem) {
	if !validBranch(branch) || !strings.HasPrefix(branch, "sf/") {
		return "", fail(http.StatusBadRequest, "branch", "The session's branch is sf/ and its id.")
	}
	head, err := s.git(ctx, repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch+"^{commit}")
	if err != nil || !commitHash.MatchString(head) {
		return "", fail(http.StatusConflict, "branch", "The clone has no session branch by that name.")
	}
	return head, nil
}

func (s *Server) addHelper(ctx context.Context, q helperJSON) (string, *problem) {
	repo, p := s.repoPath(q.Repo)
	if p != nil {
		return "", p
	}
	path, p := s.helperPath(q.Name, q.Branch)
	if p != nil {
		return "", p
	}
	if !commitHash.MatchString(q.Base) {
		return "", fail(http.StatusBadRequest, "base", "A helper copy starts at a full commit hash.")
	}
	id, p := identityIn(q.Identity, true)
	if p != nil {
		return "", p
	}
	s.wtMu.Lock()
	defer s.wtMu.Unlock()
	if _, p := s.plainHelpers(true); p != nil {
		return "", p
	}
	if _, err := os.Lstat(path); err == nil || !errors.Is(err, os.ErrNotExist) {
		return "", fail(http.StatusConflict, "exists", "A helper copy by that name already exists.")
	}
	_, _ = s.git(ctx, repo, "worktree", "prune")
	out, err := s.git(ctx, repo, "worktree", "add", "-b", q.Branch, "--", path, q.Base)
	if err != nil {
		s.log.Warn("agent-host helper", "event", "add-failed")
		return "", fail(http.StatusConflict, "git", gitMessage(out))
	}
	if !pointsBack(repo, path) {
		s.dropHelper(ctx, repo, path, q.Name)
		s.log.Warn("agent-host helper", "event", "link-unread")
		return "", fail(http.StatusConflict, "git", "git made the helper's copy under another admin folder, so agent-host removed it again.")
	}
	if p := s.setIdentity(ctx, repo, path, id); p != nil {
		s.dropHelper(ctx, repo, path, q.Name)
		return "", p
	}
	s.log.Info("agent-host helper", "event", "added")
	return path, nil
}

func (s *Server) dropHelper(ctx context.Context, repo, path, name string) {
	if _, err := s.git(ctx, repo, "worktree", "remove", "--force", "--force", "--", path); err != nil {
		s.log.Warn("agent-host helper", "event", "remove-failed")
	}
	ref := "refs/heads/" + helperBranch(name)
	if tip, err := s.git(ctx, repo, "rev-parse", "--verify", "--quiet", ref); err == nil && commitHash.MatchString(tip) {
		_, _ = s.git(ctx, repo, "update-ref", "-d", ref, tip)
	}
}

func (s *Server) merged(ctx context.Context, repo, tip, head string) bool {
	if tip == head {
		return true
	}
	_, err := s.git(ctx, repo, "merge-base", "--is-ancestor", tip, head)
	return err == nil
}

func (s *Server) dropMergedBranch(ctx context.Context, repo, name, head string) {
	branch := helperBranch(name)
	ref := "refs/heads/" + branch
	tip, err := s.git(ctx, repo, "rev-parse", "--verify", "--quiet", ref)
	if err != nil || !commitHash.MatchString(tip) || !s.merged(ctx, repo, tip, head) {
		return
	}
	if used, sure := checkedOut(repo, branch); used || !sure {
		return
	}
	if _, err := s.git(ctx, repo, "update-ref", "-d", ref, tip); err != nil {
		s.log.Warn("agent-host helper", "event", "branch-kept")
	}
}

func (s *Server) removeHelper(ctx context.Context, q helperRemoveJSON) *problem {
	repo, p := s.repoPath(q.Repo)
	if p != nil {
		return p
	}
	path, p := s.helperPath(q.Name, helperBranch(q.Name))
	if p != nil {
		return p
	}
	s.wtMu.Lock()
	defer s.wtMu.Unlock()
	head, p := s.sessionHead(ctx, repo, q.Session)
	if p != nil {
		return p
	}
	if there, p := s.plainHelpers(false); p != nil || !there {
		if p != nil {
			return p
		}
		return fail(http.StatusNotFound, "helper", "There's no helper copy by that name.")
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		_, _ = s.git(ctx, repo, "worktree", "prune")
		s.dropMergedBranch(ctx, repo, q.Name, head)
		return nil
	}
	tree, p := s.listedTree(ctx, repo, path)
	if p != nil {
		return p
	}
	switch {
	case !tree.listed || !pointsBack(repo, path):
		return fail(http.StatusConflict, "helper", "That folder isn't a helper copy git knows, so agent-host left it alone.")
	case tree.locked:
		return fail(http.StatusConflict, "locked", "That helper copy is locked, so agent-host left it alone.")
	}
	if p := s.cleanTree(ctx, repo, path); p != nil {
		return p
	}
	if out, err := s.git(ctx, repo, "worktree", "remove", "--", path); err != nil {
		if dirtyText.MatchString(out) {
			return fail(http.StatusConflict, session.HostDirty, "The helper copy has changes that aren't committed.")
		}
		s.log.Warn("agent-host helper", "event", "remove-failed")
		return fail(http.StatusConflict, "git", gitMessage(out))
	}
	s.dropMergedBranch(ctx, repo, q.Name, head)
	_, _ = s.git(ctx, repo, "worktree", "prune")
	s.log.Info("agent-host helper", "event", "removed")
	return nil
}

func (s *Server) sweepHelpers(ctx context.Context, q sweepJSON) (sweepReply, *problem) {
	out := sweepReply{Removed: []string{}}
	repo, p := s.repoPath(q.Repo)
	if p != nil {
		return out, p
	}
	s.wtMu.Lock()
	defer s.wtMu.Unlock()
	head, p := s.sessionHead(ctx, repo, q.Session)
	if p != nil {
		return out, p
	}
	there, p := s.plainHelpers(false)
	if p != nil {
		return out, p
	}
	debris := filepath.Join(repo, debrisFolder)
	_, derr := os.Lstat(debris)
	if !there && errors.Is(derr, os.ErrNotExist) {
		return out, nil
	}
	trees, p := s.listedTrees(ctx, repo)
	if p != nil {
		return out, p
	}
	if there {
		root, err := openPlainFolder(s.helpersDir())
		if err != nil {
			return out, fail(http.StatusConflict, "helpers", "The helpers folder isn't a plain folder.")
		}
		entries, err := readDirCapped(root, ".", maxHelpersListed)
		_ = root.Close()
		if err != nil {
			return out, fail(http.StatusConflict, "helpers", "agent-host couldn't read the helpers folder.")
		}
		for _, e := range entries {
			if !session.HelperFolderName(e.Name()) || !e.IsDir() {
				continue
			}
			if s.sweepOne(ctx, repo, head, e.Name(), trees) {
				out.Removed = append(out.Removed, e.Name())
			} else {
				out.Kept++
			}
		}
	}
	out.Debris = s.sweepDebris(ctx, repo, debris, trees)
	_, _ = s.git(ctx, repo, "worktree", "prune")
	return out, nil
}

func (s *Server) sweepOne(ctx context.Context, repo, head, name string, trees map[string]listedTree) bool {
	path := filepath.Join(s.helpersDir(), name)
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || time.Since(info.ModTime()) < helperGrace {
		return false
	}
	tree := trees[canonPath(path)]
	if !tree.listed || tree.locked || !pointsBack(repo, path) || s.cleanTree(ctx, repo, path) != nil {
		return false
	}
	tip, err := s.gitIn(ctx, repo, path, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if err != nil || !commitHash.MatchString(tip) || !s.merged(ctx, repo, tip, head) {
		return false
	}
	if _, err := s.git(ctx, repo, "worktree", "remove", "--", path); err != nil {
		s.log.Warn("agent-host helper", "event", "sweep-kept")
		return false
	}
	s.dropMergedBranch(ctx, repo, name, head)
	return true
}

func (s *Server) sweepDebris(ctx context.Context, repo, debris string, trees map[string]listedTree) int {
	info, err := os.Lstat(debris)
	if err != nil || !info.IsDir() {
		return 0
	}
	under := canonDir(debris) + string(filepath.Separator)
	n := 0
	for path, tree := range trees {
		if !strings.HasPrefix(path, under) || strings.Contains(strings.TrimPrefix(path, under), string(filepath.Separator)) || tree.locked {
			continue
		}
		if !pointsBack(repo, path) || s.cleanTree(ctx, repo, path) != nil {
			continue
		}
		if _, err := s.git(ctx, repo, "worktree", "remove", "--", path); err == nil {
			n++
		}
	}
	return n
}
