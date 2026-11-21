package session

import (
	"cmp"
	"io"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	ImportKept    = "kept"
	ImportDisplay = "display"
	ImportOff     = "off"
	ImportDropped = "dropped"
	ImportLeftOut = "left out"

	ImportStaged  = "staged"
	ImportApplied = "applied"
	ImportRefused = "refused"

	AuditExport = "export"
	AuditImport = "import"

	MaxImport     = 256 << 20
	ExportFor     = 5 * time.Minute
	ImportFor     = 10 * time.Minute
	restartAfter  = 2 * time.Second
	maxReportLine = 300

	ImportTooBig       = "That file is larger than 256 MiB, more than an import takes."
	historyMessage     = "This session came from an export of another engine, so it is history only: its worktree and agent home stayed on that engine. Start a new session instead."
	ImportedReviewNote = "These reviews came from an export of another engine. They are shown for the record and count for nothing here: a push of this commit needs its reviews again."
)

type ExportResult struct {
	Stores  int
	LeftOut int
	Bytes   int64
}

type ImportedStore struct {
	File    string
	Class   string
	Version int
	Label   string
}

type ImportReport struct {
	State      string
	Why        string
	At         time.Time
	Engine     string
	ExportedAt time.Time
	Stores     []ImportedStore
	Off        []string
	KeptOwn    []string
	LeftOut    []string
	Projects   int
	Sessions   int

	FormerPerson string
}

type DroppedRecords struct {
	Store string
	Count int
	Since time.Time
	Last  time.Time
}

type BackupCopy struct {
	Version string
	Files   []string
	At      time.Time
}

type StateArchive interface {
	Export(w io.Writer) (ExportResult, error)
	Stage(r io.Reader) (ImportReport, error)
	Discard() error
	Last() (ImportReport, bool, error)
	Drops() ([]DroppedRecords, error)
	Copies() ([]BackupCopy, error)
}

type BackupView struct {
	Drops     []DroppedRecords
	Copies    []BackupCopy
	Last      *ImportReport
	NeedClone []string
	CanImport bool
	Why       string
}

type BackupPage interface {
	Export(w io.Writer) (ExportResult, error)
	Import(r io.Reader) (ImportReport, error)
	View() (BackupView, error)
	CloneAgain(name string) (Cloning, error)
}

type BackupsConfig struct {
	Archive  StateArchive
	Repos    *Repos
	Sessions func() int
	Audit    Audit
	Restart  func()
	Now      func() time.Time
	Logger   shared.Logger
}

type Backups struct {
	cfg       BackupsConfig
	mu        sync.Mutex
	importing bool
	staged    bool
}

var _ BackupPage = (*Backups)(nil)

func NewBackups(cfg BackupsConfig) *Backups {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = quiet{}
	}
	cfg.Audit = masking(cfg.Audit)
	return &Backups{cfg: cfg}
}

func (b *Backups) audit(result, reason string) {
	b.auditFormer(result, reason, "")
}

func (b *Backups) auditFormer(result, reason, former string) {
	if b.cfg.Audit == nil {
		return
	}
	e := AuditEntry{Time: b.cfg.Now(), By: ByPage, Kind: AuditImport, Result: result, Reason: clip(reason, maxReportLine), FormerPerson: former}
	if err := b.cfg.Audit.Append(e); err != nil {
		b.cfg.Logger.Warn("backup", "event", "audit-not-written")
	}
}

func (b *Backups) empty() string {
	if b.cfg.Repos != nil {
		list, err := b.cfg.Repos.List()
		if err != nil {
			return "The engine couldn't read its list of projects."
		}
		if len(list) > 0 {
			return "This engine has projects already. An import goes only into an engine with no projects and no sessions."
		}
	}
	if b.cfg.Sessions != nil && b.cfg.Sessions() > 0 {
		return "This engine has sessions already. An import goes only into an engine with no projects and no sessions."
	}
	return ""
}

func (b *Backups) Export(w io.Writer) (ExportResult, error) {
	if b.cfg.Archive == nil {
		return ExportResult{}, explained(conflict("no state"), "This engine has no state folder to export.")
	}
	b.mu.Lock()
	staged := b.staged
	b.mu.Unlock()
	if staged {
		return ExportResult{}, explained(conflict("restarting"), "The engine restarts to finish an import. Export after it's back.")
	}
	res, err := b.cfg.Archive.Export(w)
	result := "exported"
	if err != nil {
		result = "failed"
	}
	if b.cfg.Audit != nil {
		e := AuditEntry{Time: b.cfg.Now(), By: ByPage, Kind: AuditExport, Result: result,
			Reason: strconv.Itoa(res.Stores) + " stores, " + strconv.FormatInt(res.Bytes, 10) + " bytes, " + strconv.Itoa(res.LeftOut) + " left out"}
		if aerr := b.cfg.Audit.Append(e); aerr != nil {
			b.cfg.Logger.Warn("backup", "event", "audit-not-written")
		}
	}
	b.cfg.Logger.Info("backup", "event", "export", "ok", err == nil, "stores", res.Stores)
	return res, err
}

