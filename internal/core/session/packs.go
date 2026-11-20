package session

import (
	"context"
	"errors"
	"maps"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/review"
	"github.com/bezmoradi/software-factory/internal/core/shared"
	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

const (
	AuditPack          = "pack"
	PackKnackpack      = "knackpack"
	PackNone           = "none"
	PackFromGit        = "git"
	PackUploaded       = "upload"
	MaxPackUploadFiles = 200
	MaxPackUploadFile  = 256 << 10
	MaxPackUploadBytes = 2 << 20
	MaxPackUploadPath  = 200
	PackKindPack       = "pack"
	MaxPacks           = 8
	MaxPackURL         = 400
	ResultUsed         = "used"
	PackFetchFor       = 2 * time.Minute
)

var (
	PackDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)
	uploadPath = regexp.MustCompile(`^(agents/[A-Za-z0-9._-]+|commands/[A-Za-z0-9._/-]+|skills/[A-Za-z0-9._-]+/[A-Za-z0-9._/-]+)$`)
)

type PackUpload struct {
	Path string
	Text string
}

type ReviewerPack struct {
	Path     string
	Digest   string
	Name     string
	Version  string
	Commit   string
	Provider string
	Agents   []string
}

func (p ReviewerPack) None() bool { return p.Name == "" }

type PackSource struct {
	Kind   string
	URL    string
	Commit string
}

type StoredPack struct {
	Digest        string
	SessionDigest string
	Name          string
	Version       string
	Source        PackSource
	Manifest      workflow.PackManifest
	Agents        []string
	CodeFiles     []string
	AddedAt       time.Time
}

type PackChoice struct {
	Project string
	URL     string
	Pack    string
}

type PackState struct {
	Packs   []StoredPack
	Choices []PackChoice
}

type PackStore interface {
	Load() (PackState, error)
	Save(PackState) error
}

type FetchedPack struct {
	Pack StoredPack
	Keep func() error
	Drop func()
}

type PackFiles interface {
	Fetch(ctx context.Context, url, commit string) (FetchedPack, error)
	Upload(name string, files []PackUpload) (FetchedPack, error)
	Remove(digest string) error
	ReviewerPath(digest string, roles map[review.Role]string) (string, error)
	SessionPath(digest, sessionDigest string) (string, error)
	SessionCode(digest, name string) ([]StartupFile, error)
	Knackpack() (ReviewerPack, error)
	KnackpackCommit() string
	KnackpackAgents() []string
	KnackpackVersion() (version, commit string)
}

type PackSetup struct {
	Kind     string
	Name     string
	Choice   string
	Roles    map[review.Role]string
	Agents   []string
	Workflow workflow.Adapter
}

type SessionPack struct {
	Choice        string
	Name          string
	Version       string
	Digest        string
	SessionDigest string
	Path          string
	CodeFiles     []string
}

type SessionPacks interface {
	SessionPack(project string) (SessionPack, error)
	SessionPackFor(choice string) (SessionPack, error)
	SessionCode(sp SessionPack) ([]StartupFile, error)
	WakeNote(project, choice string) string
}

type ProjectPacks interface {
	PackSetup(project string) PackSetup
	WorkflowOf(choice string) workflow.Adapter
}

type PackView struct {
	Digest        string
	SessionDigest string
	Name          string
	Version       string
	Source        PackSource
	Roles         map[review.Role]string
	Steps         map[workflow.Step]string
	Documents     bool
	Agents        []string
	CodeFiles     []string
	AddedAt       time.Time
	UsedBy        []string
}

type PacksView struct {
	Using            string
	KnackpackVersion string
	KnackpackCommit  string
	Packs            []PackView
	Max              int
}

