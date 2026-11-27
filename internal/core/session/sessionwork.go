package session

import (
	"context"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	maxCommandProjects = 1000
	workFrame          = "session-work"
	commandImage       = "image"
	commandBrowser     = "browser"
)

type SessionWorkConfig struct {
	Repos    interface{ List() ([]Repository, error) }
	Commands ProjectCommandStore
	Queues   QueueStore
	Ports    PortStore
	Models   ModelStore
	Audit    Audit
	Note     func(sid, text string)
	PortNote func(sid, text string)
	Base     context.Context
	Now      func() time.Time
	Logger   shared.Logger

	HelperCount func(box string) (int, error)
}

type sessionRuns struct {
	turn     int
	check    *CommandRun
	setup    *CommandRun
	checking bool
	setting  bool
	failTurn int
	fails    int
}

type SessionWork struct {
	cfg      SessionWorkConfig
	reg      *Registry
	d        *Dispatcher
	mu       sync.Mutex
	saveMu   sync.Mutex
	projects map[ProjectID]ProjectCommands
	runs     map[string]*sessionRuns
	queues   map[string]*sessionQueue
	ports    map[string]int
	pending  map[string]int
	models   map[string]SessionModel
	roles    map[string]ModelChoice
	helperMu sync.Mutex
	helping  map[string]int
	swept    map[string]time.Time
}

type workFacts struct {
	Agent   string
	Cwd     string
	Project string
	Branch  string
	Running bool
	Idle    bool
	Login   bool
	Cards   int
	Push    bool
	Forge   bool
}

func NewSessionWork(reg *Registry, d *Dispatcher, cfg SessionWorkConfig) *SessionWork {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = quiet{}
	}
	if cfg.Base == nil {
		cfg.Base = context.Background()
	}
	if cfg.Note == nil {
		cfg.Note = func(string, string) {}
	}
	if cfg.PortNote == nil {
		cfg.PortNote = func(string, string) {}
	}
	cfg.Audit = masking(cfg.Audit)
	w := &SessionWork{cfg: cfg, reg: reg, d: d, projects: map[ProjectID]ProjectCommands{}, runs: map[string]*sessionRuns{},
		queues: map[string]*sessionQueue{}, ports: map[string]int{}, pending: map[string]int{}, models: map[string]SessionModel{}, roles: map[string]ModelChoice{},
		helping: map[string]int{}, swept: map[string]time.Time{}}
	w.load()
	reg.mu.Lock()
	reg.work = w
	reg.mu.Unlock()
	if d != nil {
		d.mu.Lock()
		d.work = w
		d.mu.Unlock()
	}
	return w
}

func (w *SessionWork) load() {
	if w.cfg.Commands != nil {
		if saved, err := w.cfg.Commands.Load(); err == nil {
			for id, c := range saved {
				check, okCheck := CheckProjectCommand(c.Check)
				setup, okSetup := CheckProjectCommand(c.Setup)
				image, okImage := CheckVerifyImage(c.Image)
				if okCheck && okSetup && okImage && id.Name != "" && id.URL != "" && len(w.projects) < maxCommandProjects {
					w.projects[id] = ProjectCommands{Check: check, Setup: setup, Image: image}
				}
			}
		} else {
			w.cfg.Logger.Warn("session work", "event", "commands-unreadable")
		}
	}
	w.loadQueues()
	w.loadPorts()
	w.loadModels()
}

func (w *SessionWork) runsOf(sid string) *sessionRuns {
	rs := w.runs[sid]
	if rs == nil {
		rs = &sessionRuns{}
		w.runs[sid] = rs
	}
	return rs
}

func (w *SessionWork) changed(sid string) {
	w.reg.Notify(Fields{"type": workFrame, "sid": sid})
}

func (w *SessionWork) command(project, kind string) string {
	return commandOf(w.Commands(project), kind)
}

