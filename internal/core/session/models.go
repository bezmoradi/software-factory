package session

import (
	"context"
	"errors"
	"io"
	"maps"
	"regexp"
	"slices"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/review"
)

const (
	AuditModelSwitch = "model-switch"
	AuditRoleModel   = "review-model"
	maxAgentModels   = 50
	maxModelSessions = 1000
	maxRoleModels    = 4000
	modelListFor     = 10 * time.Minute
	modelProbeFor    = 30 * time.Second
	ModelsFrom       = "the next turn"
)

var errNoModels = errors.New("no model list")

var (
	ClaudeEfforts        = []string{"low", "medium", "high", "xhigh", "max"}
	CodexFallbackEfforts = []string{"low", "medium", "high"}
	modelName            = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,99}$`)
	effortName           = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,19}$`)
)

type AgentModel struct {
	ID            string
	Name          string
	Efforts       []string
	DefaultEffort string
	Default       bool
}

type ModelLister interface {
	ListModels(ctx context.Context, rw io.ReadWriteCloser) ([]AgentModel, error)
}

type ModelReporter interface {
	Models() []AgentModel
}

type TurnSettingSession interface {
	SetTurnSettings(model, effort string)
}

type ModelChoice struct {
	Model  string
	Effort string
}

type SessionModel struct {
	Agent       string
	StartModel  string
	StartEffort string
	Model       string
	Effort      string
}

type RoleModel struct {
	Project string
	URL     string
	Role    review.Role
	Model   string
	Effort  string
}

type ModelState struct {
	Sessions map[string]SessionModel
	Roles    []RoleModel
}

type ModelStore interface {
	Load() (ModelState, error)
	Save(ModelState) error
}

type codexModels struct {
	list    []AgentModel
	at      time.Time
	known   bool
	probing bool
}

func (m SessionModel) Now() ModelChoice {
	c := ModelChoice{Model: m.StartModel, Effort: m.StartEffort}
	if m.Model != "" {
		c.Model = m.Model
	}
	if m.Effort != "" {
		c.Effort = m.Effort
	}
	return c
}

func CleanModels(list []AgentModel) []AgentModel {
	out := []AgentModel{}
	for _, m := range list {
		if !modelName.MatchString(m.ID) || len(out) >= maxAgentModels || slices.ContainsFunc(out, func(x AgentModel) bool { return x.ID == m.ID }) {
			continue
		}
		c := AgentModel{ID: m.ID, Name: CleanLine(m.Name, 80), Default: m.Default}
		if c.Name == "" {
			c.Name = m.ID
		}
		for _, e := range m.Efforts {
			if effortName.MatchString(e) && !slices.Contains(c.Efforts, e) && len(c.Efforts) < 12 {
				c.Efforts = append(c.Efforts, e)
			}
		}
		if effortName.MatchString(m.DefaultEffort) {
			c.DefaultEffort = m.DefaultEffort
		}
		out = append(out, c)
	}
	return out
}

func roleKey(project, url string, role review.Role) string {
	return project + "\x00" + url + "\x00" + string(role)
}

func (w *SessionWork) loadModels() {
	if w.cfg.Models == nil {
		return
	}
	st, err := w.cfg.Models.Load()
	if err != nil {
		w.cfg.Logger.Warn("session work", "event", "models-unreadable")
		return
	}
	for sid, m := range st.Sessions {
		if sidPattern.MatchString(sid) && len(w.models) < maxModelSessions && validSessionModel(m) {
			w.models[sid] = m
		}
	}
	for _, r := range st.Roles {
		if slices.Contains(review.Roles, r.Role) && r.Project != "" && len(w.roles) < maxRoleModels && ValidReviewChoice(r.Model, r.Effort) {
			w.roles[roleKey(r.Project, r.URL, r.Role)] = ModelChoice{Model: r.Model, Effort: r.Effort}
		}
	}
}

func validSessionModel(m SessionModel) bool {
	for _, v := range []string{m.StartModel, m.Model} {
		if v != "" && !modelName.MatchString(v) {
			return false
		}
	}
	for _, v := range []string{m.StartEffort, m.Effort} {
		if v != "" && !effortName.MatchString(v) {
			return false
		}
	}
	return m.Agent != ""
}

