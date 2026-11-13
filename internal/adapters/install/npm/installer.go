package npm

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	DefaultRegistry = "https://registry.npmjs.org"
	BinFolder       = "bin"
	marker          = ".installed.json"
	maxMeta         = 4 << 20
	maxTarball      = 768 << 20
	maxUnpacked     = 2 << 30
	maxEntries      = 20000
	installFor      = 30 * time.Minute
)

type Config struct {
	Dir      string
	Registry string
	Arch     string
	Agents   []Agent
	Client   *http.Client
	Logger   shared.Logger
}

type Installer struct {
	cfg     Config
	mu      sync.Mutex
	running map[string]bool
	failed  map[string]string
}

var _ session.Installs = (*Installer)(nil)

func New(cfg Config) (*Installer, error) {
	if !filepath.IsAbs(cfg.Dir) {
		return nil, errors.New("agent installs: the agents folder must be an absolute path")
	}
	cfg.Dir = filepath.Clean(cfg.Dir)
	if cfg.Registry == "" {
		cfg.Registry = DefaultRegistry
	}
	u, err := url.Parse(cfg.Registry)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.Path != "" && u.Path != "/" {
		return nil, fmt.Errorf("agent installs: the registry must be a plain https URL, got %q", cfg.Registry)
	}
	cfg.Registry = strings.TrimSuffix(cfg.Registry, "/")
	if cfg.Arch == "" {
		cfg.Arch = runtime.GOARCH
	}
	if cfg.Agents == nil {
		cfg.Agents = Pinned
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 15 * time.Second}).DialContext,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			MaxIdleConnsPerHost:   2,
		}}
	}
	if cfg.Logger == nil {
		cfg.Logger = quiet{}
	}
	return &Installer{cfg: cfg, running: map[string]bool{}, failed: map[string]string{}}, nil
}

func (i *Installer) Agents() []Agent {
	return slices.Clone(i.cfg.Agents)
}

func (i *Installer) find(id string) (Agent, Build, bool) {
	for _, a := range i.cfg.Agents {
		if a.ID == id {
			b, ok := a.Builds[i.cfg.Arch]
			return a, b, ok
		}
	}
	return Agent{}, Build{}, false
}

func (i *Installer) versionDir(a Agent) string {
	return filepath.Join(i.cfg.Dir, a.ID, a.Version)
}

func (i *Installer) Installed(id string) bool {
	a, b, ok := i.find(id)
	if !ok {
		return false
	}
	link, err := os.Readlink(filepath.Join(i.cfg.Dir, BinFolder, a.Command))
	if err != nil || link != linkTarget(a, b) {
		return false
	}
	info, err := os.Lstat(filepath.Join(i.versionDir(a), marker))
	return err == nil && info.Mode().IsRegular()
}

func linkTarget(a Agent, b Build) string {
	return path.Join("..", a.ID, a.Version, b.Bin)
}

func (i *Installer) Ready(id string) (bool, string) {
	a, _, ok := i.find(id)
	if !ok {
		if slices.ContainsFunc(i.cfg.Agents, func(x Agent) bool { return x.ID == id }) {
			return false, "This engine has no pinned build of that agent for " + i.cfg.Arch + "."
		}
		return true, ""
	}
	if i.Installed(id) {
		return true, ""
	}
	i.mu.Lock()
	running, failed := i.running[id], i.failed[id]
	i.mu.Unlock()
	switch {
	case running:
		return false, fmt.Sprintf("The engine is installing %s %s into the agents volume. It shows here once it's done.", a.Name, a.Version)
	case failed != "":
		return false, fmt.Sprintf("%s %s isn't installed: %s Run docker compose exec engine software-factory install-agents to try again.", a.Name, a.Version, failed)
	}
	return false, fmt.Sprintf("%s %s isn't installed yet. Run docker compose exec engine software-factory install-agents.", a.Name, a.Version)
}

