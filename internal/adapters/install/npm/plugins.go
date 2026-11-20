package npm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/push"
	"github.com/bezmoradi/software-factory/internal/core/review"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	PluginManifest = ".claude-plugin/plugin.json"
	PluginsFolder  = "plugins"
	currentLink    = "current"
	pluginFor      = 10 * time.Minute
	headFor        = 30 * time.Second
	maxManifest    = 64 << 10
	maxShownName   = 64
)

var (
	pluginID    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	commitHash  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	inertKeys   = []string{"name", "version", "description", "author", "homepage", "repository", "license", "keywords", "engines"}
	serverFiles = []string{".mcp.json", ".lsp.json", "monitors", "workflows", "settings.json"}
	versionForm = regexp.MustCompile(`^[0-9]{1,9}\.[0-9]{1,9}\.[0-9]{1,9}([-+][0-9A-Za-z.+-]{1,64})?$`)
)

type CommitFetcher interface {
	BranchHead(ctx context.Context, url, branch string) (string, error)
	FetchCommit(ctx context.Context, url, commit, dir string) error
}

type TreeReader interface {
	Commit(repo, sha string) (push.Commit, error)
	ReadFile(repo, commit, path string) (push.File, error)
	ListTree(repo, commit, dir string) ([]session.TreeEntry, error)
	WriteTree(repo, commit, dest string) error
}

type PluginConfig struct {
	Dir     string
	SeenAs  string
	Plugins []Tracked
	Fetch   CommitFetcher
	Trees   TreeReader
	Logger  shared.Logger
	Now     func() time.Time
}

type InstalledPlugin struct {
	Commit  string
	Version string
	Path    string
	At      time.Time
	Refused bool
	Failing string
}

type Check struct {
	At      time.Time
	Head    string
	From    string
	Result  string
	Problem string
}

type Plugins struct {
	cfg     PluginConfig
	mu      sync.Mutex
	running map[string]bool
	checks  map[string]Check
	failing map[string]string
}

