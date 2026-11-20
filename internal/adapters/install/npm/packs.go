package npm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/push"
	"github.com/bezmoradi/software-factory/internal/core/review"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

const (
	maxPackEntries  = 2000
	maxPackBytes    = 32 << 20
	maxCodeFiles    = 50
	knackpackViewID = "knackpack"
)

var (
	codeFolders  = []string{"hooks", "bin", "scripts"}
	startFolders = []string{"agents", "skills", "commands"}
)

type PacksConfig struct {
	Dir     string
	SeenAs  string
	Plugins *Plugins
	Fetch   CommitFetcher
	Trees   TreeReader
	Logger  shared.Logger
}

type Packs struct {
	cfg PacksConfig
}

var _ session.PackFiles = (*Packs)(nil)

func NewPacks(cfg PacksConfig) (*Packs, error) {
	if !filepath.IsAbs(cfg.Dir) {
		return nil, errors.New("packs: the agents folder must be an absolute path")
	}
	cfg.Dir = filepath.Clean(cfg.Dir)
	if cfg.SeenAs == "" {
		cfg.SeenAs = cfg.Dir
	}
	if !filepath.IsAbs(cfg.SeenAs) {
		return nil, errors.New("packs: the reviewer's agents folder must be an absolute path")
	}
	if cfg.Fetch == nil || cfg.Trees == nil {
		return nil, errors.New("packs: need a git fetcher and the hash-checked reader")
	}
	if cfg.Logger == nil {
		cfg.Logger = quiet{}
	}
	return &Packs{cfg: cfg}, nil
}

func (p *Packs) base() string {
	return filepath.Join(p.cfg.Dir, PacksFolder)
}

func (p *Packs) seen(digest string) string {
	return filepath.Join(p.cfg.SeenAs, PacksFolder, digest, reviewersView)
}

func (p *Packs) lock(ctx context.Context) (func(), error) {
	if err := os.MkdirAll(p.base(), 0o755); err != nil {
		return nil, fmt.Errorf("the packs folder can't be created: %w", err)
	}
	return lock(ctx, filepath.Join(p.cfg.Dir, ".lock"))
}

func refusedPack(format string, args ...any) error {
	return fmt.Errorf(format, args...)
}

func (p *Packs) Fetch(ctx context.Context, url, commit string) (session.FetchedPack, error) {
	ctx, cancel := context.WithTimeout(ctx, session.PackFetchFor)
	defer cancel()
	if err := os.MkdirAll(p.base(), 0o755); err != nil {
		return session.FetchedPack{}, fmt.Errorf("the packs folder can't be created: %w", err)
	}
	tmp := filepath.Join(p.base(), ".tmp-"+randomHex())
	if err := os.Mkdir(tmp, 0o755); err != nil {
		return session.FetchedPack{}, err
	}
	drop := func() { _ = os.RemoveAll(tmp) }
	sp, err := p.fetchInto(ctx, tmp, url, commit)
	if err != nil {
		drop()
		return session.FetchedPack{}, err
	}
	return session.FetchedPack{Pack: sp, Keep: p.keeper(tmp, sp.Digest, drop), Drop: drop}, nil
}

func (p *Packs) keeper(tmp, digest string, drop func()) func() error {
	return func() error {
		defer drop()
		unlock, err := p.lock(context.Background())
		if err != nil {
			return err
		}
		defer unlock()
		final := filepath.Join(p.base(), digest)
		if _, err := os.Lstat(final); err == nil {
			return nil
		}
		return os.Rename(filepath.Join(tmp, "pack"), final)
	}
}

func (p *Packs) fetchInto(ctx context.Context, tmp, url, commit string) (session.StoredPack, error) {
	repo, tree := filepath.Join(tmp, "git"), filepath.Join(tmp, "tree")
	for _, d := range []string{repo, tree} {
		if err := os.Mkdir(d, 0o755); err != nil {
			return session.StoredPack{}, err
		}
	}
	if err := p.cfg.Fetch.FetchCommit(ctx, url, commit, repo); err != nil {
		return session.StoredPack{}, explainedFetch(err)
	}
	c, err := p.cfg.Trees.Commit(repo, commit)
	if err != nil || c.SHA != commit {
		return session.StoredPack{}, refusedPack("the commit the remote sent isn't %s", commit)
	}
	m, err := p.manifestAt(repo, commit)
	if err != nil {
		return session.StoredPack{}, err
	}
	if err := p.pluginAt(repo, commit, m.Name); err != nil {
		return session.StoredPack{}, err
	}
	if err := p.cfg.Trees.WriteTree(repo, commit, tree); err != nil {
		return session.StoredPack{}, refusedPack("the pack's tree couldn't be written: %s", err.Error())
	}
	if err := os.Chmod(tree, 0o755); err != nil {
		return session.StoredPack{}, err
	}
	return finishPack(tmp, tree, m, session.PackSource{Kind: session.PackFromGit, URL: url, Commit: commit}, func() error { return shellRefusedAs(tree, "a pack") })
}

