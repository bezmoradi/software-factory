package session

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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

	"github.com/bezmoradi/software-factory/internal/core/gate"
	"github.com/bezmoradi/software-factory/internal/core/level"
	"github.com/bezmoradi/software-factory/internal/core/push"
	"github.com/bezmoradi/software-factory/internal/core/review"
	"github.com/bezmoradi/software-factory/internal/core/shared"
	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

const (
	HandoffCommand = "/knackpack:handoff"
	TeamFile       = "software-factory.json"
	DefaultRounds  = 3
	maxRounds      = 20
	RoundsMax      = maxRounds
)

var (
	InstructionFiles = []string{"CLAUDE.md", ".claude/CLAUDE.md", "AGENTS.md"}
	sha256Hex        = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type WorkFiles interface {
	ChangedFiles(ctx context.Context, repo, from, to string) ([]push.Change, error)
	ReadFile(repo, commit, path string) (push.File, error)
}

type WorkRepos interface {
	List() ([]Repository, error)
	Main(name string) (string, error)
	Fresh(ctx context.Context, name string) (string, error)
}

type TeamApproval struct {
	SHA string
	At  time.Time
}

type ProjectSettings struct {
	Rounds    int
	Type      string
	Findings  bool
	PRComment bool
	Hold      HoldSettings
	Depth     workflow.Depth
}

type SavedApproval struct {
	Hash   string
	Commit string
	At     time.Time
	Text   string
}

type SavedWork struct {
	Repo         string
	Type         string
	Chosen       bool
	Requirements *SavedApproval
	Plan         *SavedApproval
	Allowed      map[string][]string
	Lock         *TestLock
	Ran          []string
}

type WorkState struct {
	Teams        map[string]TeamApproval
	Projects     map[string]ProjectSettings
	Removed      []RemovedProject[ProjectSettings]
	RemovedTeams []RemovedProject[TeamApproval]
	Sessions     map[string]SavedWork
}

type WorkStore interface {
	Load() (WorkState, error)
	Save(WorkState) error
}

type WorkConfig struct {
	Targets     func(sid string) (PushTarget, bool)
	Doubled     func(sid string) bool
	Held        func(sid string) (PushTarget, bool)
	Heads       Heads
	History     History
	Files       WorkFiles
	Bounded     BoundedFiles
	Blobs       ChangeBlobs
	Repos       WorkRepos
	Reported    func(sid string) (Workflow, bool)
	Reviews     ReviewEntries
	Store       WorkStore
	Installed   func() []string
	Contents    PushContents
	Audit       Audit
	Tell        func(sid, text string) error
	Note        func(sid, text string)
	HistoryOnly func(sid string) error
	Now         func() time.Time
	Texts       ApprovedStore
	Logger      shared.Logger

	RoleAgents  RoleAgents
	Packs       ProjectPacks
	SessionPack func(sid string) (string, bool)
	Changes     ChangeReader
	Level       func(sid string) (level.Result, bool)
	Catalog     func() workflow.Catalog

	ApprovalMoved func(sid string, at func(commit string) (ApprovalGiven, bool))

	NothingToExplain func(sid string) string
}

type workApproval struct {
	Hash   string
	Commit string
	At     time.Time
	Text   string
}

type workSession struct {
	repo    string
	typeID  string
	chosen  bool
	req     *workApproval
	plan    *workApproval
	allowed map[string]map[string]bool
	lock    *TestLock
	ask     *typeAsk
	ran     []string
}

type Work struct {
	cfg      WorkConfig
	packMu   sync.Mutex
	mu       sync.Mutex
	sessions map[string]*workSession
	store    sync.Mutex
	pending  pendingSteps
	scanMu   sync.Mutex
	scans    map[string]scanned
	scanKeys []string
	sizeMu   sync.Mutex
	sizes    map[string]changeSize
	lockShow func(sid string)

	looseMu   sync.Mutex
	loose     map[string]loosening
	looseKeys []string

	docMu    sync.Mutex
	docs     DocReader
	docSeen  map[string]string
	textMu   sync.Mutex
	textKeys map[string]string

	digestKey [32]byte
}

type WorkDoc struct {
	Path      string
	Text      string
	Hash      string
	Clipped   bool
	Approved  bool
	Changed   bool
	Previous  string
	Unread    bool
	UnreadWhy string
}

type WorkReviewer struct {
	Agent    string
	Role     review.Role
	Name     string
	Why      string
	State    string
	Verdict  string
	Round    int
	Limit    int
	Capped   bool
	Override string
	Since    string
	SetAside string
	Summary  string
}

type GateView struct {
	ExplainOff   string
	Head         string
	Branch       string
	Adapter      workflow.AdapterID
	Type         string
	Chosen       bool
	Profile      workflow.Profile
	Note         string
	Types        []workflow.WorkType
	Commands     map[workflow.Step]string
	TeamApproved bool
	TeamNote     string
	Requirements *WorkDoc
	Plan         *WorkDoc
	Research     string
	DocsFolder   string
	DocsNote     string
	Define       bool
	FixCommitted bool
	Reviewers    []WorkReviewer
	Missing      []string
	Allowed      bool
	Waiting      string
	Holds        map[string]string
	Rounds       int
	FilesListed  bool
	ChangedFiles int
	Flagged      []FlagView
	FlagsRead    bool
	LFS          []string
	Handoff      bool
	Catalog      workflow.CatalogState
	CatalogWhy   string
	Lock         LockView
	Change       *TypeChange
	Started      BranchStart
	Ask          *TypeAsk
	Ran          []string
	Early        *EarlyWork
	PushWait     string
}

type EarlyWork struct {
	Commits bool
}

func earlyWorkOf(g gathered) *EarlyWork {
	if g.chosen.Profile() != workflow.Change || g.reqOk || g.reqWas || !slices.Contains(g.chosen.Steps, workflow.Define) || g.setup.setup.Workflow.Commands[workflow.Define] == "" {
		return nil
	}
	return &EarlyWork{Commits: g.listed && len(g.files) > 0}
}

type BranchStart string

const (
	StartedUnknown BranchStart = ""
	StartedNone    BranchStart = "none"
	StartedCommits BranchStart = "commits"
	StartedNoBase  BranchStart = "nobase"
)

func startedOf(g gathered) BranchStart {
	switch {
	case g.head == "" || !g.listed:
		return StartedUnknown
	case g.base == "":
		return StartedNoBase
	case g.base == g.head:
		return StartedNone
	}
	return StartedCommits
}

type TeamView struct {
	Repo          string
	Branch        string
	Main          string
	Present       bool
	Text          string
	SHA           string
	Problems      []string
	Approved      *TeamApproval
	Same          bool
	Note          string
	Setup         *workflow.Setup
	SetupProblems []string
}

type ProjectInfo struct {
	Types        []workflow.WorkType
	Rounds       int
	Type         string
	Instructions []string
	AutoFindings bool
	PRComment    bool
	Hold         HoldSettings
}

type WorkFacts struct {
	Type          workflow.WorkType
	Required      []workflow.Requirement
	Items         []string
	NeedsApproval bool
	Rounds        int
	Refusal       string
	Hold          HoldSettings
	Approval      ApprovalGiven
	Verify        []string
}

type WorkPage interface {
	View(ctx context.Context, sid string) (GateView, error)
	SetType(ctx context.Context, sid, id string) error
	Approve(ctx context.Context, sid string, plan bool, hash, text string) error
	Team(ctx context.Context, path string) (TeamView, error)
	ApproveTeam(ctx context.Context, path, sha string) (TeamView, error)
	ForgetTeam(path string) (TeamView, error)
	Project(name string) ProjectInfo
	SetProject(ctx context.Context, path, op string, rounds int, kind string) error
	SetAutoFindings(path string, on bool) error
	SetProjectOption(path, op string, on bool) error
	SetHold(path string, h HoldSettings) error
	AllowFile(ctx context.Context, sid, path, digest, reason string) error
	AskRemove(ctx context.Context, sid, path string) error
	AllowCommit(ctx context.Context, sid, commit string, field CommitField, reason string) error
	AskRewrite(ctx context.Context, sid, commit string, field CommitField) error
	EditHold(ctx context.Context, sid, top string, paths []string) string
	SuggestType(sid, id, reason string) TypeAnswer
	Guarded(sid, top string, paths []string) string
	LockTests(ctx context.Context, sid, head string) error
	UnlockTests(sid string) error
	ReviewedFiles(sid, commit string, paths []string, limit int64) ([]ReviewedRead, error)
}

var (
	_ WorkPage    = (*Work)(nil)
	_ ProjectData = (*Work)(nil)
)

func (w *Work) loadLocked() (WorkState, error) {
	st, err := w.cfg.Store.Load()
	filled(&st)
	if err != nil {
		return st, err
	}
	steps := w.pending.list()
	if len(steps) == 0 {
		return st, nil
	}
	next := st
	next.Projects, next.Teams = maps.Clone(st.Projects), maps.Clone(st.Teams)
	next.Removed, _ = replaySteps(steps, next.Projects, slices.Clone(st.Removed))
	next.RemovedTeams, _ = replaySteps(steps, next.Teams, slices.Clone(st.RemovedTeams))
	if err := w.cfg.Store.Save(next); err != nil {
		projectDataNotSaved(w.cfg.Logger, "work", "", err)
		return st, nil
	}
	w.pending.done(len(steps))
	w.cfg.Logger.Info("work", "event", "pending-project-data-applied", "steps", len(steps))
	return next, nil
}

func (w *Work) projectData(event string, step *projectStep, fn func(st *WorkState) bool) {
	w.store.Lock()
	defer w.store.Unlock()
	failed := func(err error) {
		name := ""
		if step != nil {
			name = step.id.Name
			w.pending.remember(step.id, step.restore, step.at)
		}
		projectDataNotSaved(w.cfg.Logger, "work", name, err)
	}
	st, err := w.loadLocked()
	if err != nil {
		failed(err)
		return
	}
	if !fn(&st) {
		return
	}
	if err := w.cfg.Store.Save(st); err != nil {
		failed(err)
		return
	}
	w.cfg.Logger.Info("work", "event", event)
}

func (w *Work) ParkProject(id ProjectID) {
	w.projectData("settings-parked", &projectStep{id: id, at: w.cfg.Now()}, func(st *WorkState) bool {
		var settings, team bool
		st.Removed, _, settings = parkProject(st.Projects, st.Removed, id)
		st.RemovedTeams, _, team = parkProject(st.Teams, st.RemovedTeams, id)
		return settings || team
	})
}

func (w *Work) RestoreProject(id ProjectID) {
	w.projectData("settings-restored", &projectStep{id: id, restore: true, at: w.cfg.Now()}, func(st *WorkState) bool {
		var settings, team bool
		st.Removed, settings = restoreProject(st.Projects, st.Removed, id)
		st.RemovedTeams, team = restoreProject(st.Teams, st.RemovedTeams, id)
		return settings || team
	})
}

func (w *Work) KeepProjects(names []string) {
	w.projectData("unlisted-settings-set-aside", nil, func(st *WorkState) bool {
		var settings, team int
		st.Removed, settings = keepProjects(st.Projects, st.Removed, names)
		st.RemovedTeams, team = keepProjects(st.Teams, st.RemovedTeams, names)
		return settings+team > 0
	})
}

func NewWork(cfg WorkConfig) (*Work, error) {
	if cfg.Targets == nil || cfg.Heads == nil || cfg.History == nil || cfg.Files == nil || cfg.Repos == nil || cfg.Store == nil || cfg.Level == nil {
		return nil, errors.New("work: need the sessions, the hash-checked reader, the repositories, the state file and the sessions' levels")
	}
	if cfg.Reported == nil {
		cfg.Reported = func(string) (Workflow, bool) { return Workflow{}, false }
	}
	if cfg.Installed == nil {
		cfg.Installed = func() []string { return nil }
	}
	if cfg.Catalog == nil {
		cfg.Catalog = workflow.NoCatalog
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = quiet{}
	}
	cfg.Audit = masking(cfg.Audit)
	w := &Work{cfg: cfg, sessions: map[string]*workSession{}, scans: map[string]scanned{}, sizes: map[string]changeSize{}, loose: map[string]loosening{}, docSeen: map[string]string{}, textKeys: map[string]string{}}
	if _, err := rand.Read(w.digestKey[:]); err != nil {
		return nil, errors.New("work: no random key for the flagged files' digests")
	}
	return w, nil
}

func textHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func (w *Work) load() WorkState {
	st, _ := w.read()
	return st
}

func (w *Work) read() (WorkState, error) {
	w.store.Lock()
	defer w.store.Unlock()
	st, err := w.loadLocked()
	if err != nil {
		w.cfg.Logger.Warn("work", "event", "state-unreadable")
	}
	return st, err
}

func filled(st *WorkState) {
	if st.Teams == nil {
		st.Teams = map[string]TeamApproval{}
	}
	if st.Projects == nil {
		st.Projects = map[string]ProjectSettings{}
	}
	if st.Sessions == nil {
		st.Sessions = map[string]SavedWork{}
	}
}

func (w *Work) change(fn func(*WorkState) error) error {
	w.store.Lock()
	defer w.store.Unlock()
	st, err := w.loadLocked()
	if err != nil {
		return conflict("state", "The engine couldn't read its project settings.")
	}
	if err := fn(&st); err != nil {
		return err
	}
	if err := w.cfg.Store.Save(st); err != nil {
		return conflict("state", "The engine couldn't save its project settings.")
	}
	return nil
}

func (w *Work) repoByPath(path string) (Repository, error) {
	list, err := w.cfg.Repos.List()
	if err != nil {
		return Repository{}, conflict("records", "The engine couldn't read its list of repositories.")
	}
	i := slices.IndexFunc(list, func(r Repository) bool { return r.Path == path })
	if i < 0 {
		return Repository{}, notFound("project", "That project is no longer in the list.")
	}
	return list[i], nil
}

func (w *Work) repoByName(name string) (Repository, bool) {
	list, err := w.cfg.Repos.List()
	if err != nil {
		return Repository{}, false
	}
	i := slices.IndexFunc(list, func(r Repository) bool { return r.Name == name })
	if i < 0 {
		return Repository{}, false
	}
	return list[i], true
}

type teamRead struct {
	main     string
	present  bool
	text     string
	sha      string
	team     *workflow.Team
	problems []string
	unread   string
}

func (w *Work) readTeam(r Repository) teamRead {
	main, err := w.cfg.Repos.Main(r.Name)
	if err != nil || main == "" {
		return teamRead{unread: "the engine hasn't fetched the main branch yet"}
	}
	out := teamRead{main: main}
	f, err := w.cfg.Files.ReadFile(r.Path, main, TeamFile)
	switch {
	case errors.Is(err, push.ErrNotFound):
		return out
	case err != nil:
		out.unread = "the engine couldn't read it from the main branch's commit"
		return out
	case f.Mode != push.ModeFile:
		out.present = true
		out.problems = []string{TeamFile + " must be a plain file"}
		return out
	}
	out.present = true
	out.sha = textHash(string(f.Data))
	if len(f.Data) > workflow.MaxTeamBytes || !utf8.Valid(f.Data) {
		out.problems = []string{"the file is over 64 KiB or isn't UTF-8"}
		return out
	}
	out.text = string(f.Data)
	team, problems := workflow.ParseTeam(out.text)
	problems = append(problems, instructionProblems(team)...)
	problems = append(problems, workflow.AgentProblems(team, w.installed(r.Name))...)
	out.team, out.problems = team, problems
	if len(problems) > 0 {
		out.team = nil
	}
	return out
}

func (w *Work) teamFor(r Repository, st WorkState) (*workflow.Team, bool, string) {
	a, approved := st.Teams[r.Name]
	t := w.readTeam(r)
	switch {
	case !t.present && approved:
		return nil, false, "The team file you approved isn't on the main branch any more, so the built-in setup applies."
	case !t.present:
		return nil, false, ""
	case !approved:
		return nil, false, "This project has a team file you haven't approved, so the built-in setup applies. Review it to use it."
	case a.SHA != t.sha:
		return nil, false, "The team file changed since you approved it, so the built-in setup applies until you approve this version."
	case t.team == nil:
		return nil, false, "The approved team file can't be used now, so the built-in setup applies."
	}
	return t.team, true, ""
}

type setupOf struct {
	all      []workflow.WorkType
	setup    workflow.Setup
	problems []string
	approved bool
	note     string
	rounds   int
	project  string
}

func (w *Work) setupFor(name string) setupOf {
	st := w.load()
	personal := workflow.Personal{Rounds: DefaultRounds}
	if p, ok := st.Projects[name]; ok {
		if p.Rounds > 0 {
			personal.Rounds = p.Rounds
		}
		personal.Type = p.Type
	}
	var team *workflow.Team
	out := setupOf{}
	if r, ok := w.repoByName(name); ok {
		team, out.approved, out.note = w.teamFor(r, st)
	}
	out.setup, out.problems = workflow.SetupOf(team, personal, workflow.Knackpack)
	out.setup.Catalog = w.cfg.Catalog()
	if w.cfg.Packs != nil {
		if ps := w.cfg.Packs.PackSetup(name); ps.Kind != PackKnackpack {
			out.setup.Catalog = workflow.OffCatalog()
			for _, role := range review.Roles {
				out.setup.Roles[role] = ps.Roles[role]
			}
			out.setup.Workflow = ps.Workflow
		}
	}
	out.all = out.setup.Types
	out.setup.Adapter = workflow.AdapterID(out.setup.Workflow.Name)
	out.setup.Types = workflow.Offered(out.all, out.setup.Workflow)
	if w.cfg.RoleAgents != nil {
		maps.Copy(out.setup.Roles, w.cfg.RoleAgents.RolesFor(name))
	}
	out.rounds = out.setup.Rounds
	if _, ok := typeIn(out.setup.Types, personal.Type); ok {
		out.project = personal.Type
	}
	return out
}

func typeIn(types []workflow.WorkType, id string) (workflow.WorkType, bool) {
	i := slices.IndexFunc(types, func(t workflow.WorkType) bool { return t.ID == id })
	if i < 0 {
		return workflow.WorkType{}, false
	}
	return types[i], true
}

func (w *Work) defaultType(s workflow.Setup) string {
	return workflow.TypeOr(s.Types, s.DefaultType).ID
}

func (w *Work) Check(repo, typeID string) error {
	if typeID == "" {
		return nil
	}
	if _, ok := typeIn(w.setupFor(repo).setup.Types, typeID); !ok {
		return invalid("type", "That work type isn't one this project offers.")
	}
	return nil
}

func (w *Work) Begin(sid, repo, typeID string) {
	s := w.setupFor(repo)
	chosen := typeID != ""
	if _, ok := typeIn(s.setup.Types, typeID); !ok {
		typeID, chosen = w.defaultType(s.setup), s.project != ""
	}
	w.mu.Lock()
	w.sessions[sid] = &workSession{repo: repo, typeID: typeID, chosen: chosen}
	w.mu.Unlock()
	w.persist(sid)
	if t, ok := typeIn(s.setup.Types, typeID); ok && chosen && w.cfg.Note != nil {
		w.cfg.Note(sid, startNote(t, w.packSetup(sid, repo).setup.Adapter == workflow.Knackpack))
	}
}

func (w *Work) Has(sid string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.sessions[sid]
	return ok
}

func (w *Work) Forget(sid string) {
	w.mu.Lock()
	delete(w.sessions, sid)
	w.mu.Unlock()
	w.persist(sid)
	w.forgetDocs(sid)
}

func (w *Work) session(sid string) (workSession, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.sessions[sid]
	if s == nil {
		return workSession{}, false
	}
	c := *s
	c.ran = slices.Clone(s.ran)
	c.allowed = map[string]map[string]bool{}
	for p, set := range s.allowed {
		c.allowed[p] = maps.Clone(set)
	}
	return c, true
}

func (w *Work) SetType(ctx context.Context, sid, id string) error {
	if err := w.unusable(sid); err != nil {
		return err
	}
	ws, ok := w.session(sid)
	if !ok {
		return conflict("not started here", "Only a session this engine started has a work type here.")
	}
	t, ok := typeIn(w.packSetup(sid, ws.repo).setup.Types, id)
	if !ok {
		return invalid("type", "That work type isn't one this project offers.")
	}
	setup := w.packSetup(sid, ws.repo).setup
	knackpack := setup.Adapter == workflow.Knackpack
	told := ""
	w.mu.Lock()
	if s := w.sessions[sid]; s != nil {
		told = typeNote(*s, t, knackpack)
		if workflow.TypeOr(setup.Types, s.typeID).Plans() != t.Plans() {
			s.req, s.plan = nil, nil
		}
		s.typeID, s.chosen, s.ask = id, true, nil
	}
	w.mu.Unlock()
	w.persist(sid)
	if told != "" && w.cfg.Note != nil {
		w.cfg.Note(sid, told)
	}
	w.recheckApproval(ctx, sid)
	w.cfg.Logger.Info("work type", "type", id)
	return nil
}

type docSlot struct {
	doc  *WorkDoc
	path string
	why  string
}

type workDocs struct {
	req      docSlot
	plan     docSlot
	research string
	folder   string
}

func shownDoc(path, text string) *WorkDoc {
	d := &WorkDoc{Path: path, Text: text}
	if utf16Len(text) > MaxWorkflowText {
		d.Text, d.Clipped = clip(text, MaxWorkflowText), true
	}
	d.Hash = textHash(d.Text)
	return d
}

func readSlot(r DocReader, sid, name string) docSlot {
	if r == nil || name == "" {
		return docSlot{}
	}
	got := r.ReadDoc(sid, name)
	out := docSlot{path: got.Path, why: got.Why}
	if got.State == DocFound {
		out.doc = shownDoc(got.Path, got.Text)
	}
	return out
}

func docNames(def workflow.Adapter, p workflow.Profile) (string, string) {
	switch p {
	case workflow.Change:
		return def.Documents.Requirements, def.Documents.Plan
	case workflow.Incident:
		return "", def.Documents.RootCause
	}
	return "", ""
}

func (w *Work) UseDocs(r DocReader) {
	w.docMu.Lock()
	w.docs = r
	w.docMu.Unlock()
}

func (w *Work) docReader() DocReader {
	w.docMu.Lock()
	defer w.docMu.Unlock()
	return w.docs
}

func (w *Work) readDocs(sid string, def workflow.Adapter, p workflow.Profile) workDocs {
	out := workDocs{}
	if rep, ok := w.cfg.Reported(sid); ok && rep.Research != nil {
		out.research = rep.Research.Path
	}
	r := w.docReader()
	if r == nil {
		return out
	}
	out.folder = r.DocsPath(sid)
	req, plan := docNames(def, p)
	out.req, out.plan = readSlot(r, sid, req), readSlot(r, sid, plan)
	return out
}

func (s docSlot) key() string {
	if s.doc != nil {
		return s.doc.Hash
	}
	return s.why
}

type gathered struct {
	sid        string
	ws         workSession
	t          PushTarget
	head       string
	main       string
	base       string
	files      []string
	listed     bool
	note       string
	setup      setupOf
	adapter    workflow.AdapterID
	chosen     workflow.WorkType
	typeNote   string
	docs       workDocs
	reqOk      bool
	reqWas     bool
	planNeeded bool
	planOk     bool
	planWas    bool
	workBase   string
	built      bool
	required   []workflow.Requirement
	items      []string
	toApprove  bool
	headError  string
	scan       scanned
	flags      []FlagView
	flagsRead  bool
	specs      bool
	net        []push.Change
}

func (w *Work) gather(ctx context.Context, sid string, ws workSession, t PushTarget, head, main string) gathered {
	g := gathered{sid: sid, ws: ws, t: t, head: head, main: main}
	g.setup = w.packSetup(sid, ws.repo)
	g.adapter = g.setup.setup.Adapter
	kind := workflow.TypeOr(g.setup.setup.Types, ws.typeID)
	if kind.ID != ws.typeID {
		g.typeNote = "This session's work type isn't offered any more, so a Code change's checks apply."
	}
	g.chosen = kind
	if head != "" {
		g.specs = w.specsProtected(t, main, head)
		if main != "" {
			base, err := w.cfg.History.MergeBase(ctx, t.Repo, head, main)
			if err != nil {
				g.note = "the engine couldn't find where the branch leaves the main branch"
			}
			g.base = base
			if err == nil {
				g.listed = true
			}
		} else {
			g.listed = true
		}
		if g.listed {
			changes, err := w.cfg.Files.ChangedFiles(ctx, t.Repo, g.base, head)
			if err != nil {
				g.listed = false
				g.note = "the engine's reader couldn't compare the branch with the main branch"
			}
			for _, c := range changes {
				g.files = append(g.files, c.Path)
			}
			g.net = changes
		}
	}
	g.docs = w.readDocs(sid, g.setup.setup.Workflow, g.chosen.Profile())
	change := g.chosen.Profile() == workflow.Change
	counts := func(a *workApproval, s docSlot) bool {
		return a != nil && a.Text != "" && (s.doc == nil || s.doc.Hash == a.Hash)
	}
	g.reqOk = counts(ws.req, g.docs.req)
	g.reqWas = ws.req != nil && !g.reqOk
	g.planNeeded = g.chosen.Plans()
	g.planOk = counts(ws.plan, g.docs.plan) && (!change || g.reqOk && g.planNeeded)
	g.planWas = ws.plan != nil && !g.planOk
	base := ""
	switch {
	case g.chosen.Profile() == workflow.Incident && g.planOk:
		base = ws.plan.Commit
	case change && g.reqOk && g.planNeeded && g.planOk:
		base, g.workBase = ws.plan.Commit, ws.plan.Commit
	case change && g.reqOk && !g.planNeeded:
		base, g.workBase = ws.req.Commit, ws.req.Commit
	}
	if base != "" && head != "" && head != base {
		in, err := w.cfg.History.IsAncestor(ctx, t.Repo, base, head)
		g.built = err == nil && in
	}
	g.required = w.deepened(ctx, t, workflow.Required(g.setup.setup, g.chosen, g.files), g.base, head, g.listed)
	planFile := ""
	if g.docs.plan.doc != nil || g.planOk {
		planFile = cmpOr(g.docs.plan.path, "the approved "+planWord(g))
	}
	flagsRead := true
	g.flagsRead = true
	var flagged []workflow.Flag
	if head != "" {
		if g.listed {
			g.scan = w.scanOf(ctx, t.Repo, g.base, head, w.reviewedOn(t.Branch, head), g.specs)
			if g.specs {
				g.scan = w.withSpecs(t, head, g.scan, g.net)
			}
		}
		flagsRead = g.listed && g.scan.complete
		g.flagsRead = flagsRead
		refused := w.carriedDocs(g)
		for _, f := range g.scan.flags {
			if _, doc := slices.BinarySearch(refused, f.Path); doc && f.Commit == "" {
				continue
			}
			v := FlagView{Flag: f, Shown: f.maskedPath(), Allowed: allowedFlag(f, ws.allowed), Digest: w.flagDigest(f), Spec: g.specs && f.Commit == "" && gate.SpecPath(f.Path)}
			g.flags = append(g.flags, v)
			flagged = append(flagged, workflow.Flag{Path: f.Label(), Why: f.Why, Allowed: v.Allowed, Fixed: f.Link})
		}
	}
	g.items, g.toApprove = workflow.Items(workflow.Progress{
		Type: g.chosen, Adapter: g.adapter, Commands: g.setup.setup.Workflow.Commands,
		FilesListed: g.listed || head == "", FilesNote: g.note, FlagsRead: flagsRead, Flagged: flagged,
		ReqOk: g.reqOk, ReqWasApproved: g.reqWas, PlanNeeded: g.planNeeded, PlanFile: planFile,
		PlanOk: g.planOk, PlanWasApproved: g.planWas, Base: g.workBase, Built: g.built,
		ChangesCode: head != "" && (!g.listed || len(workflow.NotDocs(g.files)) > 0),
	})
	if item := specOverItem(g); item != "" {
		g.items = append([]string{item}, g.items...)
	}
	if g.specs && !flagsRead {
		g.items = append(g.items, "a check of this branch's changes to Proctor's specifications under proctor/ and to the root Makefile: this project uses Proctor, and they may be among the files and commits the gate couldn't read, so the developer has to check them too before allowing the push")
	}
	g.required = w.notInstalled(ws.repo, g.required)
	for _, q := range g.required {
		if q.Missing {
			g.items = append(g.items, "a review by "+q.Agent+", which no plugin installed in the reviewer image provides")
		}
	}
	for _, p := range g.setup.problems {
		g.items = append(g.items, "a setup the gate can read: "+p)
	}
	return g
}

func (w *Work) PushFacts(ctx context.Context, sid string, t PushTarget, head, main string) (WorkFacts, bool) {
	ws, ok := w.session(sid)
	if !ok {
		return WorkFacts{}, false
	}
	g := w.gather(ctx, sid, ws, t, head, main)
	hold, err := w.holdOf(t.Name)
	items := g.items
	if err != nil {
		items = append(slices.Clone(items), workUnreadItem)
	}
	refusal := w.docsRefusal(g)
	if refusal == "" {
		refusal = lfsRefusal(g.scan.lfs)
	}
	if refusal == "" {
		refusal = linkRefusal(g)
	}
	return WorkFacts{Type: g.chosen, Required: g.required, Items: items, NeedsApproval: g.toApprove, Rounds: g.setup.rounds, Refusal: refusal, Hold: hold,
		Approval: w.givenOf(g), Verify: slices.Clone(g.setup.setup.Verify)}, true
}

func (w *Work) VerifyKinds(project string) []string {
	return slices.Clone(w.setupFor(project).setup.Verify)
}

const workUnreadItem = "project settings the engine can read: it couldn't read them (work.json), so it can't tell this project's push hold, review rounds or work type"

func lfsRefusal(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	shown := make([]string, 0, 5)
	for _, p := range paths[:min(len(paths), 5)] {
		shown = append(shown, Escape([]byte(maskLines(p, true))))
	}
	more := ""
	if len(paths) > 5 {
		more = " and " + strconv.Itoa(len(paths)-5) + " more"
	}
	return "this push carries files the commit's .gitattributes give to Git LFS (" + strings.Join(shown, ", ") + more + "), and the engine's push sends git objects only, so the remote would get LFS pointers without their content; take those files out of Git LFS in .gitattributes, or push this work another way"
}

func (w *Work) Reviewers(ctx context.Context, sid string, t PushTarget, base, head string) []workflow.Requirement {
	ws, ok := w.session(sid)
	if !ok {
		ws = workSession{repo: t.Name, typeID: workflow.DefaultType}
	}
	setup := w.packSetup(sid, ws.repo)
	kind := workflow.TypeOr(setup.setup.Types, ws.typeID)
	changes, err := w.cfg.Files.ChangedFiles(ctx, t.Repo, base, head)
	var files []string
	for _, c := range changes {
		files = append(files, c.Path)
	}
	required := w.notInstalled(ws.repo, w.deepened(ctx, t, workflow.Required(setup.setup, kind, files), base, head, err == nil))
	return append(required, workflow.Advisory(required, w.depthOf(t.Name))...)
}

func ReviewerName(q workflow.Requirement) string {
	if q.Role != "" {
		return q.Role.Name()
	}
	return strings.TrimPrefix(q.Agent, "knackpack:")
}

func (w *Work) current(ctx context.Context, sid string) (gathered, error) {
	return w.currentOf(ctx, sid, w.cfg.Targets)
}

func (w *Work) currentOf(ctx context.Context, sid string, lookup func(sid string) (PushTarget, bool)) (gathered, error) {
	ws, ok := w.session(sid)
	if !ok {
		return gathered{}, conflict("not started here", "Only a session this engine started has a workflow here.")
	}
	t, ok := lookup(sid)
	if !ok {
		return gathered{}, notStartedHere(w.cfg.Doubled, sid, "Only a session this engine started has a workflow here.")
	}
	head, err := w.cfg.Heads.Head(t.Repo, t.Branch)
	headError := ""
	if err != nil {
		head, headError = "", "the engine couldn't read the session's branch "+t.Branch
	}
	main, _ := w.cfg.Repos.Main(t.Name)
	g := w.gather(ctx, sid, ws, t, head, main)
	g.headError = headError
	return g, nil
}

func (w *Work) facts(g gathered) workflow.Facts {
	f := workflow.Facts{Profile: g.chosen.Profile(), PlanNeeded: g.planNeeded, Head: g.head, ReqOk: g.reqOk, PlanOk: g.planOk,
		DocsFolder: g.docs.folder, Untyped: !g.ws.chosen}
	for _, t := range g.setup.setup.Types {
		f.Offered = append(f.Offered, t.ID)
	}
	if d := g.docs.req.doc; d != nil {
		f.ReqHash = d.Hash
		f.Brief = w.briefCheck(g.adapter, d.Text)
	}
	if d := g.docs.plan.doc; d != nil {
		f.PlanFile, f.PlanHash = d.Path, d.Hash
		f.CauseStatus = workflow.RootCauseStatus(d.Text)
	}
	if g.reqOk && g.ws.req != nil {
		f.ReqBaseline = g.ws.req.Commit
	}
	return f
}

func (w *Work) briefCheck(adapter workflow.AdapterID, text string) workflow.BriefCheck {
	c := workflow.RequirementsReadiness(text)
	if c.Kind == workflow.BriefNone && (adapter != workflow.Knackpack || w.cfg.Packs == nil || !workflow.WritesRequirementsFormat(w.cfg.Packs.KnackpackVersion)) {
		return workflow.BriefCheck{}
	}
	return c
}

func (w *Work) Approve(ctx context.Context, sid string, plan bool, hash, text string) error {
	if !sha256Hex.MatchString(hash) || textHash(text) != hash {
		return invalid("hash", "The text the page showed doesn't match its hash. Reload the page and read it again.")
	}
	g, err := w.current(ctx, sid)
	if err != nil {
		return err
	}
	if !plan && g.chosen.Profile() != workflow.Change {
		return conflict("no define", "This session's work type has no Define step, so it has no requirements to approve.")
	}
	slot, what := g.docs.req, "requirements"
	if plan {
		slot, what = g.docs.plan, planWord(g)
	}
	doc := slot.doc
	switch {
	case doc == nil && slot.why != "":
		return conflict("unreadable", "The engine couldn't read the "+what+" in the session's documents folder ("+slot.path+"): "+slot.why+". Ask the agent to save it there again as plain text, then approve.")
	case doc == nil:
		return conflict("nothing", "There's no "+what+" to approve yet.")
	case doc.Clipped:
		return conflict("too long", "The "+what+" is longer than the page shows, so it can't be approved as a whole. Ask for a shorter one.")
	case doc.Hash != hash:
		return conflict("changed", "The "+what+" changed before your approval arrived. Read the new version and approve it again.")
	case g.head == "":
		return conflict("head", "The "+what+" weren't approved: the engine couldn't read the session's branch.")
	}
	if !plan && g.reqOk && g.built {
		return conflict("built", "Work is committed since your last approval.")
	}
	f := w.facts(g)
	kind := workflow.ApproveRequirements
	if plan {
		kind = workflow.ApprovePlan
	}
	if why := workflow.Decide(f, workflow.Action{Kind: kind, Hash: hash}); why != "" {
		return conflict("held", why)
	}
	a := &workApproval{Hash: hash, Commit: g.head, At: w.cfg.Now(), Text: text}
	var unlocked *TestLock
	w.mu.Lock()
	if s := w.sessions[sid]; s != nil {
		if plan {
			s.plan = a
		} else {
			s.req = a
			s.plan = nil
		}
		unlocked, s.lock = s.lock, nil
	}
	w.mu.Unlock()
	w.persist(sid)
	w.approved(sid, g.t, g.head, plan, hash, what)
	if unlocked != nil {
		w.unlocked(sid, g.t, unlocked, "the developer approved the "+what+" again, which clears the tests locked at commit "+shortSHA(unlocked.Commit))
	}
	w.recheckApproval(ctx, sid)
	w.cfg.Logger.Info("approved", "what", what)
	if next := nextStep(g, plan); next != "" && w.cfg.Tell != nil {
		if err := w.cfg.Tell(sid, next); err != nil {
			w.cfg.Logger.Warn("approved", "what", what, "event", "next-step-not-sent")
		}
	}
	return nil
}

func (w *Work) ApprovedRequirements(ctx context.Context, sid string) string {
	g, err := w.currentOf(ctx, sid, func(sid string) (PushTarget, bool) { return targetOrHeld(w.cfg.Targets, w.cfg.Held, sid) })
	if err != nil || !g.reqOk || g.ws.req == nil {
		return ""
	}
	return g.ws.req.Text
}

func (w *Work) RecheckApproval(sid string) {
	w.recheckApproval(context.Background(), sid)
}

func (w *Work) recheckApproval(ctx context.Context, sid string) {
	if w.cfg.ApprovalMoved != nil {
		w.cfg.ApprovalMoved(sid, func(commit string) (ApprovalGiven, bool) { return w.approvalAt(ctx, sid, commit) })
	}
}

func (w *Work) approvalAt(ctx context.Context, sid, commit string) (ApprovalGiven, bool) {
	t, ok := w.cfg.Targets(sid)
	if !ok {
		return ApprovalGiven{}, false
	}
	ws, ok := w.session(sid)
	if !ok {
		return ApprovalGiven{}, false
	}
	main, _ := w.cfg.Repos.Main(t.Name)
	return w.givenOf(w.gather(ctx, sid, ws, t, commit, main)), true
}

func (w *Work) EditHold(ctx context.Context, sid, top string, paths []string) string {
	if top == "" {
		return ""
	}
	var rels []string
	for _, p := range paths {
		rel, err := filepath.Rel(top, p)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
			continue
		}
		rels = append(rels, filepath.ToSlash(rel))
	}
	if why := w.docWrite(sid, rels); why != "" {
		return why
	}
	g, err := w.current(ctx, sid)
	if err != nil {
		return ""
	}
	f := w.facts(g)
	for _, rel := range rels {
		if why := workflow.Decide(f, workflow.Action{Kind: workflow.EditFile, Path: rel}); why != "" {
			if f.Untyped {
				w.heldForType(sid)
			}
			return why
		}
	}
	return ""
}

