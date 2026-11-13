package npm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/push"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	PluginManifest = ".claude-plugin/plugin.json"
	PyProject      = "pyproject.toml"
	PluginsFolder  = "plugins"
	currentLink    = "current"
	pluginFor      = 10 * time.Minute
)

var (
	pluginID   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	commitHash = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

type CommitFetcher interface {
	FetchCommit(ctx context.Context, url, commit, dir string) error
}

type TreeReader interface {
	Commit(repo, sha string) (push.Commit, error)
	ReadFile(repo, commit, path string) (push.File, error)
	WriteTree(repo, commit, dest string) error
}

type PluginConfig struct {
	Dir     string
	SeenAs  string
	Plugins []Plugin
	Fetch   CommitFetcher
	Trees   TreeReader
	Logger  shared.Logger
}

type Plugins struct {
	cfg     PluginConfig
	mu      sync.Mutex
	running map[string]bool
	failed  map[string]string
}

var _ session.PluginSource = (*Plugins)(nil)

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
		cfg.Plugins = PinnedPlugins
	}
	for _, p := range cfg.Plugins {
		if !pluginID.MatchString(p.ID) || !commitHash.MatchString(p.Commit) {
			return nil, fmt.Errorf("plugin installs: %q needs a plain id and a full commit hash", p.ID)
		}
	}
	if cfg.Fetch == nil || cfg.Trees == nil {
		return nil, errors.New("plugin installs: need a git fetcher and the hash-checked reader")
	}
	if cfg.Logger == nil {
		cfg.Logger = quiet{}
	}
	return &Plugins{cfg: cfg, running: map[string]bool{}, failed: map[string]string{}}, nil
}

func (p *Plugins) List() []Plugin {
	return slices.Clone(p.cfg.Plugins)
}

func (p *Plugins) find(id string) (Plugin, bool) {
	i := slices.IndexFunc(p.cfg.Plugins, func(x Plugin) bool { return x.ID == id })
	if i < 0 {
		return Plugin{}, false
	}
	return p.cfg.Plugins[i], true
}

func (p *Plugins) folder(pl Plugin) string {
	return filepath.Join(p.cfg.Dir, PluginsFolder, pl.ID)
}

func (p *Plugins) Installed(id string) bool {
	pl, ok := p.find(id)
	if !ok {
		return false
	}
	link, err := os.Readlink(filepath.Join(p.folder(pl), currentLink))
	if err != nil || link != pl.Commit {
		return false
	}
	info, err := os.Lstat(filepath.Join(p.folder(pl), pl.Commit, filepath.FromSlash(manifestOf(pl))))
	return err == nil && info.Mode().IsRegular()
}

func (p *Plugins) PluginPath(id string) (string, error) {
	pl, ok := p.find(id)
	if !ok {
		return "", fmt.Errorf("this engine pins no plugin %q", id)
	}
	if p.Installed(id) {
		return filepath.Join(p.cfg.SeenAs, PluginsFolder, pl.ID, pl.Commit), nil
	}
	p.mu.Lock()
	running, failed := p.running[id], p.failed[id]
	p.mu.Unlock()
	switch {
	case running:
		return "", fmt.Errorf("the engine is installing %s %s into the agents volume; try again once it's done", pl.Name, pl.Version)
	case failed != "":
		return "", fmt.Errorf("%s %s isn't installed: %s Run docker compose exec engine software-factory install-agents to try again", pl.Name, pl.Version, failed)
	}
	return "", fmt.Errorf("%s %s isn't installed yet; run docker compose exec engine software-factory install-agents", pl.Name, pl.Version)
}