func ValidReviewChoice(model, effort string) bool {
	return (model == "" || slices.Contains(ClaudeModels[1:], model)) && (effort == "" || slices.Contains(ClaudeEfforts, effort))
}

func (w *SessionWork) saveModels() {
	if w.cfg.Models == nil {
		return
	}
	w.mu.Lock()
	st := ModelState{Sessions: maps.Clone(w.models)}
	for k, c := range w.roles {
		parts := splitRoleKey(k)
		st.Roles = append(st.Roles, RoleModel{Project: parts[0], URL: parts[1], Role: review.Role(parts[2]), Model: c.Model, Effort: c.Effort})
	}
	w.mu.Unlock()
	w.saveMu.Lock()
	err := w.cfg.Models.Save(st)
	w.saveMu.Unlock()
	if err != nil {
		w.cfg.Logger.Warn("session work", "event", "models-not-saved")
	}
}

func splitRoleKey(k string) [3]string {
	var out [3]string
	i := 0
	start := 0
	for j := 0; j < len(k) && i < 2; j++ {
		if k[j] == 0 {
			out[i] = k[start:j]
			i++
			start = j + 1
		}
	}
	out[i] = k[start:]
	return out
}

func effortsFor(list []AgentModel, known bool, model string) []string {
	if !known {
		return CodexFallbackEfforts
	}
	for _, m := range list {
		if (model != "" && m.ID == model) || (model == "" && m.Default) {
			if len(m.Efforts) > 0 {
				return m.Efforts
			}
		}
	}
	return CodexFallbackEfforts
}

func (w *SessionWork) codexChoice(ctx context.Context, model, effort, box string) error {
	if model == "" && effort == "" {
		return nil
	}
	list, known := w.CodexModels(ctx, box)
	if model != "" && (!known || !slices.ContainsFunc(list, func(m AgentModel) bool { return m.ID == model })) {
		return explained(invalid("model"), "Codex's model list doesn't offer that model.")
	}
	if effort != "" && !slices.Contains(effortsFor(list, known, model), effort) {
		return explained(invalid("effort"), "That model doesn't offer that effort.")
	}
	return nil
}

func claudeChoice(model, effort string) error {
	if !slices.Contains(ClaudeModels, model) {
		return explained(invalid("model"), "That agent can't be started with that model from the page.")
	}
	if effort != "" && !slices.Contains(ClaudeEfforts, effort) {
		return explained(invalid("effort"), "Claude Code's effort is low, medium, high, xhigh or max.")
	}
	return nil
}

func (d *Dispatcher) checkChoice(ctx context.Context, spec AgentSpec, model, effort string) error {
	switch spec.Method {
	case MethodPlugin:
		return claudeChoice(model, effort)
	case MethodCodex:
		w := d.workOf()
		if w == nil {
			if model != "" || effort != "" {
				return explained(invalid("model"), "This engine can't choose Codex's model or effort.")
			}
			return nil
		}
		return w.codexChoice(ctx, model, effort, "")
	}
	if model != "" || effort != "" {
		return explained(invalid("model"), "That agent can't be started with that model from the page.")
	}
	return nil
}

func (w *SessionWork) CodexModels(ctx context.Context, box string) ([]AgentModel, bool) {
	w.mu.Lock()
	c := w.codex
	fresh := c.known && w.cfg.Now().Sub(c.at) < modelListFor
	if fresh || c.probing || w.cfg.Lister == nil || w.d == nil || !w.d.liveBox(box) {
		w.mu.Unlock()
		return slices.Clone(c.list), c.known
	}
	w.codex.probing = true
	w.mu.Unlock()
	list, err := w.probeCodex(ctx, box)
	w.mu.Lock()
	w.codex.probing = false
	if err == nil && len(list) > 0 {
		w.codex = codexModels{list: list, at: w.cfg.Now(), known: true}
	}
	c = w.codex
	w.mu.Unlock()
	if err != nil {
		w.cfg.Logger.Info("codex models", "event", "probe-failed")
	}
	return slices.Clone(c.list), c.known
}

