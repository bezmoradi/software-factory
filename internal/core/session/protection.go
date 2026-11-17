package session

import (
	"context"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	ProtectionUnknown = "unknown"
	ProtectionOn      = "protected"
	ProtectionOff     = "not protected"
	RequiredYes       = "required"
	RequiredNo        = "not required"

	NeedAdministration = "Administration: Read-only"
	MaxLookupProjects  = 1000
	protectionFor      = 20 * time.Second
	maxProtectionNotes = 6
	maxCheckNames      = 30
)

type Protection struct {
	State      string
	Reviews    string
	Approvals  int
	Checks     string
	CheckNames []string
	Notes      []string
}

type ProtectionReader interface {
	Supports(host string) bool
	Protection(ctx context.Context, r ForgeRepo, branch string) (Protection, error)
}

type LookupState struct {
	Projects map[string]bool
	Removed  []RemovedProject[bool]
}

type LookupStore interface {
	Load() (LookupState, error)
	Save(LookupState) error
}

type ProjectFactsView struct {
	Repo             string
	Branch           string
	Commit           string
	Workflows        []WorkflowRun
	WorkflowsProblem string
	Protection       Protection
	Lookups          bool
	Readiness        []ReadinessLine
}

type ProjectFactsPage interface {
	ProjectFacts(ctx context.Context, repo string) (ProjectFactsView, error)
	SetLookups(repo string, on bool) error
}

type ProjectFactsConfig struct {
	Repos      *Repos
	Workflows  WorkflowReader
	Protection ProtectionReader
	Lookups    LookupStore
	Files      MainFiles
	Check      func(project string) string
	Audit      Audit
	Now        func() time.Time
	Logger     shared.Logger
}

type ProjectFacts struct {
	cfg     ProjectFactsConfig
	mu      sync.Mutex
	loaded  bool
	on      map[string]bool
	removed []RemovedProject[bool]
}

var _ ProjectData = (*ProjectFacts)(nil)

func NewProjectFacts(cfg ProjectFactsConfig) *ProjectFacts {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = quiet{}
	}
	return &ProjectFacts{cfg: cfg, on: map[string]bool{}}
}

func (f *ProjectFacts) loadLocked() error {
	if f.loaded || f.cfg.Lookups == nil {
		return nil
	}
	st, err := f.cfg.Lookups.Load()
	if err != nil {
		f.cfg.Logger.Warn("project facts", "event", "lookups-unreadable", "err", err.Error())
		return err
	}
	f.loaded = true
	for name, on := range st.Projects {
		if on {
			f.on[name] = true
		}
	}
	f.removed = slices.Clone(st.Removed)
	return nil
}

func (f *ProjectFacts) projectData(event, name string, fn func(st *LookupState) bool) {
	if f.cfg.Lookups == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.loadLocked(); err != nil {
		projectDataNotSaved(f.cfg.Logger, "project facts", name, err)
		return
	}
	st := LookupState{Projects: maps.Clone(f.on), Removed: slices.Clone(f.removed)}
	if !fn(&st) {
		return
	}
	if err := f.cfg.Lookups.Save(st); err != nil {
		projectDataNotSaved(f.cfg.Logger, "project facts", name, err)
		return
	}
	f.on, f.removed = st.Projects, st.Removed
	f.cfg.Logger.Info("project facts", "event", event)
}

func (f *ProjectFacts) ParkProject(id ProjectID) {
	f.projectData("lookups-parked", id.Name, func(st *LookupState) bool {
		var parked bool
		st.Removed, _, parked = parkProject(st.Projects, st.Removed, id)
		return parked
	})
}

func (f *ProjectFacts) RestoreProject(id ProjectID) {
	f.projectData("lookups-restored", id.Name, func(st *LookupState) bool {
		var changed bool
		st.Removed, changed = restoreProject(st.Projects, st.Removed, id)
		return changed
	})
}

func (f *ProjectFacts) KeepProjects(names []string) {
	f.projectData("lookups-unlisted-set-aside", "", func(st *LookupState) bool {
		var n int
		st.Removed, n = keepProjects(st.Projects, st.Removed, names)
		return n > 0
	})
}

func (f *ProjectFacts) LookupsOn(project string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_ = f.loadLocked()
	return f.on[project]
}

