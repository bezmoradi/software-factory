package agenthost

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	allowRulesRoute   = "/v1/allow-rules"
	removeRuleRoute   = "/v1/allow-rules/remove"
	localSettings     = ".claude/settings.local.json"
	userSettings      = "settings.json"
	maxGitFile        = 4 << 10
	ruleTempPrefix    = ".sf-rule-"
	worktreesFolder   = "worktrees"
	maxWorktreeListed = 2000
)

type allowRulesJSON struct {
	Repo string `json:"repo"`
}

type ruleFileJSON struct {
	Place    string   `json:"place"`
	Worktree string   `json:"worktree,omitempty"`
	Path     string   `json:"path"`
	Rules    []string `json:"rules"`
	Problem  string   `json:"problem,omitempty"`
}

type allowRulesReply struct {
	Files []ruleFileJSON `json:"files"`
}

type removeRuleJSON struct {
	Repo     string `json:"repo"`
	Place    string `json:"place"`
	Worktree string `json:"worktree,omitempty"`
	Rule     string `json:"rule"`
}

type settingsAt struct {
	place    session.RulePlace
	worktree string
	dir      string
	rel      string
	shown    string
}

var errNotPlain = errors.New("not a plain file")

func homeShown(home, p string) string {
	if rel, err := filepath.Rel(home, p); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		return "~/" + filepath.ToSlash(rel)
	}
	return p
}

func (s *Server) userSettingsAt() (settingsAt, *problem) {
	home := os.Getenv("HOME")
	if !filepath.IsAbs(home) {
		return settingsAt{}, fail(http.StatusConflict, "home", "The container has no usable home folder for Claude Code's settings.")
	}
	dir := filepath.Clean(claudeConfigDir(home))
	return settingsAt{place: session.RuleInUser, dir: dir, rel: userSettings, shown: homeShown(home, filepath.Join(dir, userSettings))}, nil
}

func (s *Server) worktreeOf(repo, name string) bool {
	dir := filepath.Join(s.cfg.Workspace, worktreesFolder, name)
	root, err := openPlainFolder(dir)
	if err != nil {
		return false
	}
	defer func() { _ = root.Close() }()
	data, _, err := readPlain(root, ".git", maxGitFile)
	if err != nil {
		return false
	}
	target, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
	if !ok {
		return false
	}
	target = strings.TrimSpace(target)
	if !filepath.IsAbs(target) {
		target = filepath.Join(dir, target)
	}
	want := filepath.Join(s.cfg.Workspace, "repos", repo, ".git", "worktrees") + string(filepath.Separator)
	return strings.HasPrefix(filepath.Clean(target)+string(filepath.Separator), want) && filepath.Clean(target)+string(filepath.Separator) != want
}

func (s *Server) settingsPlaces(repo string) ([]settingsAt, *problem) {
	clone, p := s.repoPath(repo)
	if p != nil {
		return nil, p
	}
	out := []settingsAt{{place: session.RuleInClone, dir: clone, rel: localSettings, shown: path.Join("repos", repo, localSettings)}}
	trees := filepath.Join(s.cfg.Workspace, worktreesFolder)
	if d, err := openPlainFolder(trees); err == nil {
		entries, _ := readDirCapped(d, ".", maxWorktreeListed)
		_ = d.Close()
		for _, e := range entries {
			name := e.Name()
			if len(out) > session.MaxRuleWorktrees || !e.IsDir() || !session.RuleWorktreeName.MatchString(name) || !s.worktreeOf(repo, name) {
				continue
			}
			out = append(out, settingsAt{place: session.RuleInWorktree, worktree: name, dir: filepath.Join(trees, name), rel: localSettings, shown: path.Join(worktreesFolder, name, localSettings)})
		}
	}
	user, p := s.userSettingsAt()
	if p == nil {
		out = append(out, user)
	}
	return out, nil
}

func openPlainFolder(dir string) (*os.Root, error) {
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errNotPlain
	}
	return os.OpenRoot(dir)
}

func readDirCapped(root *os.Root, name string, limit int) ([]fs.DirEntry, error) {
	d, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = d.Close() }()
	entries, err := d.ReadDir(limit)
	if errors.Is(err, io.EOF) {
		err = nil
	}
	return entries, err
}

func plainPath(root *os.Root, rel string) (os.FileInfo, error) {
	parts := strings.Split(rel, "/")
	for i := range parts {
		info, err := root.Lstat(strings.Join(parts[:i+1], "/"))
		if err != nil {
			return nil, err
		}
		last := i == len(parts)-1
		if !last && !info.IsDir() || last && !info.Mode().IsRegular() {
			return nil, errNotPlain
		}
		if last {
			return info, nil
		}
	}
	return nil, errNotPlain
}