const (
	implementText = "The developer approved the plan on the software factory page. Implement it now, then commit and call factory_push. Each commit message says what changed and why."
	noPlanText    = "The developer approved the requirements on the software factory page, and this work type has no Plan step. Implement them now, then commit and call factory_push. Each commit message says what changed and why."
)

func planWord(g gathered) string {
	if g.chosen.Profile() == workflow.Incident {
		return "root cause"
	}
	return "plan"
}

func nextStep(g gathered, plan bool) string {
	if g.chosen.Profile() == workflow.Incident {
		return ""
	}
	want, text := workflow.Implement, implementText
	switch {
	case !plan && g.planNeeded:
		want, text = workflow.Plan, ""
	case !plan:
		text = noPlanText
	}
	if !slices.Contains(g.chosen.Steps, want) {
		return ""
	}
	if c := g.setup.setup.Workflow.Commands[want]; c != "" {
		return c
	}
	return text
}

const WorkNotInstalled = "not-installed"

func (w *Work) installed(project string) []string {
	if w.cfg.Packs != nil {
		if ps := w.cfg.Packs.PackSetup(project); ps.Kind != PackKnackpack {
			return ps.Agents
		}
	}
	if w.cfg.Installed == nil {
		return nil
	}
	return w.cfg.Installed()
}