type PacksPage interface {
	ListPacks(path string) (PacksView, error)
	FetchPack(ctx context.Context, path, url, commit string) (PackView, error)
	AddPack(ctx context.Context, path, url, commit, digest string) (PacksView, error)
	UploadPack(path, name string, files []PackUpload) (PacksView, error)
	RemovePack(path, digest string) (PacksView, error)
	UsePack(path, pack string) (PacksView, error)
}

type PacksConfig struct {
	Store  PackStore
	Files  PackFiles
	Repos  interface{ List() ([]Repository, error) }
	Audit  Audit
	Now    func() time.Time
	Logger shared.Logger
}

type Packs struct {
	cfg      PacksConfig
	mu       sync.Mutex
	fetching sync.Mutex
	usedMu   sync.Mutex
	sessions func() []string
}

var (
	_ PluginSource = (*Packs)(nil)
	_ ProjectPacks = (*Packs)(nil)
	_ PacksPage    = (*Packs)(nil)
)

func NewPacks(cfg PacksConfig) (*Packs, error) {
	if cfg.Store == nil || cfg.Files == nil || cfg.Repos == nil {
		return nil, errors.New("packs: need the state file, the pack files and the repositories")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = quiet{}
	}
	cfg.Audit = masking(cfg.Audit)
	return &Packs{cfg: cfg}, nil
}

func (p *Packs) load() (PackState, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cfg.Store.Load()
}

func (p *Packs) repo(match func(Repository) bool) (Repository, bool, error) {
	list, err := p.cfg.Repos.List()
	if err != nil {
		return Repository{}, false, err
	}
	i := slices.IndexFunc(list, match)
	if i < 0 {
		return Repository{}, false, nil
	}
	return list[i], true, nil
}

func choiceOf(st PackState, r Repository) string {
	for _, c := range st.Choices {
		if c.Project == r.Name && c.URL == r.URL {
			return c.Pack
		}
	}
	return PackKnackpack
}

func packOf(st PackState, digest string) (StoredPack, bool) {
	i := slices.IndexFunc(st.Packs, func(s StoredPack) bool { return s.Digest == digest })
	if i < 0 {
		return StoredPack{}, false
	}
	return st.Packs[i], true
}

func (p *Packs) chosen(project string) (string, StoredPack, error) {
	r, ok, err := p.repo(func(r Repository) bool { return r.Name == project })
	if err != nil {
		return "", StoredPack{}, errors.New("the engine couldn't read its list of repositories, so it can't tell which pack reviews this project")
	}
	if !ok {
		return PackKnackpack, StoredPack{}, nil
	}
	st, err := p.load()
	if err != nil {
		return "", StoredPack{}, errors.New("the engine couldn't read its packs (packs.json), so it can't tell which pack reviews this project")
	}
	choice := choiceOf(st, r)
	if choice == PackKnackpack || choice == PackNone {
		return choice, StoredPack{}, nil
	}
	sp, found := packOf(st, choice)
	if !found {
		return "", StoredPack{}, errors.New("the pack this project uses, " + shortSHA(choice) + ", isn't in the engine's packs any more; pick a pack for the project on the page")
	}
	return choice, sp, nil
}

func agentsOf(name string, bare []string) []string {
	out := make([]string, 0, len(bare))
	for _, a := range bare {
		out = append(out, name+":"+a)
	}
	return out
}

func (p *Packs) ReviewerPack(project string) (ReviewerPack, error) {
	choice, sp, err := p.chosen(project)
	switch {
	case err != nil:
		return ReviewerPack{}, err
	case choice == PackNone:
		return ReviewerPack{}, nil
	case choice == PackKnackpack:
		return p.cfg.Files.Knackpack()
	}
	path, err := p.cfg.Files.ReviewerPath(sp.Digest, sp.Manifest.Roles)
	if err != nil {
		return ReviewerPack{}, err
	}
	return ReviewerPack{Path: path, Digest: sp.Digest, Name: sp.Name, Version: sp.Version, Commit: sp.Source.Commit, Provider: review.PackProvider(sp.Name), Agents: agentsOf(sp.Name, sp.Agents)}, nil
}

