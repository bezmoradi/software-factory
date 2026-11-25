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
	maxCheckNames      = 30
)

type Protection struct {
	State       string
	Reviews     string
	Approvals   int
	AtLeast     bool
	CodeOwners  bool
	PullRequest bool
	Checks      string
	CheckNames  []string
	Why         string
}

type ProtectionRow struct {
	Kind   string
	Label  string
	State  string
	Value  string
	Detail string
	Names  []string
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
	Readiness        []ReadinessLine
}

type ProjectFactsPage interface {
	ProjectFacts(ctx context.Context, repo string) (ProjectFactsView, error)
	Lookups(repo string) (bool, error)
	SetLookups(repo string, on bool) (bool, error)
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
	pending pendingSteps

	verified func(project, main string) ReadinessLine
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
	f.settleLocked()
	return nil
}

func (f *ProjectFacts) settleLocked() {
	steps := f.pending.list()
	if len(steps) == 0 {
		return
	}
	st := LookupState{Projects: maps.Clone(f.on), Removed: slices.Clone(f.removed)}
	st.Removed, _ = replaySteps(steps, st.Projects, st.Removed)
	if err := f.cfg.Lookups.Save(st); err != nil {
		projectDataNotSaved(f.cfg.Logger, "project facts", "", err)
		return
	}
	f.on, f.removed = st.Projects, st.Removed
	f.pending.done(len(steps))
	f.cfg.Logger.Info("project facts", "event", "pending-project-data-applied", "steps", len(steps))
}

func (f *ProjectFacts) projectData(event string, step *projectStep, fn func(st *LookupState) bool) {
	if f.cfg.Lookups == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	failed := func(err error) {
		name := ""
		if step != nil {
			name = step.id.Name
			f.pending.remember(step.id, step.restore, step.at)
		}
		projectDataNotSaved(f.cfg.Logger, "project facts", name, err)
	}
	if err := f.loadLocked(); err != nil {
		failed(err)
		return
	}
	f.settleLocked()
	st := LookupState{Projects: maps.Clone(f.on), Removed: slices.Clone(f.removed)}
	if !fn(&st) {
		return
	}
	if err := f.cfg.Lookups.Save(st); err != nil {
		failed(err)
		return
	}
	f.on, f.removed = st.Projects, st.Removed
	f.cfg.Logger.Info("project facts", "event", event)
}

func (f *ProjectFacts) ParkProject(id ProjectID) {
	f.projectData("lookups-parked", &projectStep{id: id, at: f.cfg.Now()}, func(st *LookupState) bool {
		var parked bool
		st.Removed, _, parked = parkProject(st.Projects, st.Removed, id)
		return parked
	})
}

func (f *ProjectFacts) RestoreProject(id ProjectID) {
	f.projectData("lookups-restored", &projectStep{id: id, restore: true, at: f.cfg.Now()}, func(st *LookupState) bool {
		var changed bool
		st.Removed, changed = restoreProject(st.Projects, st.Removed, id)
		return changed
	})
}