func (w *Work) packKnown(project string) bool {
	return w.cfg.Packs != nil && w.cfg.Packs.PackSetup(project).Kind != PackKnackpack
}

func (w *Work) Pushes(sid, repo string) bool {
	ws, ok := w.session(sid)
	if !ok {
		return false
	}
	s := w.packSetup(sid, repo)
	return workflow.TypeOr(s.setup.Types, ws.typeID).Push
}

func (w *Work) UseSessionPacks(fn func(sid string) (string, bool)) {
	w.packMu.Lock()
	w.cfg.SessionPack = fn
	w.packMu.Unlock()
}

func (w *Work) sessionPackOf(sid string) (string, bool) {
	w.packMu.Lock()
	fn := w.cfg.SessionPack
	w.packMu.Unlock()
	if fn == nil {
		return "", false
	}
	return fn(sid)
}

func (w *Work) packSetup(sid, repo string) setupOf {
	s := w.setupFor(repo)
	if w.cfg.Packs == nil {
		return s
	}
	choice, ok := w.sessionPackOf(sid)
	if !ok {
		return s
	}
	def := w.cfg.Packs.WorkflowOf(choice)
	s.setup.Workflow, s.setup.Adapter = def, workflow.AdapterID(def.Name)
	s.setup.Types = workflow.Offered(s.all, def)
	return s
}