func (p *Packs) KnackpackCommit() string {
	return p.cfg.Files.KnackpackCommit()
}

func (p *Packs) PackSetup(project string) PackSetup {
	choice, sp, err := p.chosen(project)
	switch {
	case err != nil:
		return PackSetup{Kind: PackNone, Choice: PackNone, Roles: map[review.Role]string{}, Agents: []string{}, Workflow: workflow.NoAdapter()}
	case choice == PackKnackpack:
		return PackSetup{Kind: PackKnackpack, Name: PackKnackpack, Choice: PackKnackpack, Agents: p.cfg.Files.KnackpackAgents(), Workflow: workflow.Adapters[workflow.Knackpack]}
	case choice == PackNone:
		return PackSetup{Kind: PackNone, Choice: PackNone, Roles: map[review.Role]string{}, Agents: []string{}, Workflow: workflow.NoAdapter()}
	}
	roles := map[review.Role]string{}
	for role, agent := range sp.Manifest.Roles {
		roles[role] = sp.Name + ":" + agent
	}
	return PackSetup{Kind: PackKindPack, Name: sp.Name, Choice: sp.Digest, Roles: roles, Agents: agentsOf(sp.Name, sp.Agents), Workflow: workflow.PackAdapter(sp.Manifest)}
}

func (p *Packs) WorkflowOf(choice string) workflow.Adapter {
	switch choice {
	case "", PackKnackpack:
		return workflow.Adapters[workflow.Knackpack]
	case PackNone:
		return workflow.NoAdapter()
	}
	st, err := p.load()
	if err != nil {
		return workflow.NoAdapter()
	}
	sp, ok := packOf(st, choice)
	if !ok {
		return workflow.NoAdapter()
	}
	return workflow.PackAdapter(sp.Manifest)
}

func (p *Packs) SessionPack(project string) (SessionPack, error) {
	choice, sp, err := p.chosen(project)
	switch {
	case err != nil:
		return SessionPack{}, err
	case choice == PackKnackpack || choice == PackNone:
		return SessionPack{Choice: choice}, nil
	}
	return p.sessionPackOf(sp)
}

func (p *Packs) SessionPackFor(choice string) (SessionPack, error) {
	if choice == PackKnackpack || choice == PackNone {
		return SessionPack{Choice: choice}, nil
	}
	st, err := p.load()
	if err != nil {
		return SessionPack{}, errors.New("the engine couldn't read its packs (packs.json)")
	}
	sp, ok := packOf(st, choice)
	if !ok {
		return SessionPack{}, errors.New("the pack this session runs, " + shortSHA(choice) + ", isn't in the engine's packs any more; start a new session")
	}
	return p.sessionPackOf(sp)
}

func (p *Packs) sessionPackOf(sp StoredPack) (SessionPack, error) {
	path, err := p.cfg.Files.SessionPath(sp.Digest, sp.SessionDigest)
	if err != nil {
		return SessionPack{}, err
	}
	return SessionPack{Choice: sp.Digest, Name: sp.Name, Version: sp.Version, Digest: sp.Digest, SessionDigest: sp.SessionDigest, Path: path, CodeFiles: slices.Clone(sp.CodeFiles)}, nil
}

func (p *Packs) SessionCode(sp SessionPack) ([]StartupFile, error) {
	if sp.Path == "" {
		return nil, nil
	}
	return p.cfg.Files.SessionCode(sp.Digest, sp.Name)
}

func (p *Packs) WakeNote(project, choice string) string {
	if choice == "" {
		return ""
	}
	now, _, err := p.chosen(project)
	if err != nil || now == choice {
		return ""
	}
	st, err := p.load()
	if err != nil {
		st = PackState{}
	}
	return "This session keeps the pack it started with, " + packLabel(st, choice) + "; the project now uses " + packLabel(st, now) + ", which new sessions get."
}