func (w *SessionWork) probeCodex(ctx context.Context, box string) ([]AgentModel, error) {
	d := w.d
	spec, ok := findAgent(d.cfg.Agents, "codex")
	dir := BoxFolder(d.cfg.Workspace, box)
	if !ok || !spec.Enabled || dir == "" {
		return nil, errNoModels
	}
	if spec = d.installed(spec); !spec.Enabled {
		return nil, errNoModels
	}
	if err := d.ConfirmBox(ctx, box); err != nil {
		return nil, err
	}
	host := d.hostFor(box)
	pctx, cancel := context.WithTimeout(ctx, modelProbeFor)
	defer cancel()
	p, err := host.Start(pctx, Command{Argv: spec.Command, Dir: dir, Pass: spec.Pass})
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = p.Close()
		sctx, scancel := context.WithTimeout(context.Background(), stopFor)
		_ = host.Stop(sctx, p.ID())
		scancel()
	}()
	list, err := w.cfg.Lister.ListModels(pctx, p)
	if err != nil {
		return nil, err
	}
	return CleanModels(list), nil
}

func (w *SessionWork) keepModels(sess any) {
	m, ok := sess.(ModelReporter)
	if !ok {
		return
	}
	list := CleanModels(m.Models())
	if len(list) == 0 {
		return
	}
	w.mu.Lock()
	w.codex = codexModels{list: list, at: w.cfg.Now(), known: true}
	w.mu.Unlock()
}

func (w *SessionWork) recordStart(sid, agent, model, effort string) {
	if sid == "" || (model == "" && effort == "") {
		return
	}
	w.mu.Lock()
	if _, ok := w.models[sid]; !ok && len(w.models) >= maxModelSessions {
		w.mu.Unlock()
		return
	}
	w.models[sid] = SessionModel{Agent: agent, StartModel: model, StartEffort: effort}
	w.mu.Unlock()
	w.saveModels()
}

func (w *SessionWork) SessionModel(sid string) (SessionModel, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	m, ok := w.models[sid]
	return m, ok
}

func (w *SessionWork) ClaudeOverride(sid string) ModelChoice {
	w.mu.Lock()
	defer w.mu.Unlock()
	m := w.models[sid]
	if m.Agent != "claude" {
		return ModelChoice{}
	}
	return ModelChoice{Model: m.Model, Effort: m.Effort}
}

func (w *SessionWork) SwitchModel(ctx context.Context, sid, model, effort string) error {
	info, ok := w.reg.workInfo(sid)
	if !ok {
		return explained(notFound("session"), "The engine has no session of that id.")
	}
	switch info.Agent {
	case "claude":
		if model != "" && !slices.Contains(ClaudeModels[1:], model) {
			return explained(invalid("model"), "Claude Code switches only to the models the start form offers.")
		}
		if err := claudeChoice(model, effort); err != nil {
			return err
		}
	case "codex":
		box := ""
		if w.d != nil {
			box, _ = w.d.SessionBox(sid)
		}
		if err := w.codexChoice(ctx, model, effort, box); err != nil {
			return err
		}
		if m, _ := w.SessionModel(sid); model == "" && m.StartModel == "" {
			return explained(invalid("model"), "Name a model: Codex keeps the last model it was given.")
		}
	default:
		return explained(conflict("agent"), "This agent's model can't be switched from the page.")
	}
	w.mu.Lock()
	if _, ok := w.models[sid]; !ok && len(w.models) >= maxModelSessions {
		w.mu.Unlock()
		return explained(limited("models"), "The engine keeps model choices for at most 1,000 sessions.")
	}
	m := w.models[sid]
	m.Agent, m.Model, m.Effort = info.Agent, model, effort
	w.models[sid] = m
	now := m.Now()
	w.mu.Unlock()
	w.saveModels()
	if info.Agent == "codex" && w.d != nil {
		w.d.turnSettings(sid, now)
	}
	result := model
	if result == "" {
		result = "start model"
	}
	w.audit(AuditEntry{Time: w.cfg.Now(), Session: sid, Repository: info.Project, By: "page", Kind: AuditModelSwitch, Result: result, Reason: effort})
	w.changed(sid)
	return nil
}