func (w *Work) notInstalled(project string, list []workflow.Requirement) []workflow.Requirement {
	installed := w.installed(project)
	if len(installed) == 0 && !w.packKnown(project) {
		return list
	}
	for i, q := range list {
		list[i].Missing = q.Role == "" && !slices.Contains(installed, q.Agent)
	}
	return list
}

func (w *Work) reviewerStates(g gathered) ([]WorkReviewer, []string) {
	var entries map[review.Key]ReviewEntry
	if w.cfg.Reviews != nil {
		entries, _ = w.cfg.Reviews.Entries()
	}
	w.givenOf(g).hold(entries, g.t.Branch, g.head)
	records := map[review.Key]review.Record{}
	for k, e := range entries {
		records[k] = e.Record
	}
	limit := roundLimit(g.setup.rounds)
	var out []WorkReviewer
	var missing []string
	for _, q := range g.required {
		r := WorkReviewer{Agent: q.Agent, Role: q.Role, Name: ReviewerName(q), Why: q.Why, State: "none", Limit: limit, Summary: g.setup.setup.Catalog.Summary(q.Agent)}
		rd := RoundOf(entries, g.t.Branch, q.Agent, false)
		r.Round, r.Capped = rd.Count, rd.Capped(limit)
		k := review.Key{Commit: g.head, Branch: g.t.Branch, Agent: q.Agent}
		if e, complete, ok := pooledEntry(entries, k, q.RunCount()); ok {
			r.State, r.Verdict, r.Override, r.Since = string(e.Record.State), e.Record.Verdict, e.Record.Override, e.Since
			if e.Record.Channel == review.ApprovalChanged && e.Record.State == review.Unknown {
				r.SetAside = e.Record.Held
			}
			if !complete && e.Record.State == review.Pass {
				r.State = "none"
			}
		} else {
			for key := range entries {
				if key.Branch == g.t.Branch && key.Agent == q.Agent {
					r.State = "stale"
					break
				}
			}
		}
		if q.Missing {
			r.State = WorkNotInstalled
		}
		if g.head != "" && !q.Missing {
			offer := offerFor(w.cfg.RoleAgents, q.Agent)
			if why, _ := review.Pooled(records, k, q.RunCount(), &offer); why != "" {
				if r.Capped {
					why = gate.CappedItem(q.Agent, rd.Count)
				}
				missing = append(missing, why)
			}
		}
		out = append(out, r)
	}
	return out, missing
}