func packLabel(st PackState, choice string) string {
	switch choice {
	case PackKnackpack:
		return "knackpack"
	case PackNone:
		return "No pack"
	}
	if sp, ok := packOf(st, choice); ok {
		return sp.Name + " " + sp.Version + " (" + shortSHA(choice) + ")"
	}
	return "the pack " + shortSHA(choice)
}

func (p *Packs) UseSessions(fn func() []string) {
	p.usedMu.Lock()
	p.sessions = fn
	p.usedMu.Unlock()
}

func (p *Packs) sessionsUsing(digest string) bool {
	p.usedMu.Lock()
	fn := p.sessions
	p.usedMu.Unlock()
	return fn != nil && slices.Contains(fn(), digest)
}

func (p *Packs) byPath(path string) (Repository, error) {
	r, ok, err := p.repo(func(r Repository) bool { return r.Path == path })
	if err != nil {
		return Repository{}, explained(conflict("records"), "The engine couldn't read its list of repositories.")
	}
	if !ok {
		return Repository{}, explained(notFound("project"), "That project is no longer in the list.")
	}
	return r, nil
}

func packViewOf(sp StoredPack) PackView {
	v := PackView{Digest: sp.Digest, SessionDigest: sp.SessionDigest, Name: sp.Name, Version: sp.Version, Source: sp.Source,
		Roles: map[review.Role]string{}, Steps: map[workflow.Step]string{}, Documents: sp.Manifest.Documents != nil,
		Agents: slices.Clone(sp.Agents), CodeFiles: slices.Clone(sp.CodeFiles), AddedAt: sp.AddedAt}
	maps.Copy(v.Roles, sp.Manifest.Roles)
	maps.Copy(v.Steps, sp.Manifest.Steps)
	return v
}

func (p *Packs) viewFor(r Repository, st PackState) PacksView {
	v := PacksView{Using: choiceOf(st, r), Max: MaxPacks, Packs: []PackView{}}
	v.KnackpackVersion, v.KnackpackCommit = p.cfg.Files.KnackpackVersion()
	listed, _ := p.cfg.Repos.List()
	for _, sp := range st.Packs {
		pv := packViewOf(sp)
		for _, l := range listed {
			if choiceOf(st, l) == sp.Digest {
				pv.UsedBy = append(pv.UsedBy, l.Name)
			}
		}
		v.Packs = append(v.Packs, pv)
	}
	return v
}

func (p *Packs) ListPacks(path string) (PacksView, error) {
	r, err := p.byPath(path)
	if err != nil {
		return PacksView{}, err
	}
	st, err := p.load()
	if err != nil {
		return PacksView{}, explained(conflict("state"), "The engine couldn't read its packs (packs.json).")
	}
	return p.viewFor(r, st), nil
}

func packURLProblem(url string) string {
	if url == "" || len(url) > MaxPackURL || !strings.HasPrefix(url, "https://") || strings.ContainsAny(url, " \t\r\n?#@") {
		return "A pack comes from a plain https address of a git repository, with no user, query or fragment."
	}
	return ""
}

func (p *Packs) fetch(ctx context.Context, url, commit string) (FetchedPack, error) {
	if why := packURLProblem(url); why != "" {
		return FetchedPack{}, explained(invalid("url"), why)
	}
	if !sha1Hex.MatchString(commit) {
		return FetchedPack{}, explained(invalid("commit"), "A pack is pinned by a full 40-character commit hash, never a branch or tag.")
	}
	if !p.fetching.TryLock() {
		return FetchedPack{}, explained(limited("fetching"), "The engine is fetching another pack. Wait for it to finish.")
	}
	defer p.fetching.Unlock()
	f, err := p.cfg.Files.Fetch(ctx, url, commit)
	if err != nil {
		p.cfg.Logger.Info("pack", "event", "refused")
		if e, ok := errors.AsType[*Error](err); ok {
			return FetchedPack{}, e
		}
		return FetchedPack{}, explained(invalid("pack"), clip("The engine refused this pack: "+err.Error()+".", maxOutput))
	}
	return f, nil
}