func NewPlugins(cfg PluginConfig) (*Plugins, error) {
	if !filepath.IsAbs(cfg.Dir) {
		return nil, errors.New("plugin installs: the agents folder must be an absolute path")
	}
	cfg.Dir = filepath.Clean(cfg.Dir)
	if cfg.SeenAs == "" {
		cfg.SeenAs = cfg.Dir
	}
	if !filepath.IsAbs(cfg.SeenAs) {
		return nil, errors.New("plugin installs: the reviewer's agents folder must be an absolute path")
	}
	if cfg.Plugins == nil {
		cfg.Plugins = []Tracked{Knackpack}
	}
	for _, p := range cfg.Plugins {
		if !pluginID.MatchString(p.ID) || p.Name == "" || p.URL == "" || !push.ValidBranch(p.Branch) || !versionForm.MatchString(p.Floor) {
			return nil, fmt.Errorf("plugin installs: %q needs a plain id, a name, a URL, a branch and a floor version", p.ID)
		}
		for _, c := range p.Refused {
			if !commitHash.MatchString(c) {
				return nil, fmt.Errorf("plugin installs: %q refuses %q, which isn't a full commit hash", p.ID, c)
			}
		}
	}
	if cfg.Fetch == nil || cfg.Trees == nil {
		return nil, errors.New("plugin installs: need a git fetcher and the hash-checked reader")
	}
	if cfg.Logger == nil {
		cfg.Logger = quiet{}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Plugins{cfg: cfg, running: map[string]bool{}, checks: map[string]Check{}, failing: map[string]string{}}, nil
}

func (p *Plugins) List() []Tracked {
	return slices.Clone(p.cfg.Plugins)
}

func (p *Plugins) find(id string) (Tracked, bool) {
	i := slices.IndexFunc(p.cfg.Plugins, func(x Tracked) bool { return x.ID == id })
	if i < 0 {
		return Tracked{}, false
	}
	return p.cfg.Plugins[i], true
}

func (p *Plugins) folder(pl Tracked) string {
	return filepath.Join(p.cfg.Dir, PluginsFolder, pl.ID)
}

func (p *Plugins) installedCommit(pl Tracked) (string, bool) {
	link, err := os.Readlink(filepath.Join(p.folder(pl), currentLink))
	if err != nil || !commitHash.MatchString(link) {
		return "", false
	}
	info, err := os.Lstat(filepath.Join(p.folder(pl), link, filepath.FromSlash(PluginManifest)))
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return link, true
}

func (p *Plugins) Installed(id string) bool {
	pl, ok := p.find(id)
	if !ok {
		return false
	}
	_, ok = p.installedCommit(pl)
	return ok
}

func (p *Plugins) Current(id string) (InstalledPlugin, bool) {
	pl, ok := p.find(id)
	if !ok {
		return InstalledPlugin{}, false
	}
	commit, ok := p.installedCommit(pl)
	if !ok {
		return InstalledPlugin{}, false
	}
	out := InstalledPlugin{Commit: commit, Path: filepath.Join(p.cfg.SeenAs, PluginsFolder, pl.ID, commit), Refused: p.unusable(pl, commit)}
	if why := p.failed(commit); why != "" {
		out.Failing = sentence(why)
	}
	if data, err := readInside(filepath.Join(p.folder(pl), commit), PluginManifest, maxManifest); err == nil {
		if _, version, err := manifestOf(data); err == nil && versionForm.MatchString(version) {
			out.Version = version
		}
	}
	if info, err := os.Lstat(filepath.Join(p.folder(pl), currentLink)); err == nil {
		out.At = info.ModTime().UTC()
	}
	return out, true
}

func (p *Plugins) LastCheck(id string) (Check, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.checks[id], p.running[id]
}

func (p *Plugins) PluginAt(id string) (string, string, error) {
	pl, ok := p.find(id)
	if !ok {
		return "", "", fmt.Errorf("this engine installs no plugin %q", id)
	}
	if commit, ok := p.installedCommit(pl); ok {
		if slices.Contains(pl.Refused, commit) {
			return "", "", fmt.Errorf("the installed %s, commit %s, is one this engine refuses, so it isn't used; the next engine start installs the head of %s if this engine doesn't refuse it", pl.Name, commit[:7], pl.Branch)
		}
		if why := p.failed(commit); why != "" {
			return "", "", fmt.Errorf("the installed %s, commit %s, doesn't pass this engine's install checks, so it isn't used: %s The next engine start installs the head of %s if it passes them", pl.Name, commit[:7], sentence(why), pl.Branch)
		}
		return filepath.Join(p.cfg.SeenAs, PluginsFolder, pl.ID, commit), commit, nil
	}
	p.mu.Lock()
	running, problem := p.running[id], p.checks[id].Problem
	p.mu.Unlock()
	switch {
	case running:
		return "", "", fmt.Errorf("the engine is installing %s from the %s branch of %s into the agents volume; try again once it's done", pl.Name, pl.Branch, pl.URL)
	case problem != "":
		return "", "", fmt.Errorf("%s isn't installed: %s Run %s exec engine software-factory install-agents to try again", pl.Name, problem, session.Compose())
	}
	return "", "", fmt.Errorf("%s isn't installed yet; run %s exec engine software-factory install-agents", pl.Name, session.Compose())
}

func (p *Plugins) failed(commit string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.failing[commit]
}

func (p *Plugins) unusable(pl Tracked, commit string) bool {
	return slices.Contains(pl.Refused, commit) || p.failed(commit) != ""
}

func (p *Plugins) Unusable(id, commit string) bool {
	pl, ok := p.find(id)
	return ok && commit != "" && p.unusable(pl, commit)
}

func (p *Plugins) VerifyInstalled(id string) error {
	pl, ok := p.find(id)
	if !ok {
		return fmt.Errorf("this engine installs no plugin %q", id)
	}
	commit, ok := p.installedCommit(pl)
	if !ok {
		return nil
	}
	err := p.folderProblem(pl, commit)
	p.mu.Lock()
	if err != nil {
		p.failing[commit] = err.Error()
	} else {
		delete(p.failing, commit)
	}
	p.mu.Unlock()
	if err != nil {
		p.cfg.Logger.Warn("plugin install", "event", "installed-refused", "plugin", pl.ID, "commit", commit, "why", err.Error())
		return fmt.Errorf("%s at commit %s: %w", pl.Name, commit, err)
	}
	return nil
}

func (p *Plugins) Update(ctx context.Context, id string, report func(string)) (Check, error) {
	if report == nil {
		report = func(string) {}
	}
	pl, ok := p.find(id)
	if !ok {
		return Check{}, fmt.Errorf("this engine installs no plugin %q", id)
	}
	p.mu.Lock()
	if p.running[id] {
		p.mu.Unlock()
		return Check{}, fmt.Errorf("%s is already being checked", pl.Name)
	}
	p.running[id] = true
	p.mu.Unlock()
	from, had := p.installedCommit(pl)
	if had {
		_ = p.VerifyInstalled(id)
	}
	c := Check{From: from}
	stage := "head"
	head, err := p.head(ctx, pl)
	switch {
	case err != nil:
		err = fmt.Errorf("the head of its %s branch couldn't be read from %s: %w", pl.Branch, pl.URL, err)
	case slices.Contains(pl.Refused, head):
		stage = "install"
		err = refusedHead(pl, head)
	case head == from && p.failed(from) != "":
		stage = "install"
		err = fmt.Errorf("the installed commit, still the head of its %s branch, doesn't pass this engine's install checks: %s", pl.Branch, p.failed(from))
	case head == from:
		c.Result = session.PluginUpToDate
		report(fmt.Sprintf("%s at commit %s is the head of %s; nothing to download.", pl.Name, head, pl.Branch))
	default:
		stage = "install"
		err = p.install(ctx, pl, head, report)
		if err == nil {
			c.Result = session.PluginUpdated
		}
	}
	c.Head, c.At = head, p.cfg.Now().UTC()
	if err != nil {
		c.Problem = sentence(err.Error())
		c.Result = session.PluginKept
		if !had {
			c.Result = session.PluginMissing
		}
	}
	p.mu.Lock()
	delete(p.running, id)
	p.checks[id] = c
	p.mu.Unlock()
	switch c.Result {
	case session.PluginUpToDate:
		p.cfg.Logger.Info("plugin install", "event", "up-to-date", "plugin", pl.ID, "commit", head)
	case session.PluginUpdated:
		p.cfg.Logger.Info("plugin install", "event", "updated", "plugin", pl.ID, "commit", head, "from", from)
	case session.PluginKept:
		p.cfg.Logger.Warn("plugin install", "event", "kept-installed", "plugin", pl.ID, "commit", from, "stage", stage, "fix", session.Compose()+" exec engine software-factory install-agents")
	default:
		p.cfg.Logger.Warn("plugin install", "event", "not-installed", "plugin", pl.ID, "stage", stage, "fix", session.Compose()+" exec engine software-factory install-agents")
	}
	if err != nil {
		return c, fmt.Errorf("%s: %w", pl.Name, err)
	}
	return c, nil
}

func refusedHead(pl Tracked, commit string) error {
	return fmt.Errorf("the head of its %s branch, commit %s, is a %s commit this engine refuses", pl.Branch, commit, pl.Name)
}

func (p *Plugins) head(ctx context.Context, pl Tracked) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, headFor)
	defer cancel()
	head, err := p.cfg.Fetch.BranchHead(ctx, pl.URL, pl.Branch)
	if err != nil {
		return "", err
	}
	if !commitHash.MatchString(head) {
		return "", errors.New("the remote named a head that isn't a full commit hash")
	}
	return head, nil
}