func (w *Work) View(ctx context.Context, sid string) (GateView, error) {
	g, err := w.currentOf(ctx, sid, func(sid string) (PushTarget, bool) { return targetOrHeld(w.cfg.Targets, w.cfg.Held, sid) })
	if err != nil {
		return GateView{}, err
	}
	v := GateView{Head: g.head, Branch: g.t.Branch, Adapter: g.adapter, Type: g.chosen.ID, Chosen: g.ws.chosen, Profile: g.chosen.Profile(), Note: g.typeNote,
		Types: g.setup.setup.Types, Commands: g.setup.setup.Workflow.Commands, TeamApproved: g.setup.approved, TeamNote: g.setup.note,
		Research: g.docs.research, DocsFolder: g.docs.folder, Rounds: g.setup.rounds, FilesListed: g.listed, ChangedFiles: len(g.files)}
	if w.cfg.NothingToExplain != nil && g.head != "" && w.cfg.NothingToExplain(sid) == g.head {
		v.ExplainOff = SaidNothingToExplain
	}
	v.Requirements = viewDoc(g.docs.req, g.ws.req, g.reqOk, g.reqWas)
	v.Plan = viewDoc(g.docs.plan, g.ws.plan, g.planOk, g.planWas)
	v.DocsNote = docsNote([]docSlot{g.docs.req, g.docs.plan}, []*WorkDoc{v.Requirements, v.Plan})
	change := g.chosen.Profile() == workflow.Change
	v.Define = change && g.reqOk
	v.FixCommitted = g.chosen.Profile() == workflow.Incident && g.built
	reviewers, missing := w.reviewerStates(g)
	v.Reviewers = reviewers
	v.Catalog, v.CatalogWhy = g.setup.setup.Catalog.State, g.setup.setup.Catalog.Reason
	v.Lock = w.lockView(ctx, g)
	v.Flagged, v.FlagsRead = g.flags, g.flagsRead
	v.Started = startedOf(g)
	v.Ask = w.typeAskOf(g)
	v.Ran = slices.Clone(g.ws.ran)
	v.Early = earlyWorkOf(g)
	if g.head != "" {
		if tc := w.typeChange(g.t.Repo, g.chosen, g.setup.setup.Protect, g.specs, g.base, g.head, g.net, g.listed); len(tc.Lines) > 0 {
			v.Change = &tc
		}
	}
	for _, p := range g.scan.lfs {
		v.LFS = append(v.LFS, maskLines(p, true))
	}
	for _, r := range reviewers {
		v.Handoff = v.Handoff || r.Capped
	}
	v.Missing = append([]string{}, g.items...)
	refused := linkRefusal(g) != ""
	for _, why := range []string{w.docsRefusal(g), lfsRefusal(g.scan.lfs)} {
		if why != "" {
			v.Missing = append(v.Missing, "a push the engine can make: "+why)
			refused = true
		}
	}
	if g.headError != "" {
		v.Missing = append([]string{"the current commit, which " + g.headError}, v.Missing...)
	}
	if !g.chosen.Push {
		v.Missing = append(v.Missing, "a work type that pushes, picked on the page")
	}
	lv, _ := w.cfg.Level(sid)
	unsandboxed := gate.SandboxItem(lv) != ""
	if unsandboxed {
		v.Missing = append(v.Missing, gate.SandboxItem(lv))
	}
	v.Missing = append(v.Missing, missing...)
	v.Allowed = len(v.Missing) == 0
	f := w.facts(g)
	v.Waiting = workflow.ImplementWait(workflow.Build{Profile: g.chosen.Profile(), Head: g.head != "", ReqOk: g.reqOk, PlanNeeded: g.planNeeded, PlanOk: g.planOk,
		Built: g.built, Commits: len(g.files), CommitsRead: g.listed, NoBase: g.main == ""})
	v.PushWait = pushWaitOf(v, g.chosen.Push, refused, unsandboxed)
	v.Holds = map[string]string{
		"define": workflow.Decide(f, workflow.Action{Kind: workflow.StartDefine}),
		"plan":   workflow.Decide(f, workflow.Action{Kind: workflow.StartPlan}),
	}
	if change && v.Requirements != nil && !g.reqOk {
		v.Holds["approveRequirements"] = workflow.Decide(f, workflow.Action{Kind: workflow.ApproveRequirements, Hash: v.Requirements.Hash})
	}
	if v.Plan != nil && !g.planOk {
		v.Holds["approvePlan"] = workflow.Decide(f, workflow.Action{Kind: workflow.ApprovePlan, Hash: v.Plan.Hash})
	}
	return v, nil
}