func (d *Dispatcher) turnSettings(sid string, c ModelChoice) {
	d.mu.Lock()
	r := d.runs[sid]
	var b *bridge
	if r != nil {
		b = r.bridge
	}
	d.mu.Unlock()
	if b == nil {
		return
	}
	b.mu.Lock()
	sess := b.sess
	b.mu.Unlock()
	if t, ok := sess.(TurnSettingSession); ok {
		t.SetTurnSettings(c.Model, c.Effort)
	}
}

func (d *Dispatcher) launchChoice(r *run) ModelChoice {
	if w := d.workOf(); w != nil && r.sid != "" {
		if m, ok := w.SessionModel(r.sid); ok {
			return m.Now()
		}
	}
	return ModelChoice{Model: r.model, Effort: r.effort}
}

func (d *Dispatcher) keepModels(sess any) {
	if w := d.workOf(); w != nil {
		w.keepModels(sess)
	}
}

func (w *SessionWork) repoOf(path string) (Repository, bool) {
	if w.cfg.Repos == nil {
		return Repository{}, false
	}
	list, err := w.cfg.Repos.List()
	if err != nil {
		return Repository{}, false
	}
	i := slices.IndexFunc(list, func(r Repository) bool { return r.Path == path })
	if i < 0 {
		if name, ok := CloneProject(path); ok {
			i = slices.IndexFunc(list, func(r Repository) bool { return r.Name == name })
		}
	}
	if i < 0 {
		return Repository{}, false
	}
	return list[i], true
}

func (w *SessionWork) RoleModels(path string) (map[review.Role]ModelChoice, error) {
	repo, ok := w.repoOf(path)
	if !ok {
		return nil, explained(notFound("project"), "That project is no longer in the list.")
	}
	out := map[review.Role]ModelChoice{}
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, role := range review.Roles {
		out[role] = w.roles[roleKey(repo.Name, repo.URL, role)]
	}
	return out, nil
}

func (w *SessionWork) SetRoleModel(path string, role review.Role, model, effort string) (map[review.Role]ModelChoice, error) {
	if !slices.Contains(review.Roles, role) {
		return nil, explained(invalid("role"), "That isn't a review role.")
	}
	if !ValidReviewChoice(model, effort) {
		return nil, explained(invalid("model"), "A review role runs with one of the start form's models and Claude Code's efforts.")
	}
	repo, ok := w.repoOf(path)
	if !ok {
		return nil, explained(notFound("project"), "That project is no longer in the list.")
	}
	key := roleKey(repo.Name, repo.URL, role)
	w.mu.Lock()
	if model == "" && effort == "" {
		delete(w.roles, key)
	} else {
		if _, ok := w.roles[key]; !ok && len(w.roles) >= maxRoleModels {
			w.mu.Unlock()
			return nil, explained(limited("roles"), "The engine keeps model choices for at most 4,000 review roles.")
		}
		w.roles[key] = ModelChoice{Model: model, Effort: effort}
	}
	w.mu.Unlock()
	w.saveModels()
	w.audit(AuditEntry{Time: w.cfg.Now(), Repository: repo.Name, By: "page", Kind: AuditRoleModel, Result: model, Reason: effort, Reviewer: string(role)})
	return w.RoleModels(path)
}

func (w *SessionWork) RoleModel(path string, role review.Role) ModelChoice {
	repo, ok := w.repoOf(path)
	if !ok {
		return ModelChoice{}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.roles[roleKey(repo.Name, repo.URL, role)]
}

func (r *Reviews) UseRoleModels(f func(path string, role review.Role) ModelChoice) {
	r.mu.Lock()
	r.roleModels = f
	r.mu.Unlock()
}

func (r *Reviews) roleModelFor(path string, role review.Role) ModelChoice {
	r.mu.Lock()
	f := r.roleModels
	r.mu.Unlock()
	if f == nil || path == "" {
		return ModelChoice{}
	}
	return f(path, role)
}
