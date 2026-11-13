package session

import (
	"context"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/push"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	CloneFor       = 10 * time.Minute
	FetchFor       = 30 * time.Second
	MaxClones      = 3
	MaxRecords     = 200
	MaxTokenLength = 4096
	maxURL         = 2048
	maxNameBase    = 60
)

var (
	urlPath    = regexp.MustCompile(`^(/[A-Za-z0-9._~+-]+)+/?$`)
	sha1Hex    = regexp.MustCompile(`^[0-9a-f]{40}$`)
	nameChars  = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
	recordName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
)

type refusal string

func (r refusal) Error() string { return string(r) }

type Record struct {
	Name    string
	URL     string
	Branch  string
	Commit  string
	AddedAt time.Time
}

type Records interface {
	Load() ([]Record, error)
	Save([]Record) error
}

type TokenInfo struct {
	Host    string
	SavedAt time.Time
}

type Tokens interface {
	Token(host string) (string, bool, error)
	List() ([]TokenInfo, error)
	Save(host, token string, at time.Time) error
	Remove(host string) (bool, error)
}

type CloneRequest struct {
	URL   string
	Token string
	Name  string
	Taken func(name string) bool
}

type Cloned struct {
	Name   string
	Branch string
	Commit string
}

type FetchRequest struct {
	Name   string
	URL    string
	Token  string
	Branch string
	Have   []string
}

type Fetched struct {
	Branch string
	Commit string
}

type Remote interface {
	Clone(ctx context.Context, q CloneRequest) (Cloned, error)
	Fetch(ctx context.Context, q FetchRequest) (Fetched, error)
}

type Commits interface {
	Commit(path, sha string) (push.Commit, error)
}

type Cloning struct {
	URL       string
	Name      string
	StartedAt time.Time
}

type RepoView struct {
	Cloning []Cloning
	Hosts   []TokenInfo
}

type RepoAdmin interface {
	List() ([]Repository, error)
	View() (RepoView, error)
	Add(raw string) (Cloning, error)
	Remove(name string) error
	SaveToken(host, token string) (TokenInfo, error)
	RemoveToken(host string) error
}

type ReposConfig struct {
	Workspace string
	Records   Records
	Tokens    Tokens
	Remote    Remote
	Commits   Commits
	Notify    func(Fields)
	Now       func() time.Time
	Base      context.Context
	CloneFor  time.Duration
	FetchFor  time.Duration
	Logger    shared.Logger
}

type Repos struct {
	cfg      ReposConfig
	mu       sync.Mutex
	cloning  map[string]Cloning
	wg       sync.WaitGroup
	sessions func(name string) int
	hosts    func()
}

func (c *Repos) UseSessions(count func(name string) int) {
	c.mu.Lock()
	c.sessions = count
	c.mu.Unlock()
}

func (c *Repos) OnHosts(changed func()) {
	c.mu.Lock()
	c.hosts = changed
	c.mu.Unlock()
}

func (c *Repos) hostsChanged() {
	c.mu.Lock()
	changed := c.hosts
	c.mu.Unlock()
	if changed != nil {
		changed()
	}
}

var (
	_ RepoAdmin    = (*Repos)(nil)
	_ Repositories = (*Repos)(nil)
	_ PushRepos    = (*Repos)(nil)
)

func NewRepos(cfg ReposConfig) *Repos {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Base == nil {
		cfg.Base = context.Background()
	}
	if cfg.CloneFor <= 0 {
		cfg.CloneFor = CloneFor
	}
	if cfg.FetchFor <= 0 {
		cfg.FetchFor = FetchFor
	}
	if cfg.Logger == nil {
		cfg.Logger = quiet{}
	}
	if cfg.Notify == nil {
		cfg.Notify = func(Fields) {}
	}
	return &Repos{cfg: cfg, cloning: map[string]Cloning{}}
}

func NormalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", refusal("Type the repository's https address.")
	}
	if len(raw) > maxURL || controlChars.MatchString(raw) || strings.ContainsAny(raw, " \\\"'`<>{}|^") {
		return "", refusal("That address has characters a repository address doesn't use.")
	}
	if strings.Contains(raw, "#") || strings.Contains(raw, "?") {
		return "", refusal("Leave out the ? query or # fragment: give the repository's plain https address.")
	}
	if !strings.HasPrefix(strings.ToLower(raw), "https://") {
		return "", refusal("Only https:// addresses are accepted; SSH and http aren't.")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" {
		return "", refusal("That isn't an address the engine can read.")
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return "", refusal("Only https:// addresses are accepted; SSH and http aren't.")
	}
	if u.User != nil || strings.Contains(u.Host, "@") {
		return "", refusal("Leave the user name and password out of the address; save a token for the host instead.")
	}
	origin, err := hostOf(u)
	if err != nil {
		return "", err
	}
	p := u.EscapedPath()
	if p != u.Path || !urlPath.MatchString(p) {
		return "", refusal("Give the repository's path after the host, such as https://git.example.com/team/project.git.")
	}
	for part := range strings.SplitSeq(strings.Trim(p, "/"), "/") {
		if part == "." || part == ".." {
			return "", refusal("The path can't have . or .. parts.")
		}
	}
	return origin + strings.TrimRight(p, "/"), nil
}

func HostOf(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw != "" && !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	if len(raw) > maxURL || controlChars.MatchString(raw) || strings.ContainsAny(raw, " \\\"'`<>{}|^#?") {
		return "", refusal("Give the git host as https://host, such as https://git.example.com.")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || !strings.EqualFold(u.Scheme, "https") || u.User != nil {
		return "", refusal("Give the git host as https://host, such as https://git.example.com.")
	}
	return hostOf(u)
}

func hostOf(u *url.URL) (string, error) {
	bad := refusal("The address needs a host name, such as git.example.com.")
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if host == "" || strings.HasSuffix(u.Host, ":") {
		return "", bad
	}
	if strings.Contains(host, ":") {
		ip := net.ParseIP(host)
		if ip == nil || strings.Contains(host, "%") {
			return "", bad
		}
		host = "[" + ip.String() + "]"
	} else if !validHostName(host) {
		return "", bad
	}
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return "", refusal("The port in the address isn't a port number.")
		}
		if n != 443 {
			host += ":" + port
		}
	}
	return "https://" + host, nil
}

func validHostName(h string) bool {
	if len(h) > 253 {
		return false
	}
	for label := range strings.SplitSeq(h, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '_' {
				return false
			}
		}
	}
	return true
}

func sameRepository(a, b string) bool {
	key := func(s string) string {
		return strings.TrimSuffix(strings.TrimRight(s, "/"), ".git")
	}
	return key(a) == key(b)
}

func RepoName(normalized string) string {
	u, err := url.Parse(normalized)
	base := ""
	if err == nil {
		base = u.Path[strings.LastIndex(u.Path, "/")+1:]
	}
	base = strings.TrimSuffix(base, ".git")
	base = nameChars.ReplaceAllString(base, "-")
	for strings.Contains(base, "..") {
		base = strings.ReplaceAll(base, "..", ".")
	}
	base = strings.TrimLeft(base, ".-_")
	if len(base) > maxNameBase {
		base = base[:maxNameBase]
	}
	base = strings.TrimRight(base, ".")
	if before, ok := strings.CutSuffix(base, ".lock"); ok {
		base = before + "-lock"
	}
	if !recordName.MatchString(base) {
		return "repository"
	}
	return base
}

func validToken(t string) bool {
	if t == "" || len(t) > MaxTokenLength {
		return false
	}
	for i := 0; i < len(t); i++ {
		if t[i] < 0x21 || t[i] > 0x7e {
			return false
		}
	}
	return true
}

func (c *Repos) path(name string) string {
	return filepath.Join(c.cfg.Workspace, "repos", name)
}