func (p *Packs) FetchPack(ctx context.Context, path, url, commit string) (PackView, error) {
	if _, err := p.byPath(path); err != nil {
		return PackView{}, err
	}
	f, err := p.fetch(ctx, url, commit)
	if err != nil {
		return PackView{}, err
	}
	f.Drop()
	p.cfg.Logger.Info("pack", "event", "fetched", "name", f.Pack.Name, "digest", f.Pack.Digest)
	return packViewOf(f.Pack), nil
}

func (p *Packs) change(r Repository, fn func(*PackState) error) (PacksView, error) {
	p.mu.Lock()
	st, err := p.cfg.Store.Load()
	if err != nil {
		p.mu.Unlock()
		return PacksView{}, explained(conflict("state"), "The engine couldn't read its packs (packs.json).")
	}
	if err := fn(&st); err != nil {
		p.mu.Unlock()
		return PacksView{}, err
	}
	if err := p.cfg.Store.Save(st); err != nil {
		p.mu.Unlock()
		return PacksView{}, explained(conflict("state"), "The engine couldn't save its packs.")
	}
	p.mu.Unlock()
	return p.viewFor(r, st), nil
}

func uploadProblem(name string, files []PackUpload) string {
	if why := workflow.PackNameProblem(name); why != "" {
		return why + "."
	}
	if len(files) == 0 || len(files) > MaxPackUploadFiles {
		return "An upload holds 1 to " + strconv.Itoa(MaxPackUploadFiles) + " files."
	}
	total := 0
	seen := map[string]bool{}
	for _, f := range files {
		shown := clip(f.Path, 80)
		folded := strings.ToLower(f.Path)
		switch {
		case f.Path != workflow.PackManifestFile && (!uploadPath.MatchString(f.Path) || len(f.Path) > MaxPackUploadPath || !filepath.IsLocal(f.Path) || path.Clean(f.Path) != f.Path || slices.Contains(strings.Split(f.Path, "/"), "..")):
			return "The upload's " + shown + " isn't a plain path under agents/, skills/<skill>/ or commands/, or the manifest " + workflow.PackManifestFile + " at its top: an upload holds no hooks, servers, settings or programs."
		case seen[folded]:
			return "The upload names " + shown + " twice, in some letter case."
		case len(f.Text) > MaxPackUploadFile:
			return "The upload's " + shown + " is over " + strconv.Itoa(MaxPackUploadFile>>10) + " KiB."
		case !utf8.ValidString(f.Text) || strings.ContainsRune(f.Text, 0):
			return "The upload's " + shown + " isn't UTF-8 text with no NUL byte."
		}
		seen[folded] = true
		total += len(f.Text)
	}
	if total > MaxPackUploadBytes {
		return "The upload is over " + strconv.Itoa(MaxPackUploadBytes>>20) + " MiB in all."
	}
	return ""
}

func (p *Packs) UploadPack(path, name string, files []PackUpload) (PacksView, error) {
	r, err := p.byPath(path)
	if err != nil {
		return PacksView{}, err
	}
	if why := uploadProblem(name, files); why != "" {
		p.cfg.Logger.Info("pack", "event", "upload-refused")
		return PacksView{}, explained(invalid("refused upload"), "The engine refused this upload: "+why)
	}
	if !p.fetching.TryLock() {
		return PacksView{}, explained(limited("fetching"), "The engine is fetching another pack. Wait for it to finish.")
	}
	defer p.fetching.Unlock()
	f, err := p.cfg.Files.Upload(name, files)
	if err != nil {
		p.cfg.Logger.Info("pack", "event", "upload-refused")
		return PacksView{}, explained(invalid("pack"), clip("The engine refused this pack: "+err.Error()+".", maxOutput))
	}
	return p.keep(r, f, "uploaded the pack "+f.Pack.Name+" "+f.Pack.Version+" ("+strconv.Itoa(len(files))+" files)")
}