func (p *Plugins) install(ctx context.Context, pl Tracked, commit string, report func(string)) error {
	ctx, cancel := context.WithTimeout(ctx, pluginFor)
	defer cancel()
	base := p.folder(pl)
	if err := os.MkdirAll(base, 0o755); err != nil {
		return fmt.Errorf("the plugins folder can't be created: %w", err)
	}
	unlock, err := lock(ctx, filepath.Join(p.cfg.Dir, ".lock"))
	if err != nil {
		return err
	}
	defer unlock()
	removeLeftovers(base)
	if slices.Contains(pl.Refused, commit) {
		return refusedHead(pl, commit)
	}
	if now, ok := p.installedCommit(pl); ok && now == commit {
		report(fmt.Sprintf("%s at commit %s is already installed.", pl.Name, commit))
		return nil
	}
	if version, ok := p.kept(pl, commit); ok {
		if err := p.point(pl, commit); err != nil {
			return err
		}
		report(fmt.Sprintf("%s %s at commit %s was still in the agents volume; it is the installed one again.", pl.Name, version, commit))
		return nil
	}
	tmp := filepath.Join(base, ".tmp-"+randomHex())
	if err := os.Mkdir(tmp, 0o755); err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	repo, tree := filepath.Join(tmp, "git"), filepath.Join(tmp, "tree")
	for _, d := range []string{repo, tree} {
		if err := os.Mkdir(d, 0o755); err != nil {
			return err
		}
	}
	report(fmt.Sprintf("Fetching %s at commit %s, the head of its %s branch, from %s...", pl.Name, commit, pl.Branch, pl.URL))
	if err := p.cfg.Fetch.FetchCommit(ctx, pl.URL, commit, repo); err != nil {
		return fmt.Errorf("the fetch failed: %w", err)
	}
	c, err := p.cfg.Trees.Commit(repo, commit)
	if err != nil || c.SHA != commit {
		return fmt.Errorf("the commit the remote sent isn't %s", commit)
	}
	version, err := p.checkManifest(repo, commit, pl)
	if err != nil {
		return err
	}
	if err := p.cfg.Trees.WriteTree(repo, commit, tree); err != nil {
		return fmt.Errorf("the plugin's tree couldn't be written: %w", err)
	}
	if err := os.Chmod(tree, 0o755); err != nil {
		return err
	}
	if err := shellRefused(tree); err != nil {
		return err
	}
	if err := p.activate(pl, commit, tree); err != nil {
		return err
	}
	report(fmt.Sprintf("%s %s is installed at commit %s.", pl.Name, version, commit))
	return nil
}