func readPlain(root *os.Root, rel string, limit int64) ([]byte, os.FileInfo, error) {
	info, err := plainPath(root, rel)
	if err != nil {
		return nil, nil, err
	}
	if info.Size() > limit || !oneLink(info) {
		return nil, nil, errNotPlain
	}
	f, err := root.OpenFile(rel, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = f.Close() }()
	open, err := f.Stat()
	if err != nil || !os.SameFile(info, open) || !open.Mode().IsRegular() || !oneLink(open) {
		return nil, nil, errNotPlain
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, nil, errNotPlain
	}
	return data, open, nil
}

func readSettings(at settingsAt) ([]byte, os.FileInfo, bool, string) {
	root, err := openPlainFolder(at.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, false, ""
	}
	if err != nil {
		return nil, nil, true, "The folder isn't a plain folder, so agent-host didn't read it."
	}
	defer func() { _ = root.Close() }()
	data, info, err := readPlain(root, at.rel, session.MaxRuleFileBytes)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil, false, ""
	case err != nil:
		return nil, nil, true, "The file isn't a plain file of at most 1 MiB with one name (a link, a hard link or another kind), so agent-host didn't read it."
	}
	return data, info, true, ""
}

func (s *Server) allowRules(q allowRulesJSON) (allowRulesReply, *problem) {
	places, p := s.settingsPlaces(q.Repo)
	if p != nil {
		return allowRulesReply{}, p
	}
	out := allowRulesReply{Files: []ruleFileJSON{}}
	for _, at := range places {
		data, _, exists, why := readSettings(at)
		if !exists {
			continue
		}
		f := ruleFileJSON{Place: string(at.place), Worktree: at.worktree, Path: at.shown, Rules: []string{}, Problem: why}
		if why == "" {
			rules, err := session.AllowRulesIn(data)
			if err != nil {
				f.Problem = session.RuleProblem(err) + "."
			} else {
				f.Rules = rules
			}
		}
		if len(out.Files) < session.MaxRuleFiles {
			out.Files = append(out.Files, f)
		}
	}
	s.log.Info("allow rules", "files", len(out.Files))
	return out, nil
}

func (s *Server) removeRule(q removeRuleJSON) *problem {
	if !session.ValidRuleText(q.Rule) {
		return fail(http.StatusBadRequest, "rule", "")
	}
	places, p := s.settingsPlaces(q.Repo)
	if p != nil {
		return p
	}
	var at *settingsAt
	for i := range places {
		if string(places[i].place) == q.Place && places[i].worktree == q.Worktree {
			at = &places[i]
		}
	}
	if at == nil {
		return fail(http.StatusNotFound, "file", "agent-host finds no such settings file for that project.")
	}
	s.rulesMu.Lock()
	defer s.rulesMu.Unlock()
	data, info, exists, why := readSettings(*at)
	switch {
	case !exists:
		return fail(http.StatusConflict, "gone", "The settings file is no longer there.")
	case why != "":
		return fail(http.StatusConflict, "file", why)
	case !ownedByMe(info):
		return fail(http.StatusConflict, "owner", "The settings file isn't the agents' user's, so agent-host doesn't change it.")
	}
	next, err := session.RemoveAllowRule(data, q.Rule)
	if err != nil {
		return fail(http.StatusConflict, "shape", "agent-host read the file again and didn't change it: "+session.RuleProblem(err)+".")
	}
	if err := writeSettings(*at, info, data, next); err != nil {
		return fail(http.StatusConflict, "write", "The settings file changed while agent-host wrote it, or couldn't be written, so it was left as it was.")
	}
	s.log.Info("allow rule removed", "place", q.Place)
	return nil
}

func writeSettings(at settingsAt, info os.FileInfo, before, next []byte) error {
	root, err := openPlainFolder(at.dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	dir := path.Dir(at.rel)
	tmp := path.Join(dir, ruleTempPrefix+randomID())
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = root.Remove(tmp) }()
	if _, err := f.Write(next); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Chmod(info.Mode().Perm()); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	again, _, err := readPlain(root, at.rel, session.MaxRuleFileBytes)
	if err != nil || !bytes.Equal(again, before) {
		return errNotPlain
	}
	return root.Rename(tmp, at.rel)
}

func (c *Client) ReadAllowRules(ctx context.Context, repo string) ([]session.SettingsFile, error) {
	var out allowRulesReply
	if err := c.call(ctx, http.MethodPost, allowRulesRoute, allowRulesJSON{Repo: repo}, &out); err != nil {
		return nil, err
	}
	files := make([]session.SettingsFile, 0, len(out.Files))
	for _, f := range out.Files {
		files = append(files, session.SettingsFile{Place: session.RulePlace(f.Place), Worktree: f.Worktree, Path: f.Path, Rules: f.Rules, Problem: f.Problem})
	}
	return files, nil
}

func (c *Client) RemoveAllowRule(ctx context.Context, repo string, place session.RulePlace, worktree, rule string) error {
	return c.call(ctx, http.MethodPost, removeRuleRoute, removeRuleJSON{Repo: repo, Place: string(place), Worktree: worktree, Rule: rule}, nil)
}

var _ session.AllowRulesHost = (*Client)(nil)