func (p *Packs) AddPack(ctx context.Context, path, url, commit, digest string) (PacksView, error) {
	r, err := p.byPath(path)
	if err != nil {
		return PacksView{}, err
	}
	if !PackDigest.MatchString(digest) {
		return PacksView{}, explained(invalid("digest"), "Add a pack with the digest the page showed after fetching it.")
	}
	f, err := p.fetch(ctx, url, commit)
	if err != nil {
		return PacksView{}, err
	}
	sp := f.Pack
	if sp.Digest != digest {
		f.Drop()
		p.cfg.Logger.Info("pack", "event", "digest-changed")
		return PacksView{}, explained(conflict("digest"), "The pack at that commit isn't the one the page showed: its reviewers' digest is now "+shortSHA(sp.Digest)+", not "+shortSHA(digest)+". Fetch it again and read what it holds before you add it.")
	}
	return p.keep(r, f, "added the pack "+sp.Name+" "+sp.Version+" from git, pinned at commit "+shortSHA(sp.Source.Commit))
}

func (p *Packs) keep(r Repository, f FetchedPack, what string) (PacksView, error) {
	sp := f.Pack
	sp.AddedAt = p.cfg.Now()
	added := false
	v, err := p.change(r, func(st *PackState) error {
		if old, ok := packOf(*st, sp.Digest); ok {
			if old.SessionDigest != sp.SessionDigest {
				return explained(conflict("same reviewers"), "The engine already keeps a pack with the same reviewers and other session files, "+old.Name+" "+old.Version+". Remove it first.")
			}
			return nil
		}
		if slices.ContainsFunc(st.Packs, func(s StoredPack) bool { return s.Name == sp.Name }) {
			return explained(conflict("name"), "The engine already keeps a pack named "+sp.Name+". Remove it first, or use the one it keeps.")
		}
		if len(st.Packs) >= MaxPacks {
			return explained(limited("packs"), "The engine already keeps "+strconv.Itoa(MaxPacks)+" packs, its limit. Remove one first.")
		}
		if err := f.Keep(); err != nil {
			return explained(conflict("files"), clip("The engine couldn't keep the pack's files in the agents volume: "+err.Error(), maxOutput))
		}
		added = true
		st.Packs = append(st.Packs, sp)
		return nil
	})
	if !added {
		f.Drop()
	}
	if err != nil {
		return PacksView{}, err
	}
	if added {
		p.audit(AuditEntry{Repository: r.Name, Result: ResultAdded, Hash: sp.Digest, Remote: sp.Source.URL, Commit: sp.Source.Commit,
			Reason: "the developer " + what + ", on the page"})
		p.cfg.Logger.Info("pack", "event", ResultAdded, "name", sp.Name, "digest", sp.Digest)
	}
	return v, nil
}