func viewDoc(s docSlot, a *workApproval, ok, was bool) *WorkDoc {
	switch {
	case s.doc != nil:
		c := *s.doc
		c.Approved, c.Changed = ok, was
		if was && a != nil {
			c.Previous = a.Text
		}
		return &c
	case ok && a != nil:
		c := shownDoc(s.path, a.Text)
		c.Approved, c.Unread, c.UnreadWhy = true, true, s.why
		return c
	}
	return nil
}

func docsNote(slots []docSlot, shown []*WorkDoc) string {
	var out []string
	for i, s := range slots {
		if s.doc == nil && s.why != "" && shown[i] == nil {
			out = append(out, "The engine couldn't read "+path.Base(s.path)+" in the session's documents folder ("+s.path+"): "+s.why+", so it shows nothing to approve from it.")
		}
	}
	return strings.Join(out, " ")
}

func (w *Work) teamView(r Repository) TeamView {
	st := w.load()
	t := w.readTeam(r)
	v := TeamView{Repo: r.Name, Branch: r.Branch, Main: t.main, Present: t.present, Text: t.text, SHA: t.sha, Problems: t.problems}
	if t.unread != "" {
		v.Note = "The team file couldn't be read: " + t.unread + "."
	}
	if a, ok := st.Teams[r.Name]; ok {
		approved := a
		v.Approved = &approved
		v.Same = a.SHA == t.sha && t.present
	}
	if t.team != nil {
		personal := workflow.Personal{Rounds: DefaultRounds}
		if p, ok := st.Projects[r.Name]; ok {
			if p.Rounds > 0 {
				personal.Rounds = p.Rounds
			}
			personal.Type = p.Type
		}
		s, problems := workflow.SetupOf(t.team, personal, workflow.Knackpack)
		v.Setup, v.SetupProblems = &s, problems
	}
	return v
}