func (b *Backups) Import(r io.Reader) (ImportReport, error) {
	if b.cfg.Archive == nil {
		return ImportReport{}, explained(conflict("no state"), "This engine has no state folder to import into.")
	}
	b.mu.Lock()
	if b.importing || b.staged {
		b.mu.Unlock()
		return ImportReport{}, explained(conflict("importing"), "An import is running or waiting for the engine to restart.")
	}
	b.importing = true
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.importing = false
		b.mu.Unlock()
	}()
	if why := b.empty(); why != "" {
		b.audit(ImportRefused, why)
		return ImportReport{}, explained(conflict("not empty"), why)
	}
	report, err := b.cfg.Archive.Stage(r)
	if err != nil {
		_ = b.cfg.Archive.Discard()
		b.audit(ImportRefused, err.Error())
		b.cfg.Logger.Warn("backup", "event", "import-refused")
		return ImportReport{}, explained(invalid("archive"), clip("The file wasn't imported: "+err.Error(), maxOutput))
	}
	if why := b.empty(); why != "" {
		_ = b.cfg.Archive.Discard()
		b.audit(ImportRefused, why)
		return ImportReport{}, explained(conflict("not empty"), why)
	}
	b.mu.Lock()
	b.staged = true
	b.mu.Unlock()
	b.audit(ImportStaged, "from engine "+report.Engine+", "+strconv.Itoa(report.Projects)+" projects and "+strconv.Itoa(report.Sessions)+" sessions; the engine restarts to finish")
	b.cfg.Logger.Info("backup", "event", "import-staged", "projects", report.Projects, "sessions", report.Sessions)
	if b.cfg.Restart != nil {
		time.AfterFunc(restartAfter, b.cfg.Restart)
	}
	return report, nil
}

func (b *Backups) Applied(report ImportReport) {
	reason := "from engine " + report.Engine + ", " + strconv.Itoa(report.Projects) + " projects and " + strconv.Itoa(report.Sessions) + " sessions"
	if report.State == ImportRefused {
		reason = report.Why
	}
	b.auditFormer(report.State, reason, report.FormerPerson)
	b.cfg.Logger.Info("backup", "event", "import-"+report.State)
}

func (b *Backups) View() (BackupView, error) {
	var v BackupView
	if b.cfg.Archive == nil {
		return v, explained(conflict("no state"), "This engine has no state folder.")
	}
	drops, err := b.cfg.Archive.Drops()
	if err != nil {
		return v, explained(conflict("drops"), "The engine couldn't read its counts of dropped records.")
	}
	v.Drops = drops
	if v.Copies, err = b.cfg.Archive.Copies(); err != nil {
		return v, explained(conflict("copies"), "The engine couldn't list its copies from before format changes.")
	}
	if last, ok, err := b.cfg.Archive.Last(); err == nil && ok {
		v.Last = &last
	}
	if b.cfg.Repos != nil {
		v.NeedClone = b.cfg.Repos.NeedClone()
	}
	v.Why = b.empty()
	b.mu.Lock()
	if b.staged {
		v.Why = "The engine restarts to finish an import."
	}
	b.mu.Unlock()
	v.CanImport = v.Why == ""
	return v, nil
}

func (b *Backups) CloneAgain(name string) (Cloning, error) {
	if b.cfg.Repos == nil {
		return Cloning{}, explained(conflict("no workspace"), "This engine has no workspace, so it can't clone.")
	}
	return b.cfg.Repos.Reclone(name)
}

func (d *Dispatcher) SessionCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.runs)
}

func (d *Dispatcher) HistoryBranches() map[string]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := map[string]string{}
	for sid, r := range d.runs {
		if r.history {
			out[sid] = r.branch
		}
	}
	return out
}

func (d *Dispatcher) HistoryOnly(sid string) error { return d.historyOnly(sid) }

func (d *Dispatcher) historyOnly(sid string) error {
	d.mu.Lock()
	r := d.runs[sid]
	history := r != nil && r.history
	doubled := r != nil && r.doubled
	old := r != nil && !r.history && r.box == ""
	d.mu.Unlock()
	if history {
		return explained(conflict("history only"), historyMessage)
	}
	if doubled {
		return explained(conflict("branch held"), doubledMessage)
	}
	if old {
		return oldLayout()
	}
	return nil
}

func (d *Dispatcher) forgetHistory(sid string) {
	d.mu.Lock()
	delete(d.runs, sid)
	d.mu.Unlock()
	d.saveRuns()
	d.reg.Forget(sid)
	if d.cfg.Work != nil {
		d.cfg.Work.Forget(sid)
	}
	d.workForget(sid)
}

func (r *Reviews) ShowHistory(branches map[string]string, list []ReviewEntry) int {
	shown := 0
	for sid, branch := range branches {
		var mine []ReviewEntry
		for _, e := range list {
			if e.Key.Branch == branch {
				mine = append(mine, e)
			}
		}
		if len(mine) == 0 {
			continue
		}
		latest := slices.MaxFunc(mine, func(a, b ReviewEntry) int { return entryTime(a).Compare(entryTime(b)) })
		view := ReviewView{Commit: latest.Key.Commit, Base: latest.Base, Branch: branch, Note: ImportedReviewNote}
		for _, e := range mine {
			if e.Key.Commit != latest.Key.Commit {
				continue
			}
			name := e.Role.Name()
			if e.Role == "" {
				name = e.Key.Agent
			}
			view.Roles = append(view.Roles, roleOf(RoleReview{Role: e.Role, Name: name, Agent: e.Key.Agent, Override: e.Record.Override, Since: e.Since, Previous: e.Since != ""}, e))
			view.CostUSD += e.CostUSD
		}
		slices.SortFunc(view.Roles, func(a, b RoleReview) int { return cmp.Or(cmp.Compare(a.Role, b.Role), cmp.Compare(a.Agent, b.Agent)) })
		r.show(sid, view)
		shown++
	}
	return shown
}

func entryTime(e ReviewEntry) time.Time {
	if !e.EndedAt.IsZero() {
		return e.EndedAt
	}
	return e.StartedAt
}