func (f *ProjectFacts) SetLookups(repo string, on bool) error {
	if f.cfg.Repos == nil || f.cfg.Lookups == nil {
		return explained(conflict("not-yet"), "This engine keeps no project settings.")
	}
	if _, err := f.cfg.Repos.record(repo); err != nil {
		return explained(notFound("project"), "That project is no longer in the list.")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.loadLocked() != nil {
		return explained(conflict("records"), "The engine couldn't read its package lookup setting.")
	}
	next := map[string]bool{}
	maps.Copy(next, f.on)
	if on {
		next[repo] = true
	} else {
		delete(next, repo)
	}
	if len(next) > MaxLookupProjects {
		return explained(conflict("full"), "The package lookup is on for too many projects.")
	}
	if err := f.cfg.Lookups.Save(LookupState{Projects: next, Removed: slices.Clone(f.removed)}); err != nil {
		return explained(conflict("records"), "The engine couldn't save the package lookup setting.")
	}
	f.on = next
	if f.cfg.Audit != nil {
		why := "the person switched off the public package registry lookup for new packages on the push card"
		if on {
			why = "the person switched on the public package registry lookup for new packages on the push card"
		}
		if err := f.cfg.Audit.Append(AuditEntry{Time: f.cfg.Now(), Repository: repo, By: ByPage, Kind: "package-lookup", Result: map[bool]string{true: "on", false: "off"}[on], Reason: why}); err != nil {
			f.cfg.Logger.Warn("project facts", "event", "audit-not-written")
		}
	}
	return nil
}

func (f *ProjectFacts) ProjectFacts(ctx context.Context, repo string) (ProjectFactsView, error) {
	if f.cfg.Repos == nil {
		return ProjectFactsView{}, explained(conflict("not-yet"), "This engine has no repositories.")
	}
	list, err := f.cfg.Repos.List()
	if err != nil {
		return ProjectFactsView{}, explained(conflict("records"), "The engine couldn't read its list of repositories.")
	}
	i := slices.IndexFunc(list, func(r Repository) bool { return r.Name == repo })
	if i < 0 {
		return ProjectFactsView{}, explained(notFound("project"), "That project is no longer in the list.")
	}
	r := list[i]
	out := ProjectFactsView{Repo: r.Name, Branch: r.Branch, Lookups: f.LookupsOn(r.Name), Protection: Protection{State: ProtectionUnknown, Reviews: ProtectionUnknown, Checks: ProtectionUnknown}}
	main, err := f.cfg.Repos.Main(r.Name)
	var files []WorkflowFile
	switch {
	case err != nil:
		main = ""
		out.WorkflowsProblem = "The engine couldn't read the repository's record."
	case main == "":
		out.WorkflowsProblem = "The engine hasn't fetched the main branch yet; start a session to fetch it."
	case f.cfg.Workflows == nil:
		out.WorkflowsProblem = "This engine doesn't read workflows."
	default:
		out.Commit = main
		files, err = f.cfg.Workflows.Workflows(r.Path, main)
		if err != nil {
			out.WorkflowsProblem = clip("The engine couldn't read .github/workflows/ on the main branch: "+err.Error(), 400)
		} else {
			out.Workflows = WorkflowRuns(files, nil, SampleBranch, false)
		}
	}
	out.Protection = f.protection(ctx, r)
	out.Readiness = f.readiness(r, main, files, out.WorkflowsProblem, out.Protection)
	return out, nil
}

func (f *ProjectFacts) protection(ctx context.Context, r Repository) Protection {
	unknown := Protection{State: ProtectionUnknown, Reviews: ProtectionUnknown, Checks: ProtectionUnknown}
	if f.cfg.Protection == nil {
		unknown.Notes = []string{"This engine doesn't read branch protection."}
		return unknown
	}
	raw, token, branch, err := f.cfg.Repos.ForgeTarget(r.Name)
	if err != nil {
		unknown.Notes = []string{"The engine couldn't read the repository's record or its saved token."}
		return unknown
	}
	host, owner, name, ok := ForgeRepoOf(raw)
	if !ok || !f.cfg.Protection.Supports(host) {
		unknown.Notes = []string{"The engine reads branch protection only from GitHub's API (github.com or a GitHub Enterprise server)."}
		return unknown
	}
	if token == "" {
		unknown.Notes = []string{"No token is saved for " + strings.TrimPrefix(host, "https://") + ", so the engine can't ask."}
		return unknown
	}
	if branch == "" {
		unknown.Notes = []string{"The engine doesn't know the main branch's name yet; start a session to fetch it."}
		return unknown
	}
	ctx, cancel := context.WithTimeout(ctx, protectionFor)
	defer cancel()
	p, err := f.cfg.Protection.Protection(ctx, ForgeRepo{Host: host, Owner: owner, Name: name, Token: token}, branch)
	if err != nil {
		msg := err.Error()
		if token != "" {
			msg = strings.ReplaceAll(msg, token, "[token]")
		}
		unknown.Notes = []string{clip(maskSecrets(msg), 400)}
		return unknown
	}
	for i, n := range p.Notes {
		p.Notes[i] = clip(strings.ReplaceAll(n, token, "[token]"), 400)
	}
	if len(p.Notes) > maxProtectionNotes {
		p.Notes = p.Notes[:maxProtectionNotes]
	}
	if len(p.CheckNames) > maxCheckNames {
		p.CheckNames = p.CheckNames[:maxCheckNames]
	}
	for i, c := range p.CheckNames {
		p.CheckNames[i] = CleanLine(c, 200)
	}
	return p
}