func (c *Repos) List() ([]Repository, error) {
	recs, err := c.cfg.Records.Load()
	if err != nil {
		return nil, err
	}
	out := make([]Repository, 0, len(recs))
	for _, r := range recs {
		out = append(out, Repository{Name: r.Name, Path: c.path(r.Name), URL: r.URL, Branch: r.Branch, AddedAt: r.AddedAt})
	}
	slices.SortFunc(out, func(a, b Repository) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func (c *Repos) GitHosts() []string {
	var out []string
	if recs, err := c.cfg.Records.Load(); err == nil {
		for _, r := range recs {
			if u, err := url.Parse(r.URL); err == nil && u.Hostname() != "" {
				out = append(out, strings.ToLower(u.Hostname()))
			}
		}
	}
	if hosts, err := c.cfg.Tokens.List(); err == nil {
		for _, h := range hosts {
			if name := hostName(h.Host); name != "" {
				out = append(out, name)
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func hostName(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
		return strings.ToLower(u.Hostname())
	}
	if name, _, err := net.SplitHostPort(raw); err == nil {
		return strings.ToLower(name)
	}
	if strings.ContainsAny(raw, "/:@") {
		return ""
	}
	return strings.ToLower(raw)
}

func (c *Repos) View() (RepoView, error) {
	hosts, err := c.cfg.Tokens.List()
	if err != nil {
		return RepoView{}, err
	}
	c.mu.Lock()
	cloning := make([]Cloning, 0, len(c.cloning))
	for _, k := range c.cloning {
		cloning = append(cloning, k)
	}
	c.mu.Unlock()
	slices.SortFunc(cloning, func(a, b Cloning) int { return a.StartedAt.Compare(b.StartedAt) })
	return RepoView{Cloning: cloning, Hosts: hosts}, nil
}

func (c *Repos) frame(state, u, name, message string) {
	f := Fields{"type": "repo", "state": state, "url": u, "name": name}
	if message != "" {
		f["message"] = message
	}
	c.cfg.Notify(f)
}

func (c *Repos) Add(raw string) (Cloning, error) {
	u, err := NormalizeURL(raw)
	if err != nil {
		return Cloning{}, explained(invalid("url"), err.Error())
	}
	c.mu.Lock()
	recs, err := c.cfg.Records.Load()
	if err != nil {
		c.mu.Unlock()
		return Cloning{}, explained(conflict("records"), "The engine couldn't read its list of repositories, so it added nothing.")
	}
	if i := slices.IndexFunc(recs, func(r Record) bool { return sameRepository(r.URL, u) }); i >= 0 {
		c.mu.Unlock()
		return Cloning{}, explained(conflict("exists"), "That repository is in the list already, as "+recs[i].Name+".")
	}
	if len(recs) >= MaxRecords {
		c.mu.Unlock()
		return Cloning{}, explained(full("records"), "The list holds at most "+strconv.Itoa(MaxRecords)+" repositories. Remove one first.")
	}
	for k := range c.cloning {
		if sameRepository(k, u) {
			c.mu.Unlock()
			return Cloning{}, explained(conflict("cloning"), "That repository is being cloned already.")
		}
	}
	if len(c.cloning) >= MaxClones {
		c.mu.Unlock()
		return Cloning{}, explained(limited("clones"), "Wait for a clone to finish before adding another repository.")
	}
	k := Cloning{URL: u, Name: RepoName(u), StartedAt: c.cfg.Now()}
	c.cloning[u] = k
	c.wg.Add(1)
	c.mu.Unlock()
	c.cfg.Logger.Info("repository", "event", "clone-started")
	c.frame("cloning", u, k.Name, "")
	go c.clone(k)
	return k, nil
}

func (c *Repos) Wait() {
	c.wg.Wait()
}

func (c *Repos) taken(name string) bool {
	recs, err := c.cfg.Records.Load()
	if err != nil {
		return true
	}
	return slices.ContainsFunc(recs, func(r Record) bool { return strings.EqualFold(r.Name, name) })
}

func (c *Repos) clone(k Cloning) {
	defer c.wg.Done()
	ctx, cancel := context.WithTimeout(c.cfg.Base, c.cfg.CloneFor)
	defer cancel()
	fail := func(event, message string) {
		c.mu.Lock()
		delete(c.cloning, k.URL)
		c.mu.Unlock()
		c.cfg.Logger.Warn("repository", "event", event)
		c.frame("failed", k.URL, k.Name, message)
	}
	host, _ := HostOf(k.URL)
	token, _, err := c.cfg.Tokens.Token(host)
	if err != nil {
		fail("clone-failed", "The engine couldn't read its saved tokens.")
		return
	}
	done, err := c.cfg.Remote.Clone(ctx, CloneRequest{URL: k.URL, Token: token, Name: k.Name, Taken: c.taken})
	if err != nil {
		fail("clone-failed", "The clone of "+k.URL+" failed: "+clip(err.Error(), 600))
		return
	}
	if !recordName.MatchString(done.Name) || !push.ValidBranch(done.Branch) || !sha1Hex.MatchString(done.Commit) {
		fail("clone-failed", "The clone finished with a name, branch or commit the engine doesn't accept.")
		return
	}
	if _, err := c.cfg.Commits.Commit(c.path(done.Name), done.Commit); err != nil {
		fail("clone-failed", "The clone finished, but its commit "+done.Commit[:12]+" couldn't be read back from it.")
		return
	}
	c.mu.Lock()
	recs, err := c.cfg.Records.Load()
	if err == nil {
		recs = append(recs, Record{Name: done.Name, URL: k.URL, Branch: done.Branch, Commit: done.Commit, AddedAt: c.cfg.Now()})
		err = c.cfg.Records.Save(recs)
	}
	delete(c.cloning, k.URL)
	c.mu.Unlock()
	if err != nil {
		c.cfg.Logger.Warn("repository", "event", "record-failed")
		c.frame("failed", k.URL, done.Name, "The clone finished, but the engine couldn't record it, so it isn't in the list.")
		return
	}
	c.cfg.Logger.Info("repository", "event", "added")
	c.frame("added", k.URL, done.Name, "")
	c.hostsChanged()
}

func (c *Repos) Remove(name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	recs, err := c.cfg.Records.Load()
	if err != nil {
		return explained(conflict("records"), "The engine couldn't read its list of repositories.")
	}
	kept := slices.DeleteFunc(slices.Clone(recs), func(r Record) bool { return r.Name == name })
	if len(kept) == len(recs) {
		return explained(notFound("repository"), "That repository is no longer in the list.")
	}
	if c.sessions != nil {
		if n := c.sessions(name); n > 0 {
			what := "its session"
			if n > 1 {
				what = "its " + strconv.Itoa(n) + " sessions"
			}
			return explained(conflict("sessions"), "This repository still has sessions, running or stopped. Stop and delete "+what+" first.")
		}
	}
	if err := c.cfg.Records.Save(kept); err != nil {
		return explained(conflict("records"), "The engine couldn't save its list of repositories.")
	}
	c.cfg.Logger.Info("repository", "event", "removed")
	return nil
}

func (c *Repos) SaveToken(host, token string) (TokenInfo, error) {
	h, err := HostOf(host)
	if err != nil {
		return TokenInfo{}, explained(invalid("host"), err.Error())
	}
	if !validToken(token) {
		return TokenInfo{}, explained(invalid("token"), "A token is 1 to 4096 printable characters with no spaces.")
	}
	at := c.cfg.Now().UTC().Truncate(time.Second)
	if err := c.cfg.Tokens.Save(h, token, at); err != nil {
		return TokenInfo{}, explained(conflict("tokens"), "The engine couldn't save the token.")
	}
	c.cfg.Logger.Info("token saved for " + h)
	c.hostsChanged()
	return TokenInfo{Host: h, SavedAt: at}, nil
}

func (c *Repos) RemoveToken(host string) error {
	h, err := HostOf(host)
	if err != nil {
		return explained(invalid("host"), err.Error())
	}
	removed, err := c.cfg.Tokens.Remove(h)
	if err != nil {
		return explained(conflict("tokens"), "The engine couldn't remove the token.")
	}
	if !removed {
		return explained(notFound("token"), "No token is saved for "+h+".")
	}
	c.cfg.Logger.Info("token removed for " + h)
	return nil
}

func (c *Repos) Fresh(ctx context.Context, name string) (string, error) {
	recs, err := c.cfg.Records.Load()
	if err != nil {
		return "", explained(conflict("records"), "The engine couldn't read its list of repositories.")
	}
	i := slices.IndexFunc(recs, func(r Record) bool { return r.Name == name })
	if i < 0 {
		return "", explained(notFound("project"), "That project is no longer in the list.")
	}
	rec := recs[i]
	host, _ := HostOf(rec.URL)
	token, _, err := c.cfg.Tokens.Token(host)
	if err != nil {
		return "", explained(conflict("tokens"), "The engine couldn't read its saved tokens.")
	}
	path := c.path(rec.Name)
	var have []string
	if sha1Hex.MatchString(rec.Commit) {
		if _, err := c.cfg.Commits.Commit(path, rec.Commit); err == nil {
			have = []string{rec.Commit}
		}
	}
	fctx, cancel := context.WithTimeout(ctx, c.cfg.FetchFor)
	got, err := c.cfg.Remote.Fetch(fctx, FetchRequest{Name: rec.Name, URL: rec.URL, Token: token, Branch: rec.Branch, Have: have})
	cancel()
	if err != nil {
		c.cfg.Logger.Warn("repository", "event", "fetch-failed")
		return "", explained(conflict("fetch"), "The engine couldn't fetch the main branch of "+rec.URL+", so no session started: "+clip(err.Error(), 600))
	}
	if !sha1Hex.MatchString(got.Commit) || !push.ValidBranch(got.Branch) {
		return "", explained(conflict("fetch"), "The fetch answered a branch or commit the engine doesn't accept.")
	}
	if _, err := c.cfg.Commits.Commit(path, got.Commit); err != nil {
		return "", explained(conflict("fetch"), "The fetched commit "+got.Commit[:12]+" couldn't be read back from the clone.")
	}
	if got.Commit != rec.Commit {
		c.mu.Lock()
		if recs, err := c.cfg.Records.Load(); err == nil {
			if j := slices.IndexFunc(recs, func(r Record) bool { return r.Name == name }); j >= 0 {
				recs[j].Commit = got.Commit
				_ = c.cfg.Records.Save(recs)
			}
		}
		c.mu.Unlock()
	}
	c.cfg.Logger.Info("repository", "event", "fetched")
	return got.Commit, nil
}

func (c *Repos) record(name string) (Record, error) {
	recs, err := c.cfg.Records.Load()
	if err != nil {
		return Record{}, err
	}
	i := slices.IndexFunc(recs, func(r Record) bool { return r.Name == name })
	if i < 0 {
		return Record{}, notFound("project")
	}
	return recs[i], nil
}

func (c *Repos) PushTarget(name string) (string, string, error) {
	rec, err := c.record(name)
	if err != nil {
		return "", "", err
	}
	host, err := HostOf(rec.URL)
	if err != nil {
		return "", "", err
	}
	token, _, err := c.cfg.Tokens.Token(host)
	if err != nil {
		return "", "", err
	}
	return rec.URL, token, nil
}

func (c *Repos) Main(name string) (string, error) {
	rec, err := c.record(name)
	if err != nil {
		return "", err
	}
	if !sha1Hex.MatchString(rec.Commit) {
		return "", nil
	}
	return rec.Commit, nil
}