func (w *Work) fetched(ctx context.Context, path string) (Repository, error) {
	r, err := w.repoByPath(path)
	if err != nil {
		return Repository{}, err
	}
	if _, err := w.cfg.Repos.Fresh(ctx, r.Name); err != nil {
		if e, ok := errors.AsType[*Error](err); ok {
			return Repository{}, e
		}
		return Repository{}, conflict("fetch", "The engine couldn't fetch the main branch, so it can't read the team file there.")
	}
	return r, nil
}

func (w *Work) Team(ctx context.Context, path string) (TeamView, error) {
	r, err := w.fetched(ctx, path)
	if err != nil {
		return TeamView{}, err
	}
	return w.teamView(r), nil
}

func (w *Work) ApproveTeam(ctx context.Context, path, sha string) (TeamView, error) {
	r, err := w.fetched(ctx, path)
	if err != nil {
		return TeamView{}, err
	}
	t := w.readTeam(r)
	switch {
	case !t.present:
		return TeamView{}, conflict("no team file", "The main branch has no "+TeamFile+" to approve.")
	case t.sha != sha:
		return TeamView{}, conflict("changed", "The team file changed on the main branch before your approval arrived. Read it again.")
	case t.team == nil:
		return TeamView{}, conflict("problems", "The team file has problems, so it can't be approved. Fix them on the main branch first.")
	}
	if err := w.change(func(st *WorkState) error {
		st.Teams[r.Name] = TeamApproval{SHA: sha, At: w.cfg.Now()}
		return nil
	}); err != nil {
		return TeamView{}, err
	}
	w.teamAudit(r.Name, AuditApproveTeam, sha)
	w.recheckProject(ctx, r.Name)
	shared.For(ctx, w.cfg.Logger).Info("team file approved")
	return w.teamView(r), nil
}

func (w *Work) ForgetTeam(path string) (TeamView, error) {
	ctx := context.Background()
	r, err := w.repoByPath(path)
	if err != nil {
		return TeamView{}, err
	}
	forgotten := ""
	if err := w.change(func(st *WorkState) error {
		forgotten = st.Teams[r.Name].SHA
		delete(st.Teams, r.Name)
		return nil
	}); err != nil {
		return TeamView{}, err
	}
	if forgotten != "" {
		w.teamAudit(r.Name, AuditForgetTeam, forgotten)
		w.recheckProject(ctx, r.Name)
	}
	w.cfg.Logger.Info("team file forgotten")
	return w.teamView(r), nil
}

func (w *Work) Project(name string) ProjectInfo {
	st := w.load()
	out := ProjectInfo{Rounds: DefaultRounds, Instructions: []string{}}
	if p, ok := st.Projects[name]; ok {
		if p.Rounds > 0 {
			out.Rounds = p.Rounds
		}
		out.Type, out.PRComment = p.Type, p.PRComment
		out.AutoFindings = p.Findings
		out.Hold = p.Hold
	}
	r, ok := w.repoByName(name)
	if !ok {
		return out
	}
	out.Types = w.setupFor(name).setup.Types
	main, err := w.cfg.Repos.Main(name)
	if err != nil || main == "" {
		return out
	}
	for _, f := range InstructionFiles {
		if file, err := w.cfg.Files.ReadFile(r.Path, main, f); err == nil && file.Mode == push.ModeFile {
			out.Instructions = append(out.Instructions, f)
		}
	}
	return out
}

func (w *Work) SetProject(ctx context.Context, path, op string, rounds int, kind string) error {
	r, err := w.repoByPath(path)
	if err != nil {
		return err
	}
	switch op {
	case "rounds":
		if rounds < 1 || rounds > maxRounds {
			return invalid("rounds", "Review rounds go from 1 to 20.")
		}
	case "type":
		if kind != "" {
			if _, ok := typeIn(w.setupFor(r.Name).setup.Types, kind); !ok {
				return invalid("type", "That work type isn't one this project offers.")
			}
		}
	default:
		return invalid("op", "The engine keeps a project's review rounds and default work type only.")
	}
	if err := w.change(func(st *WorkState) error {
		p := st.Projects[r.Name]
		if op == "rounds" {
			p.Rounds = rounds
		} else {
			p.Type = kind
		}
		st.Projects[r.Name] = p
		return nil
	}); err != nil {
		return err
	}
	if op == "type" {
		w.recheckProject(ctx, r.Name)
	}
	return nil
}

func (w *Work) recheckProject(ctx context.Context, name string) {
	w.mu.Lock()
	var sids []string
	for sid, s := range w.sessions {
		if s != nil && s.repo == name {
			sids = append(sids, sid)
		}
	}
	w.mu.Unlock()
	for _, sid := range sids {
		w.recheckApproval(ctx, sid)
	}
}