func finishPack(tmp, tree string, m workflow.PackManifest, src session.PackSource, check func() error) (session.StoredPack, error) {
	code, err := sizedTree(tree)
	if err != nil {
		return session.StoredPack{}, err
	}
	agents, err := agentsIn(tree)
	if err != nil {
		return session.StoredPack{}, refusedPack("%s", err.Error())
	}
	files, names, err := reviewerViewOf(m.Name, m.Version, agents)
	if err != nil {
		return session.StoredPack{}, err
	}
	for _, role := range review.Roles {
		if a, ok := m.Roles[role]; ok && !slices.Contains(names, a) {
			return session.StoredPack{}, refusedPack("its manifest gives the %s role to %s, which isn't an agent in its agents folder", role.Name(), shown(a))
		}
	}
	if check != nil {
		if err := check(); err != nil {
			return session.StoredPack{}, err
		}
	}
	sessionDigest, err := treeDigest(tree)
	if err != nil {
		return session.StoredPack{}, errors.New("the pack's tree couldn't be read back to take its digest")
	}
	view := filepath.Join(tmp, "pack")
	if err := os.Mkdir(view, 0o755); err != nil {
		return session.StoredPack{}, err
	}
	if err := writeView(filepath.Join(view, reviewersView), files); err != nil {
		return session.StoredPack{}, fmt.Errorf("the reviewers' copy couldn't be written: %w", err)
	}
	if err := os.Rename(tree, filepath.Join(view, sessionView)); err != nil {
		return session.StoredPack{}, err
	}
	return session.StoredPack{Digest: viewDigest(files, m.Roles), SessionDigest: sessionDigest, Name: m.Name, Version: m.Version,
		Source: src, Manifest: m, Agents: names, CodeFiles: code}, nil
}

func explainedFetch(err error) error {
	return &session.Error{Kind: session.Conflict, Reason: "fetch", Message: sentence("The engine couldn't fetch that commit: " + err.Error())}
}

func (p *Packs) manifestAt(repo, commit string) (workflow.PackManifest, error) {
	f, err := p.cfg.Trees.ReadFile(repo, commit, workflow.PackManifestFile)
	if err != nil || f.Mode != push.ModeFile {
		return workflow.PackManifest{}, refusedPack("the commit has no %s at its top, the manifest naming the pack's roles and steps", workflow.PackManifestFile)
	}
	m, problems := workflow.ParsePackManifest(f.Data)
	if len(problems) > 0 {
		return workflow.PackManifest{}, refusedPack("its %s: %s", workflow.PackManifestFile, strings.Join(problems[:min(len(problems), 6)], "; "))
	}
	return m, nil
}

func (p *Packs) pluginAt(repo, commit, name string) error {
	f, err := p.cfg.Trees.ReadFile(repo, commit, PluginManifest)
	if err != nil || f.Mode != push.ModeFile {
		return refusedPack("the commit has no %s, so it isn't a Claude Code plugin", PluginManifest)
	}
	if len(f.Data) > maxManifest {
		return refusedPack("its %s is over %d KiB", PluginManifest, maxManifest>>10)
	}
	got, _, err := manifestOf(f.Data)
	key, kerr := unknownKey(f.Data)
	switch {
	case err != nil || kerr != nil:
		return refusedPack("its %s isn't a JSON object with a name", PluginManifest)
	case key != "":
		return refusedPack("its %s has the key %s, which this engine doesn't know as one that starts no programs; a pack's plugin.json holds only keys that start none", PluginManifest, shown(key))
	case got != name:
		return refusedPack("its %s names the plugin %s, not %s, the name in %s", PluginManifest, shown(got), name, workflow.PackManifestFile)
	}
	root, err := p.cfg.Trees.ListTree(repo, commit, "")
	if err != nil {
		return errors.New("the commit's top folder couldn't be read, so the engine couldn't check it for server files")
	}
	if len(root) >= session.MaxListedEntries {
		return refusedPack("the commit's top folder lists %d entries or more, too many to check", session.MaxListedEntries)
	}
	for _, e := range root {
		if slices.Contains(serverFiles, e.Name) {
			return refusedPack("the commit has %s in its top folder, which can start programs; the engine doesn't take a pack that brings MCP or LSP servers, monitors, workflows or settings", e.Name)
		}
	}
	return nil
}

