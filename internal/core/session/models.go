package session

import (
	"context"
	"maps"
	"regexp"
	"slices"

	"github.com/bezmoradi/software-factory/internal/core/review"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	AuditModelSwitch = "model-switch"
	AuditRoleModel   = "review-model"
	maxModelSessions = 1000
	maxRoleModels    = 4000
	ModelsFrom       = "the next turn"
)

var (
	ClaudeEfforts = []string{"low", "medium", "high", "xhigh", "max"}
	modelName     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,99}$`)
	effortName    = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,19}$`)
)

type AgentModel struct {
	ID      string
	Name    string
	Efforts []string
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
	w.saveMu.Lock()
	w.mu.Lock()
	st := ModelState{Sessions: maps.Clone(w.models)}
	for k, c := range w.roles {
		parts := splitRoleKey(k)
		st.Roles = append(st.Roles, RoleModel{Project: parts[0], URL: parts[1], Role: review.Role(parts[2]), Model: c.Model, Effort: c.Effort})
	}
	w.mu.Unlock()
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

func claudeChoice(model, effort string) error {
	if !slices.Contains(ClaudeModels, model) {
		return invalid("model", "That agent can't be started with that model from the page.")
	}
	if effort != "" && !slices.Contains(ClaudeEfforts, effort) {
		return invalid("effort", "Claude Code's effort is low, medium, high, xhigh or max.")
	}
	return nil
}

func checkChoice(spec AgentSpec, model, effort string) error {
	if spec.Method == MethodPlugin {
		return claudeChoice(model, effort)
	}
	if model != "" || effort != "" {
		return invalid("model", "That agent can't be started with that model from the page.")
	}
	return nil
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
		return notFound("session", "The engine has no session of that id.")
	}
	if err := w.branchHeld(sid); err != nil {
		return err
	}
	if info.Agent != "claude" {
		return conflict("agent", "This agent's model can't be switched from the page.")
	}
	if model != "" && !slices.Contains(ClaudeModels[1:], model) {
		return invalid("model", "Claude Code switches only to the models the start form offers.")
	}
	if err := claudeChoice(model, effort); err != nil {
		return err
	}
	w.mu.Lock()
	if _, ok := w.models[sid]; !ok && len(w.models) >= maxModelSessions {
		w.mu.Unlock()
		return limited("models", "The engine keeps model choices for at most 1,000 sessions.")
	}
	m := w.models[sid]
	m.Agent, m.Model, m.Effort = info.Agent, model, effort
	w.models[sid] = m
	w.mu.Unlock()
	w.saveModels()
	result := model
	if result == "" {
		result = "start model"
	}
	w.audit(AuditEntry{Req: shared.RequestOf(ctx), Time: w.cfg.Now(), Session: sid, Repository: info.Project, By: "page", Kind: AuditModelSwitch, Result: result, Reason: effort})
	w.changed(sid)
	return nil
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
		return nil, notFound("project", "That project is no longer in the list.")
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
		return nil, invalid("role", "That isn't a review role.")
	}
	if !ValidReviewChoice(model, effort) {
		return nil, invalid("model", "A review role runs with one of the start form's models and Claude Code's efforts.")
	}
	repo, ok := w.repoOf(path)
	if !ok {
		return nil, notFound("project", "That project is no longer in the list.")
	}
	key := roleKey(repo.Name, repo.URL, role)
	w.mu.Lock()
	if model == "" && effort == "" {
		delete(w.roles, key)
	} else {
		if _, ok := w.roles[key]; !ok && len(w.roles) >= maxRoleModels {
			w.mu.Unlock()
			return nil, limited("roles", "The engine keeps model choices for at most 4,000 review roles.")
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