func manifestOf(data []byte) (string, string, error) {
	var m struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return "", "", err
	}
	return m.Name, m.Version, nil
}

func shown(s string) string {
	if len(s) > maxShownName {
		s = s[:maxShownName]
	}
	return fmt.Sprintf("%q", s)
}

func unknownKey(data []byte) (string, error) {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		return "", err
	}
	var unknown []string
	for k := range keys {
		if !slices.Contains(inertKeys, k) {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) == 0 {
		return "", nil
	}
	slices.Sort(unknown)
	return unknown[0], nil
}

func serversRefused(what string) error {
	return fmt.Errorf("%s, which can start programs in the trusted reviewer container, beside the reviewers' sign-in; the engine doesn't install a knackpack that brings MCP or LSP servers, monitors, workflows or settings", what)
}

func judge(data []byte, pl Tracked) (string, error) {
	name, version, err := manifestOf(data)
	key, kerr := unknownKey(data)
	switch {
	case err != nil || kerr != nil:
		return "", fmt.Errorf("the commit's %s isn't a JSON object with a name and a version", PluginManifest)
	case key != "":
		return "", fmt.Errorf("the commit's %s has the key %s, which this engine doesn't know as one that starts no programs; the engine installs only a knackpack whose plugin.json holds keys it knows don't start programs", PluginManifest, shown(key))
	case name != pl.Name:
		return "", fmt.Errorf("the commit's %s names the plugin %s, not %s", PluginManifest, shown(name), pl.Name)
	case !versionForm.MatchString(version):
		return "", fmt.Errorf("the commit's %s gives the version %s, which isn't of the form x.y.z", PluginManifest, shown(version))
	case review.Older(version, pl.Floor):
		return "", fmt.Errorf("the commit's %s gives %s %s, older than %s, the oldest this engine works with", PluginManifest, pl.Name, version, pl.Floor)
	}
	return version, nil
}

func (p *Plugins) checkManifest(repo, commit string, pl Tracked) (string, error) {
	f, err := p.cfg.Trees.ReadFile(repo, commit, PluginManifest)
	if err != nil || f.Mode != push.ModeFile {
		return "", fmt.Errorf("the commit has no %s", PluginManifest)
	}
	if len(f.Data) > maxManifest {
		return "", fmt.Errorf("the commit's %s is over %d KiB", PluginManifest, maxManifest>>10)
	}
	version, err := judge(f.Data, pl)
	if err != nil {
		return "", err
	}
	root, err := p.cfg.Trees.ListTree(repo, commit, "")
	if err != nil {
		return "", errors.New("the commit's top folder couldn't be read, so the engine couldn't check it for MCP and LSP server files")
	}
	if len(root) >= session.MaxListedEntries {
		return "", fmt.Errorf("the commit's top folder lists %d entries or more, too many to check for MCP and LSP server files", session.MaxListedEntries)
	}
	for _, e := range root {
		if slices.Contains(serverFiles, e.Name) {
			return "", serversRefused("the commit has " + e.Name + " in its top folder")
		}
	}
	return version, nil
}

