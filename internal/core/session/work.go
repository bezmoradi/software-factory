package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/gate"
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
)

var (
	InstructionFiles = []string{"CLAUDE.md", ".claude/CLAUDE.md", "AGENTS.md"}
	sha256Hex        = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type WorkFiles interface {
	ChangedFiles(repo, from, to string) ([]push.Change, error)
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
	Claude       bool
	Type         string
	Chosen       bool
	Requirements *SavedApproval
	Plan         *SavedApproval
	Allowed      map[string][]string
}

type WorkState struct {
	Teams    map[string]TeamApproval
	Projects map[string]ProjectSettings
	Sessions map[string]SavedWork
}

type WorkStore interface {
	Load() (WorkState, error)
	Save(WorkState) error
}

type WorkConfig struct {
	Targets    func(sid string) (PushTarget, bool)
	Heads      Heads
	History    History
	Files      WorkFiles
	Repos      WorkRepos
	Reported   func(sid string) (Workflow, bool)
	Reviews    ReviewEntries
	Store      WorkStore
	Installed  func() []string
	Contents   PushContents
	Audit      Audit
	Tell       func(sid, text string) error
	Now        func() time.Time
	DocsInRepo bool
	Logger     shared.Logger

	RoleAgents RoleAgents
	Changes    ChangeReader
}

type workApproval struct {
	Hash   string
	Commit string
	At     time.Time
	Text   string
}

type workSession struct {
	repo    string
	claude  bool
	typeID  string
	chosen  bool
	req     *workApproval
	plan    *workApproval
	allowed map[string]map[string]bool
}

type Work struct {
	cfg      WorkConfig
	mu       sync.Mutex
	sessions map[string]*workSession
	store    sync.Mutex
	scanMu   sync.Mutex
	scans    map[string]scanned
	scanKeys []string
	sizeMu   sync.Mutex
	sizes    map[string]changeSize
}

type WorkDoc struct {
	Path       string
	Text       string
	Hash       string
	Clipped    bool
	Approved   bool
	Changed    bool
	Previous   string
	Commit     string
	ApprovedAt string
	Reported   bool
	OnBase     bool
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
}

type GateView struct {
	Head          string
	Branch        string
	Adapter       workflow.AdapterID
	Type          string
	Chosen        bool
	Effective     workflow.Profile
	Note          string
	Types         []workflow.WorkType
	Commands      map[workflow.Step]string
	TeamApproved  bool
	TeamNote      string
	Requirements  *WorkDoc
	Plan          *WorkDoc
	Research      string
	TasksNeeded   bool
	TasksWritten  bool
	Define        bool
	Reviewers     []WorkReviewer
	Missing       []string
	Allowed       bool
	Waiting       string
	Holds         map[string]string
	Rounds        int
	FilesListed   bool
	ChangedFiles  int
	PlanApprovals bool
	Flagged       []FlagView
	FlagsRead     bool
	LFS           []string
	Handoff       bool
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
}

type WorkPage interface {
	View(sid string) (GateView, error)
	SetType(sid, id string) error
	Approve(sid string, plan bool, hash, text string) error
	Team(ctx context.Context, path string) (TeamView, error)
	ApproveTeam(ctx context.Context, path, sha string) (TeamView, error)
	ForgetTeam(path string) (TeamView, error)
	Project(name string) ProjectInfo
	SetProject(path, op string, rounds int, kind string) error
	SetAutoFindings(path string, on bool) error
	SetProjectOption(path, op string, on bool) error
	SetHold(path string, h HoldSettings) error
	AllowFile(sid, path, reason string) error
	AskRemove(sid, path string) error
	AllowCommit(sid, commit string, field CommitField, reason string) error
	AskRewrite(sid, commit string, field CommitField) error
	EditHold(sid, top string, paths []string) string
	Guarded(sid, top string, paths []string) string
}

var _ WorkPage = (*Work)(nil)

func NewWork(cfg WorkConfig) (*Work, error) {
	if cfg.Targets == nil || cfg.Heads == nil || cfg.History == nil || cfg.Files == nil || cfg.Repos == nil || cfg.Store == nil {
		return nil, errors.New("work: need the sessions, the hash-checked reader, the repositories and the state file")
	}
	if cfg.Reported == nil {
		cfg.Reported = func(string) (Workflow, bool) { return Workflow{}, false }
	}
	if cfg.Installed == nil {
		cfg.Installed = func() []string { return nil }
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = quiet{}
	}
	cfg.Audit = masking(cfg.Audit)
	return &Work{cfg: cfg, sessions: map[string]*workSession{}, scans: map[string]scanned{}, sizes: map[string]changeSize{}}, nil
}

func textHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func (w *Work) load() WorkState {
	w.store.Lock()
	defer w.store.Unlock()
	st, err := w.cfg.Store.Load()
	if err != nil {
		w.cfg.Logger.Warn("work", "event", "state-unreadable")
	}
	if st.Teams == nil {
		st.Teams = map[string]TeamApproval{}
	}
	if st.Projects == nil {
		st.Projects = map[string]ProjectSettings{}
	}
	if st.Sessions == nil {
		st.Sessions = map[string]SavedWork{}
	}
	return st
}

func (w *Work) change(fn func(*WorkState) error) error {
	w.store.Lock()
	defer w.store.Unlock()
	st, err := w.cfg.Store.Load()
	if err != nil {
		return explained(conflict("state"), "The engine couldn't read its project settings.")
	}
	if st.Teams == nil {
		st.Teams = map[string]TeamApproval{}
	}
	if st.Projects == nil {
		st.Projects = map[string]ProjectSettings{}
	}
	if st.Sessions == nil {
		st.Sessions = map[string]SavedWork{}
	}
	if err := fn(&st); err != nil {
		return err
	}
	if err := w.cfg.Store.Save(st); err != nil {
		return explained(conflict("state"), "The engine couldn't save its project settings.")
	}
	return nil
}

func (w *Work) repoByPath(path string) (Repository, error) {
	list, err := w.cfg.Repos.List()
	if err != nil {
		return Repository{}, explained(conflict("records"), "The engine couldn't read its list of repositories.")
	}
	i := slices.IndexFunc(list, func(r Repository) bool { return r.Path == path })
	if i < 0 {
		return Repository{}, explained(notFound("project"), "That project is no longer in the list.")
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
	problems = append(problems, workflow.AgentProblems(team, w.cfg.Installed())...)
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
	setup    workflow.Setup
	problems []string
	approved bool
	note     string
	rounds   int
	project  string
}

func (w *Work) setupFor(name string, claude bool) setupOf {
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
	adapter := workflow.SpecKit
	if claude {
		adapter = workflow.Knackpack
	}
	if team != nil && !claude {
		t := *team
		t.Adapter = workflow.SpecKit
		team = &t
	}
	out.setup, out.problems = workflow.SetupOf(team, personal, adapter)
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
	if _, ok := typeIn(s.Types, s.DefaultType); ok && s.DefaultType != "" {
		return s.DefaultType
	}
	return DefaultWorkType
}

func (w *Work) Check(repo string, claude bool, typeID string) error {
	if typeID == "" {
		return nil
	}
	if _, ok := typeIn(w.setupFor(repo, claude).setup.Types, typeID); !ok {
		return explained(invalid("type"), "That work type isn't one this project offers.")
	}
	return nil
}

func (w *Work) Begin(sid, repo string, claude bool, typeID string) {
	s := w.setupFor(repo, claude)
	chosen := typeID != ""
	if _, ok := typeIn(s.setup.Types, typeID); !ok {
		typeID, chosen = w.defaultType(s.setup), s.project != ""
	}
	w.mu.Lock()
	w.sessions[sid] = &workSession{repo: repo, claude: claude, typeID: typeID, chosen: chosen}
	w.mu.Unlock()
	w.persist(sid)
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
}

func (w *Work) session(sid string) (workSession, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.sessions[sid]
	if s == nil {
		return workSession{}, false
	}
	c := *s
	c.allowed = map[string]map[string]bool{}
	for p, set := range s.allowed {
		c.allowed[p] = maps.Clone(set)
	}
	return c, true
}

func (w *Work) SetType(sid, id string) error {
	ws, ok := w.session(sid)
	if !ok {
		return explained(conflict("not started here"), "Only a session this engine started has a work type here.")
	}
	if _, ok := typeIn(w.setupFor(ws.repo, ws.claude).setup.Types, id); !ok {
		return explained(invalid("type"), "That work type isn't one this project offers.")
	}
	w.mu.Lock()
	if s := w.sessions[sid]; s != nil {
		s.typeID, s.chosen = id, true
	}
	w.mu.Unlock()
	w.persist(sid)
	w.cfg.Logger.Info("work type", "type", id)
	return nil
}

type workDocs struct {
	req       *WorkDoc
	plan      *WorkDoc
	research  string
	tasks     bool
	dir       string
	held      bool
	reported  bool
	shownReq  *WorkDoc
	shownPlan *WorkDoc
	baseReq   *WorkDoc
	basePlan  *WorkDoc
}

func (d *workDocs) onBranch(files []string, listed bool) {
	changed := func(doc *WorkDoc) bool { return listed && slices.Contains(files, doc.Path) }
	if d.req != nil && !changed(d.req) {
		d.baseReq, d.req = d.req, nil
	}
	if d.plan != nil && !changed(d.plan) {
		d.basePlan, d.plan = d.plan, nil
	}
}

func docFrom(d *Doc) *WorkDoc {
	if d == nil {
		return nil
	}
	return &WorkDoc{Path: d.Path, Text: d.Text, Hash: d.Hash, Clipped: d.Clipped}
}

func (w *Work) readDoc(repo, head, path string) (string, bool) {
	f, err := w.cfg.Files.ReadFile(repo, head, path)
	if err != nil || f.Mode != push.ModeFile || !utf8.Valid(f.Data) {
		return "", false
	}
	return string(f.Data), true
}

func shownDoc(path, text string) *WorkDoc {
	d := &WorkDoc{Path: path, Text: text}
	if utf16Len(text) > MaxWorkflowText {
		d.Text, d.Clipped = clip(text, MaxWorkflowText), true
	}
	d.Hash = textHash(d.Text)
	return d
}

func (w *Work) committedKnackpack(sid, repo, head string) workDocs {
	out := workDocs{held: true}
	if rep, ok := w.cfg.Reported(sid); ok {
		out.shownReq, out.shownPlan = docFrom(rep.Requirements), docFrom(rep.Plan)
		if rep.Research != nil {
			out.research = rep.Research.Path
		}
	}
	if head == "" {
		return out
	}
	names := workflow.Adapters[workflow.Knackpack].Documents
	feature, _ := w.readDoc(repo, head, names.Feature)
	out.dir = workflow.KnackpackFeatureDir([]byte(feature))
	if out.dir == "" {
		return out
	}
	if t, ok := w.readDoc(repo, head, out.dir+"/"+names.Requirements); ok {
		out.req = shownDoc(out.dir+"/"+names.Requirements, t)
		out.req.Commit = head
	}
	if t, ok := w.readDoc(repo, head, out.dir+"/"+names.Plan); ok {
		out.plan = shownDoc(out.dir+"/"+names.Plan, t)
		out.plan.Commit = head
	}
	return out
}

func (w *Work) docs(sid string, adapter workflow.AdapterID, repo, head string, files []string, tasks bool) workDocs {
	if adapter == workflow.Knackpack && w.cfg.DocsInRepo {
		return w.committedKnackpack(sid, repo, head)
	}
	if adapter == workflow.Knackpack {
		rep, ok := w.cfg.Reported(sid)
		if !ok {
			return workDocs{}
		}
		out := workDocs{req: docFrom(rep.Requirements), plan: docFrom(rep.Plan), reported: true}
		if rep.Research != nil {
			out.research = rep.Research.Path
		}
		return out
	}
	out := workDocs{held: true}
	if head == "" {
		return out
	}
	names := workflow.Adapters[adapter].Documents
	feature, _ := w.readDoc(repo, head, names.Feature)
	out.dir = workflow.SpecFeatureDir([]byte(feature), files)
	if out.dir == "" {
		return out
	}
	if t, ok := w.readDoc(repo, head, out.dir+"/"+names.Requirements); ok {
		out.req = shownDoc(out.dir+"/"+names.Requirements, t)
		out.req.Commit = head
	}
	if _, ok := w.readDoc(repo, head, out.dir+"/"+names.Research); ok {
		out.research = out.dir + "/" + names.Research
	}
	plan, hasPlan := w.readDoc(repo, head, out.dir+"/"+names.Plan)
	taskText, hasTasks := w.readDoc(repo, head, out.dir+"/"+names.Tasks)
	out.tasks = hasTasks
	if hasPlan {
		text := plan
		if tasks && hasTasks {
			text = plan + "\n\n---\n\n# " + out.dir + "/" + names.Tasks + "\n\n" + taskText
		}
		out.plan = shownDoc(out.dir+"/"+names.Plan, text)
		out.plan.Commit = head
	}
	return out
}

type gathered struct {
	ws        workSession
	t         PushTarget
	head      string
	main      string
	base      string
	files     []string
	listed    bool
	note      string
	setup     setupOf
	adapter   workflow.AdapterID
	chosen    workflow.WorkType
	eff       workflow.WorkType
	effNote   string
	docs      workDocs
	reqOk     bool
	reqWas    bool
	planOk    bool
	planWas   bool
	planBase  string
	built     bool
	required  []workflow.Requirement
	items     []string
	toApprove bool
	headError string
	scan      scanned
	flags     []FlagView
	flagsRead bool
}

func (w *Work) gather(sid string, ws workSession, t PushTarget, head, main string) gathered {
	g := gathered{ws: ws, t: t, head: head, main: main}
	g.setup = w.setupFor(ws.repo, ws.claude)
	g.adapter = g.setup.setup.Adapter
	kind, ok := typeIn(g.setup.setup.Types, ws.typeID)
	if !ok {
		kind, _ = typeIn(g.setup.setup.Types, DefaultWorkType)
		g.effNote = "This session's work type isn't offered any more, so a Fix's checks apply."
	}
	g.chosen = kind
	if head != "" {
		if main != "" {
			base, err := w.cfg.History.MergeBase(t.Repo, head, main)
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
			changes, err := w.cfg.Files.ChangedFiles(t.Repo, g.base, head)
			if err != nil {
				g.listed = false
				g.note = "the engine's reader couldn't compare the branch with the main branch"
			}
			for _, c := range changes {
				g.files = append(g.files, c.Path)
			}
		}
	}
	eff, note := workflow.Effective(kind, g.files, g.listed)
	g.eff = eff
	if note != "" {
		g.effNote = strings.TrimSpace(g.effNote + " " + note)
	}
	tasks := slices.Contains(kind.Steps, workflow.Tasks)
	g.docs = w.docs(sid, g.adapter, t.Repo, head, g.files, tasks)
	if g.docs.held {
		g.docs.onBranch(g.files, g.listed)
	}
	feature := g.chosen.Profile() == workflow.Feature
	counts := func(a *workApproval, d *WorkDoc) bool {
		if a == nil {
			return false
		}
		if d == nil {
			return !g.docs.held && !g.docs.reported
		}
		return d.Hash == a.Hash
	}
	g.reqOk = counts(ws.req, g.docs.req)
	g.reqWas = ws.req != nil && !g.reqOk
	g.planOk = counts(ws.plan, g.docs.plan) && (!feature || g.reqOk)
	g.planWas = ws.plan != nil && !g.planOk
	if g.eff.Profile() == workflow.Feature && g.planOk {
		g.planBase = ws.plan.Commit
		if head != "" && head != g.planBase {
			in, err := w.cfg.History.IsAncestor(t.Repo, g.planBase, head)
			g.built = err == nil && in
		}
	}
	g.required = w.deepened(t, workflow.Required(g.setup.setup, g.eff, g.files), g.base, head, g.listed)
	planFile := ""
	if g.docs.plan != nil {
		planFile = g.docs.plan.Path
	}
	flagsRead := true
	g.flagsRead = true
	var flagged []workflow.Flag
	if head != "" {
		if g.listed {
			g.scan = w.scanOf(t.Repo, g.base, head)
		}
		flagsRead = g.listed && g.scan.complete
		g.flagsRead = flagsRead
		for _, f := range g.scan.flags {
			v := FlagView{Flag: f, Shown: f.maskedPath(), Allowed: allowedFlag(f, ws.allowed)}
			g.flags = append(g.flags, v)
			flagged = append(flagged, workflow.Flag{Path: f.Label(), Why: f.Why, Allowed: v.Allowed})
		}
	}
	g.items, g.toApprove = workflow.Items(workflow.Progress{
		Chosen: g.chosen.Profile(), Effective: g.eff, Adapter: g.adapter,
		FilesListed: g.listed || head == "", FilesNote: g.note, FlagsRead: flagsRead, Flagged: flagged,
		ReqOk: g.reqOk, ReqWasApproved: g.reqWas, PlanFile: planFile, TasksWritten: g.docs.tasks,
		PlanOk: g.planOk, PlanWasApproved: g.planWas, PlanBase: g.planBase, Built: g.built,
	})
	if installed := w.cfg.Installed(); len(installed) > 0 {
		for _, q := range g.required {
			if q.Role == "" && !slices.Contains(installed, q.Agent) {
				g.items = append(g.items, "a review by "+q.Agent+", which no plugin installed in the reviewer image provides")
			}
		}
	}
	for _, p := range g.setup.problems {
		g.items = append(g.items, "a setup the gate can read: "+p)
	}
	return g
}

func (w *Work) PushFacts(sid string, t PushTarget, head, main string) (WorkFacts, bool) {
	ws, ok := w.session(sid)
	if !ok {
		return WorkFacts{}, false
	}
	g := w.gather(sid, ws, t, head, main)
	return WorkFacts{Type: g.eff, Required: g.required, Items: g.items, NeedsApproval: g.toApprove, Rounds: g.setup.rounds, Refusal: lfsRefusal(g.scan.lfs), Hold: w.holdOf(t.Name)}, true
}

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

func (w *Work) Reviewers(sid string, t PushTarget, base, head string) []workflow.Requirement {
	ws, ok := w.session(sid)
	if !ok {
		ws = workSession{repo: t.Name, claude: true, typeID: DefaultWorkType}
	}
	setup := w.setupFor(ws.repo, ws.claude)
	kind, ok := typeIn(setup.setup.Types, ws.typeID)
	if !ok {
		kind, _ = typeIn(setup.setup.Types, DefaultWorkType)
	}
	changes, err := w.cfg.Files.ChangedFiles(t.Repo, base, head)
	var files []string
	for _, c := range changes {
		files = append(files, c.Path)
	}
	eff, _ := workflow.Effective(kind, files, err == nil)
	required := w.deepened(t, workflow.Required(setup.setup, eff, files), base, head, err == nil)
	return append(required, workflow.Advisory(required, w.depthOf(t.Name))...)
}

func ReviewerName(q workflow.Requirement) string {
	if q.Role != "" {
		return q.Role.Name()
	}
	return strings.TrimPrefix(q.Agent, "knackpack:")
}

func (w *Work) current(sid string) (gathered, error) {
	ws, ok := w.session(sid)
	if !ok {
		return gathered{}, explained(conflict("not started here"), "Only a session this engine started has a workflow here.")
	}
	t, ok := w.cfg.Targets(sid)
	if !ok {
		return gathered{}, explained(conflict("not started here"), "Only a session this engine started has a workflow here.")
	}
	head, err := w.cfg.Heads.Head(t.Repo, t.Branch)
	headError := ""
	if err != nil {
		head, headError = "", "the engine couldn't read the session's branch "+t.Branch
	}
	main, _ := w.cfg.Repos.Main(t.Name)
	g := w.gather(sid, ws, t, head, main)
	g.headError = headError
	return g, nil
}

func (w *Work) facts(g gathered) workflow.Facts {
	f := workflow.Facts{Profile: g.chosen.Profile(), Head: g.head, ReqOk: g.reqOk, PlanOk: g.planOk,
		UsesArtifacts: g.docs.held, PlanCommand: workflow.Adapters[g.adapter].Commands[workflow.Plan]}
	if g.docs.req != nil {
		f.ReqHash = g.docs.req.Hash
	}
	if g.docs.plan != nil {
		f.PlanFile, f.PlanHash = g.docs.plan.Path, g.docs.plan.Hash
	}
	if g.reqOk && g.ws.req != nil {
		f.ReqBaseline = g.ws.req.Commit
	}
	if f.UsesArtifacts {
		f.Artifacts = workflow.StepArtifacts(g.adapter, []workflow.Step{workflow.Plan, workflow.Tasks}, g.docs.dir)
		if !g.reqOk {
			dir := g.docs.dir
			if g.adapter == workflow.Knackpack {
				dir = ""
			}
			f.Artifacts = workflow.StepArtifacts(g.adapter, []workflow.Step{workflow.Define}, dir)
		}
		if f.ReqBaseline != "" && g.head != "" {
			if f.ReqBaseline == g.head {
				f.ArtifactsOnly = true
			} else if changes, err := w.cfg.Files.ChangedFiles(g.t.Repo, f.ReqBaseline, g.head); err == nil {
				paths := make([]string, 0, len(changes))
				for _, c := range changes {
					paths = append(paths, c.Path)
				}
				f.ArtifactsOnly = workflow.OnlyArtifacts(paths, f.Artifacts)
			}
		}
	}
	return f
}

func (w *Work) Approve(sid string, plan bool, hash, text string) error {
	if !sha256Hex.MatchString(hash) || textHash(text) != hash {
		return explained(invalid("hash"), "The text the page showed doesn't match its hash. Reload the page and read it again.")
	}
	g, err := w.current(sid)
	if err != nil {
		return err
	}
	doc, what := g.docs.req, "requirements"
	if plan {
		doc, what = g.docs.plan, "plan"
	}
	onBase := g.docs.baseReq
	if plan {
		onBase = g.docs.basePlan
	}
	switch {
	case doc == nil && onBase != nil:
		return explained(conflict("on base"), "The "+what+" text is from an earlier task, already on the base branch, so it can't be approved here. The session's own commits must add or change it.")
	case doc == nil && g.docs.held && g.adapter == workflow.Knackpack:
		return explained(conflict("not committed"), "The engine counts only the "+what+" text committed on the session's branch, and there's none there yet. Ask the agent to commit it, then approve.")
	case doc == nil:
		return explained(conflict("nothing"), "There's no "+what+" to approve yet.")
	case doc.Clipped:
		return explained(conflict("too long"), "The "+what+" is longer than the page shows, so it can't be approved as a whole. Ask for a shorter one.")
	case doc.Hash != hash:
		return explained(conflict("changed"), "The "+what+" changed before your approval arrived. Read the new version and approve it again.")
	case g.head == "":
		return explained(conflict("head"), "The "+what+" weren't approved: the engine couldn't read the session's branch.")
	}
	f := w.facts(g)
	kind := workflow.ApproveRequirements
	if plan {
		kind = workflow.ApprovePlan
	}
	if why := workflow.Decide(f, workflow.Action{Kind: kind, Hash: hash}); why != "" {
		return explained(conflict("held"), why)
	}
	a := &workApproval{Hash: hash, Commit: g.head, At: w.cfg.Now()}
	if !plan {
		a.Text = text
	}
	w.mu.Lock()
	if s := w.sessions[sid]; s != nil {
		if plan {
			s.plan = a
		} else {
			s.req = a
			s.plan = nil
		}
	}
	w.mu.Unlock()
	w.persist(sid)
	w.approved(sid, g.t, g.head, plan, hash)
	w.cfg.Logger.Info("approved", "what", what)
	if next := nextStep(g, plan); next != "" && w.cfg.Tell != nil {
		if err := w.cfg.Tell(sid, next); err != nil {
			w.cfg.Logger.Warn("approved", "what", what, "event", "next-step-not-sent")
		}
	}
	return nil
}

func (w *Work) EditHold(sid, top string, paths []string) string {
	if top == "" {
		return ""
	}
	g, err := w.current(sid)
	if err != nil {
		return ""
	}
	f := w.facts(g)
	for _, p := range paths {
		rel, err := filepath.Rel(top, p)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
			continue
		}
		if why := workflow.Decide(f, workflow.Action{Kind: workflow.EditFile, Path: filepath.ToSlash(rel)}); why != "" {
			return why
		}
	}
	return ""
}

const implementText = "The developer approved the plan on the software factory page. Implement it now, then commit and call factory_push. Each commit message says what changed and why."

func nextStep(g gathered, plan bool) string {
	steps := g.eff.Steps
	has := func(s workflow.Step) bool { return slices.Contains(steps, s) }
	cmds := workflow.Adapters[g.adapter].Commands
	var want []workflow.Step
	if plan {
		want = []workflow.Step{workflow.Implement}
	} else {
		want = []workflow.Step{workflow.Plan}
	}
	for _, s := range want {
		if !has(s) {
			continue
		}
		if c := cmds[s]; c != "" {
			return c
		}
		if s == workflow.Implement {
			return implementText
		}
	}
	return ""
}

func (w *Work) reviewerStates(g gathered) ([]WorkReviewer, []string) {
	var entries map[review.Key]ReviewEntry
	if w.cfg.Reviews != nil {
		entries, _ = w.cfg.Reviews.Entries()
	}
	records := map[review.Key]review.Record{}
	for k, e := range entries {
		records[k] = e.Record
	}
	limit := roundLimit(g.setup.rounds)
	var out []WorkReviewer
	var missing []string
	for _, q := range g.required {
		r := WorkReviewer{Agent: q.Agent, Role: q.Role, Name: ReviewerName(q), Why: q.Why, State: "none", Limit: limit}
		rd := RoundOf(entries, g.t.Branch, q.Agent)
		r.Round, r.Capped = rd.Count, rd.Capped(limit)
		k := review.Key{Commit: g.head, Branch: g.t.Branch, Agent: q.Agent}
		if e, complete, ok := pooledEntry(entries, k, q.RunCount()); ok {
			r.State, r.Verdict, r.Override, r.Since = string(e.Record.State), e.Record.Verdict, e.Record.Override, e.Since
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
		if g.head != "" {
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

func (w *Work) View(sid string) (GateView, error) {
	g, err := w.current(sid)
	if err != nil {
		return GateView{}, err
	}
	v := GateView{Head: g.head, Branch: g.t.Branch, Adapter: g.adapter, Type: g.chosen.ID, Chosen: g.ws.chosen, Effective: g.eff.Profile(), Note: g.effNote,
		Types: g.setup.setup.Types, Commands: workflow.Adapters[g.adapter].Commands, TeamApproved: g.setup.approved, TeamNote: g.setup.note,
		Research: g.docs.research, TasksNeeded: slices.Contains(g.chosen.Steps, workflow.Tasks), TasksWritten: g.docs.tasks,
		Rounds: g.setup.rounds, FilesListed: g.listed, ChangedFiles: len(g.files), PlanApprovals: g.docs.held}
	if d := g.docs.req; d != nil {
		c := *d
		c.Approved, c.Changed = g.reqOk, g.reqWas
		if g.reqWas && g.ws.req != nil {
			c.Previous = g.ws.req.Text
		}
		if g.docs.held && g.ws.req != nil {
			c.ApprovedAt = g.ws.req.Commit
		}
		v.Requirements = &c
	} else if d := g.docs.baseReq; d != nil {
		c := *d
		c.OnBase, c.Changed = true, g.reqWas
		v.Requirements = &c
	} else if d := g.docs.shownReq; d != nil {
		c := *d
		c.Reported, c.Changed = true, g.reqWas
		v.Requirements = &c
	}
	if d := g.docs.plan; d != nil {
		c := *d
		c.Approved, c.Changed = g.planOk, g.planWas
		if g.docs.held && g.ws.plan != nil {
			c.ApprovedAt = g.ws.plan.Commit
		}
		v.Plan = &c
	} else if d := g.docs.basePlan; d != nil {
		c := *d
		c.OnBase, c.Changed = true, g.planWas
		v.Plan = &c
	} else if d := g.docs.shownPlan; d != nil {
		c := *d
		c.Reported, c.Changed = true, g.planWas
		v.Plan = &c
	}
	feature := g.chosen.Profile() == workflow.Feature
	v.Define = (feature && g.reqOk) || (!feature && g.docs.req != nil)
	reviewers, missing := w.reviewerStates(g)
	v.Reviewers = reviewers
	v.Flagged, v.FlagsRead = g.flags, g.flagsRead
	for _, p := range g.scan.lfs {
		v.LFS = append(v.LFS, maskLines(p, true))
	}
	for _, r := range reviewers {
		v.Handoff = v.Handoff || r.Capped
	}
	v.Missing = append([]string{}, g.items...)
	if why := lfsRefusal(g.scan.lfs); why != "" {
		v.Missing = append(v.Missing, "a push the engine can make: "+why)
	}
	if g.headError != "" {
		v.Missing = append([]string{"the current commit, which " + g.headError}, v.Missing...)
	}
	if !g.eff.Push {
		v.Missing = append(v.Missing, "a work type that pushes, picked on the page")
	}
	v.Missing = append(v.Missing, missing...)
	v.Allowed = len(v.Missing) == 0
	f := w.facts(g)
	v.Waiting = workflow.ImplementWait(workflow.Build{Profile: g.eff.Profile(), Head: g.head != "", PlanOk: g.planOk,
		Built: g.built, Commits: len(g.files), CommitsRead: g.listed, NoBase: g.main == ""})
	v.Holds = map[string]string{
		"define": workflow.Decide(f, workflow.Action{Kind: workflow.StartDefine}),
		"plan":   workflow.Decide(f, workflow.Action{Kind: workflow.StartPlan}),
		"tasks":  workflow.Decide(f, workflow.Action{Kind: workflow.StartTasks}),
	}
	if feature && v.Requirements != nil && !g.reqOk {
		v.Holds["approveRequirements"] = workflow.Decide(f, workflow.Action{Kind: workflow.ApproveRequirements, Hash: v.Requirements.Hash})
		if v.Requirements.Reported {
			v.Holds["approveRequirements"] = notCommitted("requirements")
		}
		if v.Requirements.OnBase {
			v.Holds["approveRequirements"] = fromBase("requirements")
		}
	}
	if v.Plan != nil && !g.planOk {
		v.Holds["approvePlan"] = workflow.Decide(f, workflow.Action{Kind: workflow.ApprovePlan, Hash: v.Plan.Hash})
		if v.Plan.Reported {
			v.Holds["approvePlan"] = notCommitted("plan")
		}
		if v.Plan.OnBase {
			v.Holds["approvePlan"] = fromBase("plan")
		}
	}
	return v, nil
}

func fromBase(what string) string {
	return "This " + what + " text is from an earlier task, already on the base branch, so it isn't approvable here. The engine counts only text the session's own commits added or changed."
}

func notCommitted(what string) string {
	return "This is the " + what + " text as the agent reported it, and it isn't committed on the session's branch. The engine counts an approval only of committed text, so ask the agent to commit it first."
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
		return Repository{}, explained(conflict("fetch"), "The engine couldn't fetch the main branch, so it can't read the team file there.")
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
		return TeamView{}, explained(conflict("no team file"), "The main branch has no "+TeamFile+" to approve.")
	case t.sha != sha:
		return TeamView{}, explained(conflict("changed"), "The team file changed on the main branch before your approval arrived. Read it again.")
	case t.team == nil:
		return TeamView{}, explained(conflict("problems"), "The team file has problems, so it can't be approved. Fix them on the main branch first.")
	}
	if err := w.change(func(st *WorkState) error {
		st.Teams[r.Name] = TeamApproval{SHA: sha, At: w.cfg.Now()}
		return nil
	}); err != nil {
		return TeamView{}, err
	}
	w.teamAudit(r.Name, AuditApproveTeam, sha)
	w.cfg.Logger.Info("team file approved")
	return w.teamView(r), nil
}

func (w *Work) ForgetTeam(path string) (TeamView, error) {
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

func (w *Work) SetProject(path, op string, rounds int, kind string) error {
	r, err := w.repoByPath(path)
	if err != nil {
		return err
	}
	switch op {
	case "rounds":
		if rounds < 1 || rounds > maxRounds {
			return explained(invalid("rounds"), "Review rounds go from 1 to 20.")
		}
	case "type":
		if kind != "" {
			if _, ok := typeIn(w.setupFor(r.Name, true).setup.Types, kind); !ok {
				return explained(invalid("type"), "That work type isn't one this project offers.")
			}
		}
	default:
		return explained(invalid("op"), "The engine keeps a project's review rounds and default work type only.")
	}
	return w.change(func(st *WorkState) error {
		p := st.Projects[r.Name]
		if op == "rounds" {
			p.Rounds = rounds
		} else {
			p.Type = kind
		}
		st.Projects[r.Name] = p
		return nil
	})
}

func (w *Work) scanOf(repo, base, head string) scanned {
	if w.cfg.Contents == nil {
		return scanned{}
	}
	key := repo + "\x00" + base + "\x00" + head
	w.scanMu.Lock()
	defer w.scanMu.Unlock()
	if c, ok := w.scans[key]; ok {
		return c
	}
	set, err := w.cfg.Contents.Pushed(repo, base, head, FlagBudget)
	if err != nil {
		w.cfg.Logger.Warn("flag scan", "event", "unreadable")
		return scanned{}
	}
	out := scanPushed(set)
	w.scans[key] = out
	w.scanKeys = append(w.scanKeys, key)
	if len(w.scanKeys) > keptScans {
		delete(w.scans, w.scanKeys[0])
		w.scanKeys = w.scanKeys[1:]
	}
	return out
}

func (w *Work) flagged(sid, path string) (gathered, Flag, error) {
	g, err := w.current(sid)
	if err != nil {
		return g, Flag{}, err
	}
	for _, f := range g.flags {
		if f.Commit == "" && f.Path == path {
			return g, f.Flag, nil
		}
	}
	return g, Flag{}, explained(conflict("not flagged"), "That file isn't flagged on this branch any more.")
}

func (w *Work) flaggedCommit(sid, commit string, field CommitField) (gathered, Flag, error) {
	g, err := w.current(sid)
	if err != nil {
		return g, Flag{}, err
	}
	for _, f := range g.flags {
		if f.Commit != "" && f.Commit == commit && f.Field == field {
			return g, f.Flag, nil
		}
	}
	return g, Flag{}, explained(conflict("not flagged"), "That commit's "+FieldWords[field]+" isn't flagged on this branch any more.")
}

func (w *Work) allow(sid string, f Flag) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.sessions[sid]
	if s == nil {
		return explained(conflict("not started here"), "Only a session this engine started has flagged items here.")
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

func (w *Work) AllowFile(sid, path, reason string) error {
	clean := gate.Reason(reason)
	if clean == "" {
		return explained(invalid("reason"), "Allowing a flagged file needs your reason, at most 500 characters with no control characters. The audit file keeps it.")
	}
	g, f, err := w.flagged(sid, path)
	if err != nil {
		return err
	}
	if f.Full || len(f.Versions) == 0 {
		return explained(conflict("versions"), "This file has more than 20 flagged versions on the branch, the most the gate lists, so it can't be allowed. Take it out of the commits that hold it.")
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

func (w *Work) AskRemove(sid, path string) error {
	if w.cfg.Tell == nil {
		return explained(conflict("no session"), "This engine can't send the session a message.")
	}
	_, f, err := w.flagged(sid, path)
	if err != nil {
		return err
	}
	text := "The software factory's push gate flagged the file " + strconv.Quote(f.Label()) + " on this branch: " + f.Why + ". " +
		"Remove it from the branch: take it out of every commit on this branch that holds it, since deleting it in a new commit still pushes the earlier ones, " +
		"and add it to .gitignore so it isn't committed again. Change nothing else, don't push, and tell me when it's done."
	if err := w.cfg.Tell(sid, text); err != nil {
		if e, ok := errors.AsType[*Error](err); ok && e.Message != "" {
			return e
		}
		return explained(conflict("not sent"), "The message couldn't reach the session, which must be running and controlled from this page.")
	}
	w.cfg.Logger.Info("flag", "event", "asked", "kind", string(f.Kind))
	return nil
}

func (w *Work) AllowCommit(sid, commit string, field CommitField, reason string) error {
	clean := gate.Reason(reason)
	if clean == "" {
		return explained(invalid("reason"), "Allowing a flagged commit needs your reason, at most 500 characters with no control characters. The audit file keeps it.")
	}
	g, f, err := w.flaggedCommit(sid, commit, field)
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

func (w *Work) AskRewrite(sid, commit string, field CommitField) error {
	if w.cfg.Tell == nil {
		return explained(conflict("no session"), "This engine can't send the session a message.")
	}
	_, f, err := w.flaggedCommit(sid, commit, field)
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
		return explained(conflict("not sent"), "The message couldn't reach the session, which must be running and controlled from this page.")
	}
	w.cfg.Logger.Info("flag", "event", "asked-rewrite", "field", string(f.Field))
	return nil
}

func (w *Work) Rounds(sid, repo string) int {
	ws, ok := w.session(sid)
	if !ok {
		ws = workSession{repo: repo, claude: true}
	}
	return w.setupFor(ws.repo, ws.claude).rounds
}

func (w *Work) HandoffText(sid string) string {
	ws, ok := w.session(sid)
	if ok && ws.claude {
		return HandoffCommand
	}
	return "The software factory's push gate holds this branch's push: a reviewer reached this project's round limit and still blocks, and the developer chose Hand off. " +
		"Don't push, and don't fix the findings or ask for another review. Write the developer a handoff summary: what the change does, each blocking finding that is still open, what you tried, and what you'd do next."
}