func (p *Packs) RemovePack(path, digest string) (PacksView, error) {
	r, err := p.byPath(path)
	if err != nil {
		return PacksView{}, err
	}
	if !PackDigest.MatchString(digest) {
		return PacksView{}, explained(invalid("digest"), "Remove a pack by the digest the page shows.")
	}
	listed, err := p.cfg.Repos.List()
	if err != nil {
		return PacksView{}, explained(conflict("records"), "The engine couldn't read its list of repositories.")
	}
	var removed StoredPack
	v, err := p.change(r, func(st *PackState) error {
		sp, ok := packOf(*st, digest)
		if !ok {
			return explained(notFound("pack"), "The engine keeps no pack with that digest.")
		}
		var users []string
		for _, l := range listed {
			if choiceOf(*st, l) == digest {
				users = append(users, l.Name)
			}
		}
		if len(users) > 0 {
			return explained(conflict("in use"), clip("The pack "+sp.Name+" reviews "+strings.Join(users, ", ")+". Pick another pack for "+plural(len(users), "that project", "those projects")+" first.", maxOutput))
		}
		if p.sessionsUsing(digest) {
			return explained(conflict("in use"), "A saved session runs the pack "+sp.Name+", and keeps it across Stop and Wake. Delete that session first.")
		}
		if err := p.cfg.Files.Remove(digest); err != nil {
			return explained(conflict("files"), "The engine couldn't remove the pack's files from the agents volume.")
		}
		st.Packs = slices.DeleteFunc(st.Packs, func(s StoredPack) bool { return s.Digest == digest })
		st.Choices = slices.DeleteFunc(st.Choices, func(c PackChoice) bool { return c.Pack == digest })
		removed = sp
		return nil
	})
	if err != nil {
		return PacksView{}, err
	}
	p.audit(AuditEntry{Repository: r.Name, Result: ResultRemoved, Hash: removed.Digest, Remote: removed.Source.URL, Commit: removed.Source.Commit,
		Reason: "the developer removed the pack " + removed.Name + " " + removed.Version + " on the page"})
	p.cfg.Logger.Info("pack", "event", ResultRemoved, "name", removed.Name, "digest", removed.Digest)
	return v, nil
}

func (p *Packs) UsePack(path, pack string) (PacksView, error) {
	r, err := p.byPath(path)
	if err != nil {
		return PacksView{}, err
	}
	if pack != PackKnackpack && pack != PackNone && !PackDigest.MatchString(pack) {
		return PacksView{}, explained(invalid("pack"), "A project uses knackpack, a pack the engine keeps, by its digest, or none.")
	}
	var chosen StoredPack
	v, err := p.change(r, func(st *PackState) error {
		if pack != PackKnackpack && pack != PackNone {
			sp, ok := packOf(*st, pack)
			if !ok {
				return explained(notFound("pack"), "The engine keeps no pack with that digest. Add it first.")
			}
			chosen = sp
		}
		st.Choices = slices.DeleteFunc(st.Choices, func(c PackChoice) bool { return c.Project == r.Name && c.URL == r.URL })
		if pack != PackKnackpack {
			st.Choices = append(st.Choices, PackChoice{Project: r.Name, URL: r.URL, Pack: pack})
		}
		return nil
	})
	if err != nil {
		return PacksView{}, err
	}
	e := AuditEntry{Repository: r.Name, Result: ResultUsed}
	switch pack {
	case PackKnackpack:
		e.Reason = "the developer picked knackpack, the built-in pack, to review " + r.Name + " on the page"
	case PackNone:
		e.Reason = "the developer picked no pack for " + r.Name + " on the page: nothing reviews its pushes, so each needs the developer's Allow with a reason"
	default:
		e.Hash, e.Remote, e.Commit = chosen.Digest, chosen.Source.URL, chosen.Source.Commit
		e.Reason = "the developer picked the pack " + chosen.Name + " " + chosen.Version + " to review " + r.Name + " on the page"
	}
	p.audit(e)
	p.cfg.Logger.Info("pack", "event", ResultUsed, "pack", packLogged(pack))
	return v, nil
}

func packLogged(pack string) string {
	if PackDigest.MatchString(pack) {
		return shortSHA(pack)
	}
	return pack
}

func (p *Packs) Kept() ([]string, error) {
	st, err := p.load()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(st.Packs))
	for _, sp := range st.Packs {
		out = append(out, sp.Digest)
	}
	return out, nil
}

func (p *Packs) audit(e AuditEntry) {
	if p.cfg.Audit == nil {
		return
	}
	e.Time, e.By, e.Kind = p.cfg.Now(), ByPage, AuditPack
	if err := p.cfg.Audit.Append(e); err != nil {
		p.cfg.Logger.Warn("pack", "event", "audit-not-written")
	}
}