func (i *Installer) InstallMissing(ctx context.Context, report func(string)) error {
	var errs []error
	for _, a := range i.cfg.Agents {
		if _, _, ok := i.find(a.ID); !ok || i.Installed(a.ID) {
			continue
		}
		if err := i.Install(ctx, a.ID, report); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (i *Installer) Install(ctx context.Context, id string, report func(string)) error {
	if report == nil {
		report = func(string) {}
	}
	a, b, ok := i.find(id)
	if !ok {
		return fmt.Errorf("no pinned build of %q for %s", id, i.cfg.Arch)
	}
	i.mu.Lock()
	if i.running[id] {
		i.mu.Unlock()
		return fmt.Errorf("%s is already being installed", a.Name)
	}
	i.running[id] = true
	delete(i.failed, id)
	i.mu.Unlock()
	err := i.install(ctx, a, b, report)
	i.mu.Lock()
	delete(i.running, id)
	if err != nil {
		i.failed[id] = sentence(err.Error())
	}
	i.mu.Unlock()
	if err != nil {
		i.cfg.Logger.Warn("agent install", "event", "failed", "agent", a.ID)
		return fmt.Errorf("%s %s: %w", a.Name, a.Version, err)
	}
	i.cfg.Logger.Info("agent install", "event", "installed", "agent", a.ID, "version", a.Version)
	return nil
}

func sentence(s string) string {
	if len(s) > 300 {
		s = s[:300]
	}
	s = strings.ToValidUTF8(s, "")
	if !strings.HasSuffix(s, ".") {
		s += "."
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

type manifest struct {
	Agent       string   `json:"agent"`
	Version     string   `json:"version"`
	Arch        string   `json:"arch"`
	Packages    []string `json:"packages"`
	Integrity   []string `json:"integrity"`
	InstalledAt string   `json:"installedAt"`
}

func (i *Installer) install(ctx context.Context, a Agent, b Build, report func(string)) error {
	ctx, cancel := context.WithTimeout(ctx, installFor)
	defer cancel()
	if err := os.MkdirAll(i.cfg.Dir, 0o755); err != nil {
		return fmt.Errorf("the agents folder can't be created: %w", err)
	}
	unlock, err := lock(ctx, filepath.Join(i.cfg.Dir, ".lock"))
	if err != nil {
		return err
	}
	defer unlock()
	if i.Installed(a.ID) {
		report(fmt.Sprintf("%s %s is already installed.", a.Name, a.Version))
		return nil
	}
	tmp := filepath.Join(i.cfg.Dir, ".tmp-"+a.ID+"-"+randomHex())
	if err := os.Mkdir(tmp, 0o755); err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	m := manifest{Agent: a.ID, Version: a.Version, Arch: i.cfg.Arch, InstalledAt: time.Now().UTC().Format(time.RFC3339)}
	for _, p := range b.Packages {
		report(fmt.Sprintf("Downloading %s@%s and checking it against the pinned sha512...", p.Name, p.Version))
		if err := i.fetch(ctx, p, tmp); err != nil {
			return err
		}
		m.Packages = append(m.Packages, p.Name+"@"+p.Version)
		m.Integrity = append(m.Integrity, p.Integrity)
	}
	bin := filepath.Join(tmp, filepath.FromSlash(b.Bin))
	info, err := os.Lstat(bin)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("the package has no %s", b.Bin)
	}
	if err := os.Chmod(bin, 0o755); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(filepath.Join(tmp, marker), append(data, '\n'), 0o644); err != nil {
		return err
	}
	return i.activate(a, b, tmp)
}

func (i *Installer) activate(a Agent, b Build, tmp string) error {
	parent := filepath.Join(i.cfg.Dir, a.ID)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	final := i.versionDir(a)
	if err := os.RemoveAll(final); err != nil {
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		return err
	}
	bins := filepath.Join(i.cfg.Dir, BinFolder)
	if err := os.MkdirAll(bins, 0o755); err != nil {
		return err
	}
	next := filepath.Join(bins, "."+a.Command+"-"+randomHex())
	if err := os.Symlink(linkTarget(a, b), next); err != nil {
		return err
	}
	if err := os.Rename(next, filepath.Join(bins, a.Command)); err != nil {
		_ = os.Remove(next)
		return err
	}
	entries, _ := os.ReadDir(parent)
	for _, e := range entries {
		if e.Name() != a.Version {
			_ = os.RemoveAll(filepath.Join(parent, e.Name()))
		}
	}
	return nil
}

func randomHex() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type metaJSON struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Dist    struct {
		Integrity string `json:"integrity"`
		Tarball   string `json:"tarball"`
	} `json:"dist"`
}

func (i *Installer) get(ctx context.Context, raw string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := i.cfg.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("the registry didn't answer: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("the registry answered %d for %s", resp.StatusCode, raw)
	}
	return resp, nil
}

func (i *Installer) fetch(ctx context.Context, p Package, tmp string) error {
	resp, err := i.get(ctx, i.cfg.Registry+"/"+strings.Replace(p.Name, "/", "%2f", 1)+"/"+url.PathEscape(p.Version))
	if err != nil {
		return err
	}
	var meta metaJSON
	err = json.NewDecoder(io.LimitReader(resp.Body, maxMeta)).Decode(&meta)
	_ = resp.Body.Close()
	if err != nil {
		return fmt.Errorf("the registry's record of %s@%s couldn't be read", p.Name, p.Version)
	}
	if meta.Name != p.Name || meta.Version != p.Version {
		return fmt.Errorf("the registry answered %s@%s for %s@%s", meta.Name, meta.Version, p.Name, p.Version)
	}
	if meta.Dist.Integrity != p.Integrity {
		return fmt.Errorf("the registry's dist.integrity for %s@%s isn't the pinned one", p.Name, p.Version)
	}
	reg, _ := url.Parse(i.cfg.Registry)
	tar, err := url.Parse(meta.Dist.Tarball)
	if err != nil || tar.Scheme != reg.Scheme || tar.Host != reg.Host {
		return fmt.Errorf("the tarball of %s@%s isn't on the registry's own host", p.Name, p.Version)
	}
	resp, err = i.get(ctx, tar.String())
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(tmp, ".download-*")
	if err != nil {
		_ = resp.Body.Close()
		return err
	}
	defer func() { _ = file.Close(); _ = os.Remove(file.Name()) }()
	sum := sha512.New()
	n, err := io.Copy(io.MultiWriter(file, sum), io.LimitReader(resp.Body, maxTarball+1))
	_ = resp.Body.Close()
	if err != nil {
		return fmt.Errorf("the download of %s@%s broke off", p.Name, p.Version)
	}
	if n > maxTarball {
		return fmt.Errorf("the tarball of %s@%s is larger than %d MiB", p.Name, p.Version, maxTarball>>20)
	}
	if got := "sha512-" + base64.StdEncoding.EncodeToString(sum.Sum(nil)); got != p.Integrity {
		return fmt.Errorf("the tarball of %s@%s doesn't match its pinned sha512", p.Name, p.Version)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	folder := p.Folder
	if folder == "" {
		folder = p.Name
	}
	dest := filepath.Join(tmp, "node_modules", filepath.FromSlash(folder))
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	if err := Extract(file, dest); err != nil {
		return fmt.Errorf("%s@%s: %w", p.Name, p.Version, err)
	}
	return checkManifest(dest, p)
}

func checkManifest(dest string, p Package) error {
	data, err := os.ReadFile(filepath.Join(dest, "package.json"))
	if err != nil {
		return fmt.Errorf("%s@%s has no package.json", p.Name, p.Version)
	}
	var m struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if json.Unmarshal(data, &m) != nil || m.Name != p.Name || m.Version != p.Version {
		return fmt.Errorf("the package.json inside %s@%s names another package", p.Name, p.Version)
	}
	return nil
}

func Extract(r io.Reader, dest string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return errors.New("the tarball isn't gzip")
	}
	defer func() { _ = gz.Close() }()
	root, err := os.OpenRoot(dest)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	tr := tar.NewReader(gz)
	var total int64
	for n := 0; ; n++ {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return errors.New("the tarball is damaged")
		}
		if n >= maxEntries {
			return errors.New("the tarball has too many entries")
		}
		name, ok := entryName(h.Name)
		if !ok {
			return fmt.Errorf("the tarball has an entry outside package/: %q", h.Name)
		}
		if name == "" {
			continue
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(name, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			total += h.Size
			if h.Size < 0 || total > maxUnpacked {
				return errors.New("the tarball unpacks to more than the limit")
			}
			if err := writeEntry(root, name, h, tr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("the tarball has an entry that isn't a file or folder: %q", h.Name)
		}
	}
}

func entryName(raw string) (string, bool) {
	if strings.ContainsRune(raw, 0) || strings.Contains(raw, "\\") || strings.HasPrefix(raw, "/") {
		return "", false
	}
	clean := path.Clean(raw)
	rest, ok := strings.CutPrefix(clean, "package/")
	if clean == "package" {
		return "", true
	}
	if !ok || rest == "" || !fs.ValidPath(rest) {
		return "", false
	}
	return rest, true
}

func writeEntry(root *os.Root, name string, h *tar.Header, r io.Reader) error {
	if dir := path.Dir(name); dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	mode := os.FileMode(0o644)
	if h.Mode&0o111 != 0 {
		mode = 0o755
	}
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("the tarball's %q can't be written: %w", name, err)
	}
	_, err = io.Copy(f, io.LimitReader(r, h.Size))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return root.Chmod(name, mode)
}

type quiet struct{}

func (quiet) Error(string, ...any) {}
func (quiet) Fatal(error)          {}
func (quiet) Info(string, ...any)  {}
func (quiet) Warn(string, ...any)  {}