func (f *ProjectFacts) KeepProjects(names []string) {
	f.projectData("lookups-unlisted-set-aside", nil, func(st *LookupState) bool {
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

func (f *ProjectFacts) Lookups(repo string) (bool, error) {
	if f.cfg.Repos == nil || f.cfg.Lookups == nil {
		return false, conflict("not-yet", "This engine keeps no project settings.")
	}
	if _, err := f.cfg.Repos.record(repo); err != nil {
		return false, notFound("project", "That project is no longer in the list.")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.loadLocked() != nil {
		return false, conflict("records", "The engine couldn't read its package lookup setting.")
	}
	return f.on[repo], nil
}

func (f *ProjectFacts) SetLookups(repo string, on bool) (bool, error) {
	if f.cfg.Repos == nil || f.cfg.Lookups == nil {
		return false, conflict("not-yet", "This engine keeps no project settings.")
	}
	if _, err := f.cfg.Repos.record(repo); err != nil {
		return false, notFound("project", "That project is no longer in the list.")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.loadLocked() != nil {
		return false, conflict("records", "The engine couldn't read its package lookup setting.")
	}
	f.settleLocked()
	if f.on[repo] == on {
		return false, nil
	}
	next := map[string]bool{}
	maps.Copy(next, f.on)
	if on {
		next[repo] = true
	} else {
		delete(next, repo)
	}
	if len(next) > MaxLookupProjects {
		return false, conflict("full", "The package lookup is on for too many projects.")
	}
	if err := f.cfg.Lookups.Save(LookupState{Projects: next, Removed: slices.Clone(f.removed)}); err != nil {
		return false, conflict("records", "The engine couldn't save the package lookup setting.")
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
	return true, nil
}

func (f *ProjectFacts) ProjectFacts(ctx context.Context, repo string) (ProjectFactsView, error) {
	if f.cfg.Repos == nil {
		return ProjectFactsView{}, conflict("not-yet", "This engine has no repositories.")
	}
	list, err := f.cfg.Repos.List()
	if err != nil {
		return ProjectFactsView{}, conflict("records", "The engine couldn't read its list of repositories.")
	}
	i := slices.IndexFunc(list, func(r Repository) bool { return r.Name == repo })
	if i < 0 {
		return ProjectFactsView{}, notFound("project", "That project is no longer in the list.")
	}
	r := list[i]
	out := ProjectFactsView{Repo: r.Name, Branch: r.Branch, Protection: Protection{State: ProtectionUnknown, Reviews: ProtectionUnknown, Checks: ProtectionUnknown}}
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
	out.Readiness = f.readiness(r, main, files, out.WorkflowsProblem)
	return out, nil
}

func (f *ProjectFacts) protection(ctx context.Context, r Repository) Protection {
	unknown := Protection{State: ProtectionUnknown, Reviews: ProtectionUnknown, Checks: ProtectionUnknown}
	if f.cfg.Protection == nil {
		unknown.Why = "This engine doesn't read branch protection"
		return unknown
	}
	raw, token, branch, err := f.cfg.Repos.ForgeTarget(r.Name)
	if err != nil {
		unknown.Why = "Couldn't read the repository's record or its token"
		return unknown
	}
	host, owner, name, ok := ForgeRepoOf(raw)
	if !ok || !f.cfg.Protection.Supports(host) {
		unknown.Why = "Read only from GitHub"
		return unknown
	}
	if token == "" {
		unknown.Why = "No token is saved for " + strings.TrimPrefix(host, "https://")
		return unknown
	}
	if branch == "" {
		unknown.Why = readyNotFetched
		return unknown
	}
	ctx, cancel := context.WithTimeout(ctx, protectionFor)
	defer cancel()
	p, err := f.cfg.Protection.Protection(ctx, ForgeRepo{Host: host, Owner: owner, Name: name, Token: token}, branch)
	if err != nil {
		msg := strings.ReplaceAll(err.Error(), token, "[token]")
		unknown.Why = strings.TrimSuffix(clip(maskSecrets(msg), 200), ".")
		return unknown
	}
	p.Why = clip(strings.ReplaceAll(p.Why, token, "[token]"), 200)
	if len(p.CheckNames) > maxCheckNames {
		p.CheckNames = p.CheckNames[:maxCheckNames]
	}
	for i, c := range p.CheckNames {
		p.CheckNames[i] = CleanLine(c, 200)
	}
	return p
}

func ProtectionRows(p Protection) []ProtectionRow {
	state := func(v, yes string) string {
		switch v {
		case yes:
			return ReadyFound
		case ProtectionUnknown, "":
			return ReadyUnknown
		}
		return ReadyMissing
	}
	value := map[string]string{ReadyFound: "yes", ReadyMissing: "no", ReadyUnknown: "unknown"}
	rows := []ProtectionRow{
		{Kind: "protected", Label: "Protected", State: state(p.State, ProtectionOn)},
		{Kind: "reviews", Label: "Reviews required", State: state(p.Reviews, RequiredYes)},
		{Kind: "checks", Label: "Status checks required", State: state(p.Checks, RequiredYes)},
	}
	why := p.Why
	for i := range rows {
		r := &rows[i]
		r.Value = value[r.State]
		if r.State == ReadyUnknown && why != "" {
			r.Detail, why = why, ""
		}
	}
	if rows[1].State == ReadyFound {
		least := ""
		if p.AtLeast {
			least = "at least "
		}
		switch {
		case p.Approvals > 0 && p.CodeOwners:
			rows[1].Value += ", " + least + countOf(p.Approvals, "approval", "approvals") + " and code owners"
		case p.Approvals > 0:
			rows[1].Value += ", " + least + countOf(p.Approvals, "approval", "approvals")
		case p.CodeOwners:
			rows[1].Value += ", code owners"
		}
	}
	if rows[1].State == ReadyMissing && p.PullRequest {
		rows[1].Detail = "A pull request is required, with no approval"
	}
	if rows[2].State == ReadyFound && len(p.CheckNames) > 0 {
		rows[2].Value += ", " + countOf(len(p.CheckNames), "check", "checks")
		rows[2].Names = slices.Clone(p.CheckNames)
	}
	return rows
}