func sizedTree(dir string) ([]string, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, errors.New("the pack's tree couldn't be opened")
	}
	defer func() { _ = root.Close() }()
	entries, total := 0, int64(0)
	var code []string
	more := 0
	err = fs.WalkDir(root.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return errors.New("the pack's tree couldn't be read")
		}
		if rel == "." {
			return nil
		}
		entries++
		if entries > maxPackEntries {
			return refusedPack("the pack holds more than %d files and folders", maxPackEntries)
		}
		if d.IsDir() {
			return nil
		}
		info, err := root.Lstat(rel)
		if err != nil {
			return errors.New("the pack's tree couldn't be read")
		}
		total += info.Size()
		if total > maxPackBytes {
			return refusedPack("the pack is over %d MiB", maxPackBytes>>20)
		}
		if runsCode(rel) {
			if len(code) < maxCodeFiles {
				code = append(code, rel)
			} else {
				more++
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if more > 0 {
		code = append(code, fmt.Sprintf("and %d more", more))
	}
	return code, nil
}

func runsCode(rel string) bool {
	top, rest, nested := strings.Cut(rel, "/")
	switch {
	case slices.Contains(codeFolders, strings.ToLower(top)):
		return true
	case nested && (strings.EqualFold(top, "skills") || strings.EqualFold(top, "commands")):
		return !strings.HasSuffix(strings.ToLower(rest), ".md")
	}
	return slices.Contains(serverFiles, rel)
}

func loadedAtStart(rel string) bool {
	top, _, nested := strings.Cut(rel, "/")
	return runsCode(rel) || nested && slices.Contains(startFolders, strings.ToLower(top))
}

func (p *Packs) Remove(digest string) error {
	if !session.PackDigest.MatchString(digest) {
		return errors.New("not a pack digest")
	}
	unlock, err := p.lock(context.Background())
	if err != nil {
		return err
	}
	defer unlock()
	return os.RemoveAll(filepath.Join(p.base(), digest))
}

func (p *Packs) checkedView(digest string, roles map[review.Role]string) error {
	lines, err := treeLines(filepath.Join(p.base(), digest, reviewersView))
	if err != nil {
		return errors.New("its reviewers' copy in the agents volume can't be read")
	}
	if digestOf(append(lines, rolesLine(roles))) != digest {
		return errors.New("its reviewers' copy in the agents volume isn't the one the engine added: its digest changed")
	}
	return nil
}

func (p *Packs) ReviewerPath(digest string, roles map[review.Role]string) (string, error) {
	if !session.PackDigest.MatchString(digest) {
		return "", errors.New("the project's pack has no digest the engine reads")
	}
	if err := p.checkedView(digest, roles); err != nil {
		return "", fmt.Errorf("the pack %s can't review: %w", digest[:12], err)
	}
	return p.seen(digest), nil
}

func (p *Packs) Knackpack() (session.ReviewerPack, error) {
	if p.cfg.Plugins == nil {
		return session.ReviewerPack{}, errors.New("this engine installs no knackpack")
	}
	_, commit, err := p.cfg.Plugins.PluginAt(knackpackViewID)
	if err != nil {
		return session.ReviewerPack{}, err
	}
	pl, _ := p.cfg.Plugins.find(knackpackViewID)
	dir := filepath.Join(p.cfg.Plugins.folder(pl), commit)
	data, err := readInside(dir, PluginManifest, maxManifest)
	if err != nil {
		return session.ReviewerPack{}, fmt.Errorf("its %s %s", PluginManifest, err.Error())
	}
	_, version, err := manifestOf(data)
	if err != nil || !versionForm.MatchString(version) {
		return session.ReviewerPack{}, fmt.Errorf("its %s gives no version of the form x.y.z", PluginManifest)
	}
	agents, err := agentsIn(dir)
	if err != nil {
		return session.ReviewerPack{}, fmt.Errorf("knackpack at commit %s: %s", commit[:7], err.Error())
	}
	files, names, err := reviewerViewOf(pl.ID, version, agents)
	if err != nil {
		return session.ReviewerPack{}, fmt.Errorf("knackpack at commit %s can't review, since %s", commit[:7], err.Error())
	}
	roles := knackpackRoles()
	for _, role := range review.Roles {
		if !slices.Contains(names, roles[role]) {
			return session.ReviewerPack{}, fmt.Errorf("knackpack at commit %s has no agents/%s.md for the %s role", commit[:7], roles[role], role.Name())
		}
	}
	digest := viewDigest(files, roles)
	if err := p.place(digest, roles, files); err != nil {
		return session.ReviewerPack{}, err
	}
	full := make([]string, 0, len(names))
	for _, n := range names {
		full = append(full, pl.ID+":"+n)
	}
	return session.ReviewerPack{Path: p.seen(digest), Digest: digest, Name: pl.ID, Version: version, Commit: commit, Provider: review.PackProvider(pl.ID), Agents: full}, nil
}

func knackpackRoles() map[review.Role]string {
	out := map[review.Role]string{}
	for role, agent := range session.DefaultReviewers {
		out[role] = strings.TrimPrefix(agent, knackpackViewID+":")
	}
	return out
}

func (p *Packs) place(digest string, roles map[review.Role]string, files []viewFile) error {
	if p.checkedView(digest, roles) == nil {
		return nil
	}
	unlock, err := p.lock(context.Background())
	if err != nil {
		return err
	}
	defer unlock()
	if p.checkedView(digest, roles) == nil {
		return nil
	}
	tmp := filepath.Join(p.base(), ".tmp-"+randomHex())
	if err := os.Mkdir(tmp, 0o755); err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	if err := writeView(filepath.Join(tmp, reviewersView), files); err != nil {
		return fmt.Errorf("the reviewers' copy of knackpack couldn't be written: %w", err)
	}
	final := filepath.Join(p.base(), digest)
	if err := os.RemoveAll(final); err != nil {
		return err
	}
	return os.Rename(tmp, final)
}

func (p *Packs) KnackpackCommit() string {
	if p.cfg.Plugins == nil {
		return ""
	}
	_, commit, _ := p.cfg.Plugins.PluginAt(knackpackViewID)
	return commit
}

func (p *Packs) KnackpackAgents() []string {
	if p.cfg.Plugins == nil {
		return nil
	}
	return p.cfg.Plugins.Agents(knackpackViewID)
}

func (p *Packs) KnackpackVersion() (string, string) {
	if p.cfg.Plugins == nil {
		return "", ""
	}
	cur, ok := p.cfg.Plugins.Current(knackpackViewID)
	if !ok {
		return "", ""
	}
	return cur.Version, cur.Commit
}

func (p *Packs) Prune(ctx context.Context, keep []string) error {
	if _, err := os.Lstat(p.base()); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if cur, err := p.Knackpack(); err == nil {
		keep = append(keep, cur.Digest)
	}
	unlock, err := p.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	entries, err := os.ReadDir(p.base())
	if err != nil {
		return err
	}
	removed, failed := 0, 0
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".tmp-") || session.PackDigest.MatchString(name) && !slices.Contains(keep, name) {
			if err := os.RemoveAll(filepath.Join(p.base(), name)); err != nil {
				failed++
				continue
			}
			removed++
		}
	}
	if removed > 0 || failed > 0 {
		p.cfg.Logger.Info("pack", "event", "pruned", "removed", removed, "failed", failed)
	}
	if failed > 0 {
		return fmt.Errorf("%d pack folders couldn't be removed", failed)
	}
	return nil
}