func (w *Work) reviewedOn(branch, head string) []string {
	if w.cfg.Reviews == nil {
		return nil
	}
	entries, err := w.cfg.Reviews.Entries()
	if err != nil {
		return nil
	}
	latest := map[string]time.Time{}
	for k, e := range entries {
		if k.Branch != branch || k.Commit == head || e.Record.State != review.Pass && e.Record.State != review.Block {
			continue
		}
		if at, ok := latest[k.Commit]; !ok || e.StartedAt.After(at) {
			latest[k.Commit] = e.StartedAt
		}
	}
	out := slices.Collect(maps.Keys(latest))
	slices.SortFunc(out, func(a, b string) int {
		if c := latest[b].Compare(latest[a]); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	return out[:min(len(out), maxReviewedTrees)]
}

func (w *Work) scanOf(ctx context.Context, repo, base, head string, reviewed []string, specs bool) scanned {
	if w.cfg.Contents == nil {
		return scanned{}
	}
	key := repo + "\x00" + base + "\x00" + head + "\x00" + strings.Join(reviewed, ",") + "\x00" + strconv.FormatBool(specs)
	w.scanMu.Lock()
	defer w.scanMu.Unlock()
	if c, ok := w.scans[key]; ok {
		return c
	}
	set, err := w.cfg.Contents.Pushed(ctx, repo, base, head, reviewed, FlagBudget)
	if err != nil {
		w.cfg.Logger.Warn("flag scan", "event", "unreadable")
		return scanned{failed: true}
	}
	out := scanPushed(set, specs)
	w.scans[key] = out
	w.scanKeys = append(w.scanKeys, key)
	if len(w.scanKeys) > keptScans {
		delete(w.scans, w.scanKeys[0])
		w.scanKeys = w.scanKeys[1:]
	}
	return out
}

func (w *Work) flagged(ctx context.Context, sid, path string) (gathered, Flag, error) {
	g, err := w.current(ctx, sid)
	if err != nil {
		return g, Flag{}, err
	}
	for _, f := range g.flags {
		if f.Commit == "" && f.Path == path {
			return g, f.Flag, nil
		}
	}
	return g, Flag{}, conflict("not flagged", "That file isn't flagged on this branch any more.")
}

func (w *Work) flaggedCommit(ctx context.Context, sid, commit string, field CommitField) (gathered, Flag, error) {
	g, err := w.current(ctx, sid)
	if err != nil {
		return g, Flag{}, err
	}
	for _, f := range g.flags {
		if f.Commit != "" && f.Commit == commit && f.Field == field {
			return g, f.Flag, nil
		}
	}
	return g, Flag{}, conflict("not flagged", "That commit's "+FieldWords[field]+" isn't flagged on this branch any more.")
}

func (w *Work) allow(sid string, f Flag) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.sessions[sid]
	if s == nil {
		return conflict("not started here", "Only a session this engine started has flagged items here.")
	}
	if s.allowed == nil {
		s.allowed = map[string]map[string]bool{}
	}
	set := s.allowed[f.key()]
	if set == nil {
		set = map[string]bool{}
		s.allowed[f.key()] = set
	}
	for _, v := range f.Versions {
		set[v] = true
	}
	return nil
}

func (w *Work) AllowFile(ctx context.Context, sid, path, digest, reason string) error {
	clean := gate.Reason(reason)
	if clean == "" {
		return onField("reason", invalid("reason", "Needs your reason, up to 500 characters."))
	}
	g, f, err := w.flagged(ctx, sid, path)
	if err != nil {
		return err
	}
	if f.Link {
		return conflict("link", "This is a symbolic link the hold on Proctor's specifications can't follow (under proctor/ and pointing outside it, or a root Makefile that is a link), so it can't be allowed. Ask the agent to take it out of the branch.")
	}
	if !hmac.Equal([]byte(w.flagDigest(f)), []byte(digest)) {
		return conflict("stale", "This file changed on the branch since the page showed it, so nothing was allowed and it still holds the push. Look at it again, then allow it with your reason.")
	}
	if f.Full || len(f.Versions) == 0 {
		return conflict("versions", "This file has more than 20 flagged versions on the branch, the most the gate lists, so it can't be allowed. Take it out of the commits that hold it.")
	}
	if err := w.allow(sid, f); err != nil {
		return err
	}
	w.persist(sid)
	if w.cfg.Audit != nil {
		e := AuditEntry{Time: w.cfg.Now(), Session: sid, Repository: g.t.Name, Branch: g.t.Branch, Commit: g.head, By: ByPage, Kind: AuditAllowFile,
			Result: "allowed", Reason: "the developer allowed a flagged file, because " + f.Why, Path: f.Path, Blobs: slices.Clone(f.Versions), PersonReason: clean}
		if err := w.cfg.Audit.Append(e); err != nil {
			w.cfg.Logger.Warn("flag", "event", "audit-not-written")
		}
	}
	w.cfg.Logger.Info("flag", "event", "allowed", "kind", string(f.Kind))
	return nil
}

func (w *Work) AskRemove(ctx context.Context, sid, path string) error {
	if w.cfg.Tell == nil {
		return conflict("no session", "This engine can't send the session a message.")
	}
	g, f, err := w.flagged(ctx, sid, path)
	if err != nil {
		return err
	}
	text := "The software factory's push gate flagged the file " + strconv.Quote(f.Label()) + " on this branch: " + f.Why + ". " +
		"Remove it from the branch: take it out of every commit on this branch that holds it, since deleting it in a new commit still pushes the earlier ones, " +
		"and add it to .gitignore so it isn't committed again. Change nothing else, don't push, and tell me when it's done."
	if f.Kind == FlagSpec || g.specs && gate.SpecPath(f.Path) {
		text = "The software factory's push gate flagged the file " + strconv.Quote(f.Label()) + " on this branch: " + f.Why + ". " +
			"Revert this branch's change to it: take the change out of every commit on this branch that makes it, so that no commit the push sends adds, changes, deletes or renames it and it is as the main branch has it, since reverting it in a new commit still pushes the earlier ones. " +
			"Don't add it to .gitignore. Change nothing else, don't push, and tell me when it's done."
	}
	if err := w.cfg.Tell(sid, text); err != nil {
		if e, ok := errors.AsType[*Error](err); ok && e.Message != "" {
			return e
		}
		return conflict("not sent", "The message couldn't reach the session, which must be running and controlled from this page.")
	}
	w.cfg.Logger.Info("flag", "event", "asked", "kind", string(f.Kind))
	return nil
}

func (w *Work) AllowCommit(ctx context.Context, sid, commit string, field CommitField, reason string) error {
	clean := gate.Reason(reason)
	if clean == "" {
		return onField("reason", invalid("reason", "Needs your reason, up to 500 characters."))
	}
	g, f, err := w.flaggedCommit(ctx, sid, commit, field)
	if err != nil {
		return err
	}
	if err := w.allow(sid, f); err != nil {
		return err
	}
	w.persist(sid)
	if w.cfg.Audit != nil {
		e := AuditEntry{Time: w.cfg.Now(), Session: sid, Repository: g.t.Name, Branch: g.t.Branch, Commit: g.head, By: ByPage, Kind: AuditAllowCommit,
			Result: "allowed", Reason: "the developer allowed the " + FieldWords[f.Field] + " of commit " + f.Commit + ", flagged because " + f.Why, Blobs: []string{f.Commit}, PersonReason: clean}
		if err := w.cfg.Audit.Append(e); err != nil {
			w.cfg.Logger.Warn("flag", "event", "audit-not-written")
		}
	}
	w.cfg.Logger.Info("flag", "event", "allowed", "kind", "commit", "field", string(f.Field))
	return nil
}

func (w *Work) AskRewrite(ctx context.Context, sid, commit string, field CommitField) error {
	if w.cfg.Tell == nil {
		return conflict("no session", "This engine can't send the session a message.")
	}
	_, f, err := w.flaggedCommit(ctx, sid, commit, field)
	if err != nil {
		return err
	}
	how := "reword its message"
	switch f.Field {
	case FieldAuthorName, FieldAuthorEmail:
		how = "set its author to the name and email you commit with"
	case FieldCommitterName, FieldCommitterEmail:
		how = "commit it again with the committer name and email you commit with"
	case FieldHeaders:
		how = "commit it again without the extra headers"
	}
	text := "The software factory's push gate flagged the " + FieldWords[f.Field] + " of commit " + f.Commit + " on this branch: " + f.Why + ". " +
		"Rewrite that commit so its " + FieldWords[f.Field] + " no longer holds it: " + how + ", and keep every file as it is. " +
		"Rewrite only commits that aren't on the remote branch yet, since the engine never forces a push; if this one is already there, don't rewrite it and tell me, since only the developer can allow it then. " +
		"Don't repeat the flagged text anywhere, change nothing else, don't push, and tell me when it's done."
	if err := w.cfg.Tell(sid, text); err != nil {
		if e, ok := errors.AsType[*Error](err); ok && e.Message != "" {
			return e
		}
		return conflict("not sent", "The message couldn't reach the session, which must be running and controlled from this page.")
	}
	w.cfg.Logger.Info("flag", "event", "asked-rewrite", "field", string(f.Field))
	return nil
}

func (w *Work) Rounds(sid, repo string) int {
	ws, ok := w.session(sid)
	if !ok {
		ws = workSession{repo: repo}
	}
	return w.setupFor(ws.repo).rounds
}