func (w *SessionWork) Commands(project string) ProjectCommands {
	id, ok := w.projectID(project)
	if !ok {
		return ProjectCommands{}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.projects[id]
}

func (w *SessionWork) ProjectOf(project string) (ProjectID, ProjectCommands, bool) {
	id, ok := w.projectID(project)
	if !ok {
		return ProjectID{}, ProjectCommands{}, false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return id, w.projects[id], true
}

func (w *SessionWork) projectID(project string) (ProjectID, bool) {
	if w.cfg.Repos == nil || project == "" {
		return ProjectID{}, false
	}
	list, err := w.cfg.Repos.List()
	if err != nil {
		return ProjectID{}, false
	}
	i := slices.IndexFunc(list, func(r Repository) bool { return r.Name == project })
	if i < 0 || list[i].URL == "" {
		return ProjectID{}, false
	}
	return ProjectID{Name: project, URL: list[i].URL}, true
}

func (w *SessionWork) ForgetProject(id ProjectID) {
	w.saveMu.Lock()
	defer w.saveMu.Unlock()
	w.mu.Lock()
	_, had := w.projects[id]
	delete(w.projects, id)
	snapshot := maps.Clone(w.projects)
	w.mu.Unlock()
	if !had || w.cfg.Commands == nil {
		return
	}
	err := w.cfg.Commands.Save(snapshot)
	if err != nil {
		w.cfg.Logger.Warn("session work", "event", "commands-not-saved")
	}
}

func (w *SessionWork) SetCommands(project string, c ProjectCommands) (bool, error) {
	check, okCheck := CheckProjectCommand(c.Check)
	setup, okSetup := CheckProjectCommand(c.Setup)
	if !okCheck || !okSetup {
		field := "check"
		if okCheck {
			field = "setup"
		}
		return false, onField(field, invalid("command", "One line of at most 300 bytes, with no control characters."))
	}
	browser, okBrowser := CheckProjectCommand(c.Browser)
	if !okBrowser {
		return false, onField("browser", invalid("browser", "One line of at most 300 bytes, with no control characters."))
	}
	image, okImage := CheckVerifyImage(c.Image)
	if !okImage {
		return false, onField("image", invalid("image", "Needs a digest-pinned name like node@sha256:<64 hex>."))
	}
	id, known := w.projectID(project)
	if !known {
		return false, notFound("project", "The engine has no project of that name.")
	}
	w.saveMu.Lock()
	w.mu.Lock()
	before := w.projects[id]
	next := ProjectCommands{Check: check, Setup: setup, Image: image, Browser: browser}
	if before == next {
		w.mu.Unlock()
		w.saveMu.Unlock()
		return false, nil
	}
	if next == (ProjectCommands{}) {
		delete(w.projects, id)
	} else {
		if _, ok := w.projects[id]; !ok && len(w.projects) >= maxCommandProjects {
			w.mu.Unlock()
			w.saveMu.Unlock()
			return false, limited("projects", "The engine keeps commands for at most 1,000 projects.")
		}
		w.projects[id] = next
	}
	snapshot := maps.Clone(w.projects)
	w.mu.Unlock()
	if w.cfg.Commands != nil {
		if err := w.cfg.Commands.Save(snapshot); err != nil {
			w.mu.Lock()
			if before == (ProjectCommands{}) {
				delete(w.projects, id)
			} else {
				w.projects[id] = before
			}
			w.mu.Unlock()
			w.saveMu.Unlock()
			return false, conflict("state", "The engine couldn't save the project's commands.")
		}
	}
	w.saveMu.Unlock()
	for kind, pair := range map[string][2]string{CommandCheck: {before.Check, check}, CommandSetup: {before.Setup, setup}, commandImage: {before.Image, image}, commandBrowser: {before.Browser, browser}} {
		if pair[0] == pair[1] {
			continue
		}
		result := "set"
		if pair[1] == "" {
			result = "cleared"
		}
		w.audit(AuditEntry{Time: w.cfg.Now(), Repository: project, By: "page", Kind: AuditProjectCmd, Result: result, Reason: kind})
	}
	return true, nil
}

func (r *Registry) workInfo(sid string) (workFacts, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.admitted[sid]
	if a == nil {
		return workFacts{}, false
	}
	out := workFacts{Agent: a.Agent, Cwd: a.Cwd, Project: a.Project, Branch: a.Branch, Running: a.running, Idle: a.turnSince.IsZero()}
	for _, c := range r.conns {
		if c.sid == sid && c.login {
			out.Login = true
		}
	}
	for _, k := range r.cards {
		if k.sid == sid {
			out.Cards++
		}
	}
	if a.push != nil && !a.push.Running && (a.push.Result == "held" || a.push.Card != "") {
		out.Push = true
	}
	for _, f := range a.forge {
		if f.State == ForgeWaiting {
			out.Forge = true
		}
	}
	return out, true
}

func (r *Registry) workSeen(c *conn, kind string, ev map[string]any) {
	w := r.work
	if w == nil {
		return
	}
	switch {
	case kind == "busy":
		w.turnBegan(c.sid)
	case kind == "idle":
		reason, _ := ev["reason"].(string)
		go w.turnEnded(c.sid, reason)
	case cardTypes[kind]:
		go w.cardShown(c.sid)
	}
}

func (r *Registry) workDivert(sid, text string, attached, typed bool) (bool, error) {
	r.mu.Lock()
	w := r.work
	r.mu.Unlock()
	if w == nil {
		return false, nil
	}
	return w.divert(sid, text, attached, typed)
}

func (w *SessionWork) turnBegan(sid string) {
	w.mu.Lock()
	w.runsOf(sid).turn++
	w.mu.Unlock()
}

func (w *SessionWork) turnEnded(sid, reason string) {
	info, ok := w.reg.workInfo(sid)
	if !ok {
		return
	}
	if info.Agent != "claude" && plainEnd(reason) {
		w.checkAfterTurn(sid, info)
	}
	w.afterTurn(sid, reason)
}

func plainEnd(reason string) bool {
	return reason == "" || reason == "answer"
}

func (w *SessionWork) Forget(sid string) {
	w.mu.Lock()
	delete(w.runs, sid)
	_, hadQueue := w.queues[sid]
	delete(w.queues, sid)
	_, hadPorts := w.ports[sid]
	delete(w.ports, sid)
	_, hadModel := w.models[sid]
	delete(w.models, sid)
	w.mu.Unlock()
	w.helperMu.Lock()
	delete(w.swept, sid)
	w.helperMu.Unlock()
	if hadQueue {
		w.saveQueues()
	}
	if hadPorts {
		w.savePorts()
	}
	if hadModel {
		w.saveModels()
	}
}

func (d *Dispatcher) workOf() *SessionWork {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.work
}

func (d *Dispatcher) workForget(sid string) {
	if w := d.workOf(); w != nil {
		w.Forget(sid)
	}
	d.mu.Lock()
	hooks := slices.Clone(d.removed)
	d.mu.Unlock()
	for _, fn := range hooks {
		fn(sid)
	}
}

type SessionWorkView struct {
	Agent    string
	Model    SessionModel
	Offered  []AgentModel
	Queue    QueueView
	Check    *CommandRun
	Setup    *CommandRun
	Ports    int
	PortsEnv bool
	Commands ProjectCommands
}

func ClaudeOffered() []AgentModel {
	names := map[string]string{"fable": "Fable", "opus": "Opus", "sonnet": "Sonnet", "haiku": "Haiku"}
	out := []AgentModel{}
	for _, id := range ClaudeModels[1:] {
		out = append(out, AgentModel{ID: id, Name: names[id], Efforts: ClaudeEfforts})
	}
	return out
}

func AgentModels(agent string) []AgentModel {
	return agentModels(agent)
}

func AgentStartDefaults(agent string) ModelChoice {
	if agent == "claude" {
		return ModelChoice{Model: "opus", Effort: "high"}
	}
	return ModelChoice{}
}

func AgentEfforts(agent string) []string {
	if agent == "claude" {
		return ClaudeEfforts
	}
	return []string{}
}

func agentModels(agent string) []AgentModel {
	if agent == "claude" {
		return ClaudeOffered()
	}
	return []AgentModel{}
}

func (w *SessionWork) View(sid string) (SessionWorkView, error) {
	info, ok := w.reg.workInfo(sid)
	if !ok {
		return SessionWorkView{}, notFound("session", "The engine has no session of that id.")
	}
	v := SessionWorkView{Agent: info.Agent, Queue: w.Queue(sid), Ports: w.Ports(sid), Commands: w.Commands(info.Project)}
	v.Model, _ = w.SessionModel(sid)
	v.Model.Agent = info.Agent
	v.Check, v.Setup = w.Runs(sid)
	v.Offered = agentModels(info.Agent)
	v.PortsEnv = info.Agent == "claude" && len(claudePortEnv(v.Ports)) > 0
	return v, nil
}

func (w *SessionWork) audit(e AuditEntry) {
	if w.cfg.Audit != nil {
		_ = w.cfg.Audit.Append(e)
	}
}