func (p *Packs) SessionPath(digest, sessionDigest string) (string, error) {
	if !session.PackDigest.MatchString(digest) || !session.PackDigest.MatchString(sessionDigest) {
		return "", errors.New("the project's pack has no digests the engine reads")
	}
	got, err := treeDigest(filepath.Join(p.base(), digest, sessionView))
	switch {
	case err != nil:
		return "", fmt.Errorf("the pack %s's session files in the agents volume can't be read", digest[:12])
	case got != sessionDigest:
		return "", fmt.Errorf("the pack %s's session files in the agents volume aren't the ones the engine added: their digest changed", digest[:12])
	}
	return filepath.Join(p.cfg.SeenAs, PacksFolder, digest, sessionView), nil
}

func (p *Packs) SessionCode(digest, name string) ([]session.StartupFile, error) {
	if !session.PackDigest.MatchString(digest) {
		return nil, errors.New("not a pack digest")
	}
	root, err := os.OpenRoot(filepath.Join(p.base(), digest, sessionView))
	if err != nil {
		return nil, errors.New("its session files can't be opened")
	}
	defer func() { _ = root.Close() }()
	var out []session.StartupFile
	total := 0
	err = fs.WalkDir(root.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return errors.New("its session files can't be read")
		}
		if d.IsDir() || !loadedAtStart(rel) {
			return nil
		}
		if len(out) >= session.MaxStartupFiles {
			return fmt.Errorf("it has more than %d files that run code", session.MaxStartupFiles)
		}
		f := session.StartupFile{Path: name + "/" + rel, Place: session.InPack, Committed: true}
		info, err := root.Lstat(rel)
		if err != nil {
			return errors.New("its session files can't be read")
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			target, err := root.Readlink(rel)
			if err != nil {
				return errors.New("its session files can't be read")
			}
			f.Link, f.Target = true, target
		} else {
			if info.Size() > session.MaxStartupFile {
				return fmt.Errorf("its %s is over %d KiB", rel, session.MaxStartupFile>>10)
			}
			data, err := root.ReadFile(rel)
			if err != nil {
				return errors.New("its session files can't be read")
			}
			f.Data = data
		}
		total += len(f.Data)
		if total > session.MaxStartupBytes {
			return fmt.Errorf("its files that run code are over %d MiB", session.MaxStartupBytes>>20)
		}
		out = append(out, f)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (p *Packs) Upload(name string, files []session.PackUpload) (session.FetchedPack, error) {
	if err := os.MkdirAll(p.base(), 0o755); err != nil {
		return session.FetchedPack{}, fmt.Errorf("the packs folder can't be created: %w", err)
	}
	tmp := filepath.Join(p.base(), ".tmp-"+randomHex())
	if err := os.Mkdir(tmp, 0o755); err != nil {
		return session.FetchedPack{}, err
	}
	drop := func() { _ = os.RemoveAll(tmp) }
	sp, err := p.uploadInto(tmp, name, files)
	if err != nil {
		drop()
		return session.FetchedPack{}, err
	}
	return session.FetchedPack{Pack: sp, Keep: p.keeper(tmp, sp.Digest, drop), Drop: drop}, nil
}

func (p *Packs) uploadInto(tmp, name string, files []session.PackUpload) (session.StoredPack, error) {
	tree := filepath.Join(tmp, "tree")
	if err := os.Mkdir(tree, 0o755); err != nil {
		return session.StoredPack{}, err
	}
	root, err := os.OpenRoot(tree)
	if err != nil {
		return session.StoredPack{}, err
	}
	defer func() { _ = root.Close() }()
	var manifest []byte
	for _, f := range files {
		if f.Path == workflow.PackManifestFile {
			manifest = []byte(f.Text)
		}
		if err := root.MkdirAll(filepath.ToSlash(filepath.Dir(f.Path)), 0o755); err != nil {
			return session.StoredPack{}, fmt.Errorf("the upload's %s couldn't be written", f.Path)
		}
		if err := root.WriteFile(f.Path, []byte(f.Text), 0o644); err != nil {
			return session.StoredPack{}, fmt.Errorf("the upload's %s couldn't be written", f.Path)
		}
	}
	if manifest == nil {
		return session.StoredPack{}, refusedPack("the folder has no %s at its top, the manifest naming the pack's roles and steps", workflow.PackManifestFile)
	}
	m, problems := workflow.ParsePackManifest(manifest)
	switch {
	case len(problems) > 0:
		return session.StoredPack{}, refusedPack("its %s: %s", workflow.PackManifestFile, strings.Join(problems[:min(len(problems), 6)], "; "))
	case m.Name != name:
		return session.StoredPack{}, refusedPack("its %s names the pack %s, not %s, the name given for the upload", workflow.PackManifestFile, shown(m.Name), shown(name))
	}
	plugin, _ := json.MarshalIndent(map[string]string{"name": m.Name, "version": m.Version, "description": "The pack " + m.Name + ", uploaded on Software Factory's page"}, "", "  ")
	if err := root.MkdirAll(".claude-plugin", 0o755); err != nil {
		return session.StoredPack{}, err
	}
	if err := root.WriteFile(PluginManifest, append(plugin, '\n'), 0o644); err != nil {
		return session.StoredPack{}, err
	}
	return finishPack(tmp, tree, m, session.PackSource{Kind: session.PackUploaded}, nil)
}