func serverFileIn(dir string) (string, error) {
	for _, name := range serverFiles {
		_, err := os.Lstat(filepath.Join(dir, name))
		switch {
		case err == nil:
			return name, nil
		case !errors.Is(err, fs.ErrNotExist):
			return "", err
		}
	}
	return "", nil
}

func (p *Plugins) folderProblem(pl Tracked, commit string) error {
	dir := filepath.Join(p.folder(pl), commit)
	data, err := readInside(dir, PluginManifest, maxManifest)
	if err != nil {
		return fmt.Errorf("its %s %s", PluginManifest, err.Error())
	}
	if _, err := judge(data, pl); err != nil {
		return err
	}
	name, err := serverFileIn(dir)
	switch {
	case err != nil:
		return errors.New("its top folder couldn't be read to check it for MCP and LSP server files")
	case name != "":
		return serversRefused("the commit has " + name + " in its top folder")
	}
	return shellRefused(dir)
}

func (p *Plugins) kept(pl Tracked, commit string) (string, bool) {
	if slices.Contains(pl.Refused, commit) {
		return "", false
	}
	data, err := readInside(filepath.Join(p.folder(pl), commit), PluginManifest, maxManifest)
	if err != nil {
		return "", false
	}
	version, err := judge(data, pl)
	if err != nil || p.folderProblem(pl, commit) != nil {
		return "", false
	}
	return version, true
}

func (p *Plugins) Agents(id string) []string {
	pl, ok := p.find(id)
	if !ok {
		return nil
	}
	commit, ok := p.installedCommit(pl)
	if !ok {
		return nil
	}
	entries, err := os.ReadDir(filepath.Join(p.folder(pl), commit, "agents"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name, isAgent := strings.CutSuffix(e.Name(), ".md")
		if isAgent && e.Type().IsRegular() && pluginID.MatchString(name) {
			out = append(out, pl.ID+":"+name)
		}
	}
	return out
}

func (p *Plugins) activate(pl Tracked, commit, tree string) error {
	final := filepath.Join(p.folder(pl), commit)
	if err := os.RemoveAll(final); err != nil {
		return err
	}
	if err := os.Rename(tree, final); err != nil {
		return err
	}
	return p.point(pl, commit)
}

func (p *Plugins) point(pl Tracked, commit string) error {
	base := p.folder(pl)
	next := filepath.Join(base, ".current-"+randomHex())
	if err := os.Symlink(commit, next); err != nil {
		return err
	}
	if err := os.Rename(next, filepath.Join(base, currentLink)); err != nil {
		_ = os.Remove(next)
		return err
	}
	return nil
}

func (p *Plugins) Prune(ctx context.Context, id string, keep []string) error {
	pl, ok := p.find(id)
	if !ok {
		return fmt.Errorf("this engine installs no plugin %q", id)
	}
	base := p.folder(pl)
	if _, err := os.Lstat(base); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	unlock, err := lock(ctx, filepath.Join(p.cfg.Dir, ".lock"))
	if err != nil {
		return err
	}
	defer unlock()
	entries, err := os.ReadDir(base)
	if err != nil {
		return err
	}
	current, _ := os.Readlink(filepath.Join(base, currentLink))
	removed, failed := 0, 0
	for _, e := range entries {
		name := e.Name()
		if !commitHash.MatchString(name) || name == current || slices.Contains(keep, name) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(base, name)); err != nil {
			failed++
			continue
		}
		removed++
	}
	if removed > 0 || failed > 0 {
		p.cfg.Logger.Info("plugin install", "event", "pruned", "plugin", pl.ID, "removed", removed, "failed", failed)
	}
	if failed > 0 {
		return fmt.Errorf("%d of %s's old commit folders couldn't be removed", failed, pl.Name)
	}
	return nil
}