func (p *Plugins) InstallMissing(ctx context.Context, report func(string)) error {
	var errs []error
	for _, pl := range p.cfg.Plugins {
		if p.Installed(pl.ID) {
			continue
		}
		if err := p.Install(ctx, pl.ID, report); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (p *Plugins) Install(ctx context.Context, id string, report func(string)) error {
	if report == nil {
		report = func(string) {}
	}
	pl, ok := p.find(id)
	if !ok {
		return fmt.Errorf("no pinned plugin %q", id)
	}
	p.mu.Lock()
	if p.running[id] {
		p.mu.Unlock()
		return fmt.Errorf("%s is already being installed", pl.Name)
	}
	p.running[id] = true
	delete(p.failed, id)
	p.mu.Unlock()
	err := p.install(ctx, pl, report)
	p.mu.Lock()
	delete(p.running, id)
	if err != nil {
		p.failed[id] = sentence(err.Error())
	}
	p.mu.Unlock()
	if err != nil {
		p.cfg.Logger.Warn("plugin install", "event", "failed", "plugin", pl.ID)
		return fmt.Errorf("%s %s: %w", pl.Name, pl.Version, err)
	}
	p.cfg.Logger.Info("plugin install", "event", "installed", "plugin", pl.ID, "version", pl.Version, "commit", pl.Commit)
	return nil
}

func (p *Plugins) install(ctx context.Context, pl Plugin, report func(string)) error {
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
	if p.Installed(pl.ID) {
		report(fmt.Sprintf("%s %s is already installed.", pl.Name, pl.Version))
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
	report(fmt.Sprintf("Fetching %s %s at commit %s from %s...", pl.Name, pl.Version, pl.Commit, pl.URL))
	if err := p.cfg.Fetch.FetchCommit(ctx, pl.URL, pl.Commit, repo); err != nil {
		return fmt.Errorf("the fetch failed: %w", err)
	}
	c, err := p.cfg.Trees.Commit(repo, pl.Commit)
	if err != nil || c.SHA != pl.Commit {
		return fmt.Errorf("the commit the remote sent isn't %s", pl.Commit)
	}
	if err := p.checkManifest(repo, pl); err != nil {
		return err
	}
	if err := p.cfg.Trees.WriteTree(repo, pl.Commit, tree); err != nil {
		return fmt.Errorf("the plugin's tree couldn't be written: %w", err)
	}
	if err := os.Chmod(tree, 0o755); err != nil {
		return err
	}
	return p.activate(pl, tree)
}

func manifestOf(pl Plugin) string {
	if pl.Manifest != "" {
		return pl.Manifest
	}
	return PluginManifest
}

func (p *Plugins) checkManifest(repo string, pl Plugin) error {
	name := manifestOf(pl)
	f, err := p.cfg.Trees.ReadFile(repo, pl.Commit, name)
	if err != nil || f.Mode != push.ModeFile {
		return fmt.Errorf("the commit has no %s", name)
	}
	if name == PyProject {
		if !pyProjectNames(string(f.Data), pl.Name, pl.Version) {
			return fmt.Errorf("the commit's %s doesn't name %s %s", name, pl.Name, pl.Version)
		}
		return nil
	}
	var m struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if json.Unmarshal(f.Data, &m) != nil || m.Name != pl.Name || m.Version != pl.Version {
		return fmt.Errorf("the commit's %s doesn't name %s %s", name, pl.Name, pl.Version)
	}
	return nil
}

func pyProjectNames(text, name, version string) bool {
	inProject, gotName, gotVersion := false, false, false
	for line := range strings.SplitSeq(text, "\n") {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "[") {
			inProject = l == "[project]"
			continue
		}
		if !inProject {
			continue
		}
		gotName = gotName || l == `name = "`+name+`"`
		gotVersion = gotVersion || l == `version = "`+version+`"`
	}
	return gotName && gotVersion
}

func (p *Plugins) Agents(id string) []string {
	pl, ok := p.find(id)
	if !ok || !p.Installed(id) {
		return nil
	}
	entries, err := os.ReadDir(filepath.Join(p.folder(pl), pl.Commit, "agents"))
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

func (p *Plugins) activate(pl Plugin, tree string) error {
	base := p.folder(pl)
	final := filepath.Join(base, pl.Commit)
	if err := os.RemoveAll(final); err != nil {
		return err
	}
	if err := os.Rename(tree, final); err != nil {
		return err
	}
	next := filepath.Join(base, ".current-"+randomHex())
	if err := os.Symlink(pl.Commit, next); err != nil {
		return err
	}
	if err := os.Rename(next, filepath.Join(base, currentLink)); err != nil {
		_ = os.Remove(next)
		return err
	}
	entries, _ := os.ReadDir(base)
	for _, e := range entries {
		if e.Name() != pl.Commit && e.Name() != currentLink && commitHash.MatchString(e.Name()) {
			_ = os.RemoveAll(filepath.Join(base, e.Name()))
		}
	}
	return nil
}
