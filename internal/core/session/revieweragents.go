package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/review"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	AuditReviewerAgent = "reviewer-agent"
	ResultAdded        = "added"
	ResultReplaced     = "replaced"
	ResultRemoved      = "removed"
	ResultPicked       = "picked"
)

type StoredAgentFile struct {
	Project string
	URL     string
	Text    string
	At      time.Time
}

type AgentFileRole struct {
	Project string
	URL     string
	Role    review.Role
	Name    string
}

type AgentFileState struct {
	Files []StoredAgentFile
	Roles []AgentFileRole
}

type AgentFileStore interface {
	Load() (AgentFileState, error)
	Save(AgentFileState) error
}

type AgentFileOut struct {
	Name string
	Data []byte
}

type AgentFileHost interface {
	PutAgents(ctx context.Context, files []AgentFileOut, keep []string) error
}

type RoleAgents interface {
	RolesFor(project string) map[review.Role]string
	Offer(agent string) review.Offer
}

type ReviewAgentFiles interface {
	Uploaded(agent string) (UploadedAgent, bool)
	Files() []string
}

type UploadedAgent struct {
	ID      string
	Project string
	URL     string
	SHA     string
	Bytes   int
	At      time.Time
	File    AgentFile
}

func (u UploadedAgent) Out() AgentFileOut {
	return AgentFileOut{Name: AgentFileOf(u.ID), Data: []byte(u.File.Render(u.ID))}
}

type AgentFileView struct {
	Name        string
	Agent       string
	Description string
	Model       string
	Tools       []string
	Bytes       int
	SHA         string
	At          time.Time
}

type ReviewerAgentsView struct {
	Files    []AgentFileView
	Roles    map[review.Role]string
	Defaults map[review.Role]string
}

type ReviewerAgentsPage interface {
	AgentFiles(path string) (ReviewerAgentsView, error)
	AddAgentFile(path, text string) (ReviewerAgentsView, error)
	RemoveAgentFile(path, name string) (ReviewerAgentsView, error)
	PickAgentFile(path string, role review.Role, name string) (ReviewerAgentsView, error)
}

type ReviewerAgentsConfig struct {
	Store  AgentFileStore
	Packs  ProjectPacks
	Repos  interface{ List() ([]Repository, error) }
	Audit  Audit
	Now    func() time.Time
	Logger shared.Logger
}

type ReviewerAgents struct {
	cfg ReviewerAgentsConfig
	mu  sync.Mutex
}

var (
	_ RoleAgents         = (*ReviewerAgents)(nil)
	_ ReviewAgentFiles   = (*ReviewerAgents)(nil)
	_ ReviewerAgentsPage = (*ReviewerAgents)(nil)
)

func NewReviewerAgents(cfg ReviewerAgentsConfig) (*ReviewerAgents, error) {
	if cfg.Store == nil || cfg.Repos == nil {
		return nil, errors.New("reviewer agents: need the state file and the repositories")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = quiet{}
	}
	cfg.Audit = masking(cfg.Audit)
	return &ReviewerAgents{cfg: cfg}, nil
}

func uploadedOf(f StoredAgentFile) (UploadedAgent, bool) {
	a, problems := ParseAgentFile(f.Text)
	if len(problems) > 0 || len(a.Name) > MaxAgentName {
		return UploadedAgent{}, false
	}
	sum := sha256.Sum256([]byte(f.Text))
	return UploadedAgent{ID: AgentID(a.Name, f.Text), Project: f.Project, URL: f.URL, SHA: hex.EncodeToString(sum[:]), Bytes: len(f.Text), At: f.At, File: a}, true
}

func (a *ReviewerAgents) state() (AgentFileState, []UploadedAgent) {
	if a == nil {
		return AgentFileState{}, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	st, err := a.cfg.Store.Load()
	if err != nil {
		a.cfg.Logger.Warn("reviewer agents", "event", "state-unreadable")
		return AgentFileState{}, nil
	}
	var out []UploadedAgent
	for _, f := range st.Files {
		if u, ok := uploadedOf(f); ok {
			out = append(out, u)
		} else {
			a.cfg.Logger.Warn("reviewer agents", "event", "stored-file-unusable")
		}
	}
	return st, out
}

func (a *ReviewerAgents) repoNamed(name string) (Repository, bool) {
	list, err := a.cfg.Repos.List()
	if err != nil {
		return Repository{}, false
	}
	i := slices.IndexFunc(list, func(r Repository) bool { return r.Name == name })
	if i < 0 {
		return Repository{}, false
	}
	return list[i], true
}

func (a *ReviewerAgents) RolesFor(project string) map[review.Role]string {
	if a == nil {
		return nil
	}
	r, ok := a.repoNamed(project)
	if !ok {
		return nil
	}
	st, files := a.state()
	out := map[review.Role]string{}
	for _, pick := range st.Roles {
		if pick.Project != r.Name || pick.URL != r.URL || !slices.Contains(review.Roles, pick.Role) {
			continue
		}
		i := slices.IndexFunc(files, func(u UploadedAgent) bool {
			return u.Project == r.Name && u.URL == r.URL && u.File.Name == pick.Name
		})
		if i >= 0 {
			out[pick.Role] = files[i].ID
		}
	}
	return out
}

func (a *ReviewerAgents) Uploaded(agent string) (UploadedAgent, bool) {
	if a == nil || !AgentFileID.MatchString(agent) {
		return UploadedAgent{}, false
	}
	_, files := a.state()
	i := slices.IndexFunc(files, func(u UploadedAgent) bool { return u.ID == agent })
	if i < 0 {
		return UploadedAgent{}, false
	}
	return files[i], true
}

func (a *ReviewerAgents) Files() []string {
	_, files := a.state()
	out := []string{}
	for _, u := range files {
		if name := AgentFileOf(u.ID); !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func (a *ReviewerAgents) Offer(agent string) review.Offer {
	if _, ok := a.Uploaded(agent); ok {
		return review.Offer{Agent: agent, Source: "userSettings", Plugin: UploadedProvider}
	}
	return pluginOffer(agent)
}

func pluginOffer(agent string) review.Offer {
	if prefix, _, ok := strings.Cut(agent, ":"); ok && prefix != PackKnackpack {
		return review.Offer{Agent: agent, Source: "plugin", Plugin: review.PackProvider(prefix)}
	}
	return review.Offer{Agent: agent, Source: "plugin", Plugin: ReviewProvider}
}

func offerFor(a RoleAgents, agent string) review.Offer {
	if a == nil {
		return pluginOffer(agent)
	}
	return a.Offer(agent)
}

func providerFor(a ReviewAgentFiles, pack ReviewerPack, agent string) string {
	if a != nil {
		if _, ok := a.Uploaded(agent); ok {
			return UploadedProvider
		}
	}
	prefix, _, prefixed := strings.Cut(agent, ":")
	switch {
	case pack.None():
		return ""
	case prefixed && prefix == pack.Name:
		return pack.Provider
	case !prefixed && pack.Name == PackKnackpack:
		return ReviewProvider
	}
	return ""
}

func (a *ReviewerAgents) repoByPath(path string) (Repository, error) {
	list, err := a.cfg.Repos.List()
	if err != nil {
		return Repository{}, conflict("records", "The engine couldn't read its list of repositories.")
	}
	i := slices.IndexFunc(list, func(r Repository) bool { return r.Path == path })
	if i < 0 {
		return Repository{}, notFound("project", "That project is no longer in the list.")
	}
	return list[i], nil
}

func (a *ReviewerAgents) viewFor(r Repository, st AgentFileState, files []UploadedAgent) ReviewerAgentsView {
	v := ReviewerAgentsView{Roles: map[review.Role]string{}, Defaults: a.defaultsFor(r.Name)}
	for _, u := range files {
		if u.Project != r.Name || u.URL != r.URL {
			continue
		}
		v.Files = append(v.Files, AgentFileView{Name: u.File.Name, Agent: u.ID, Description: u.File.Description, Model: u.File.Model,
			Tools: slices.Clone(u.File.Tools), Bytes: u.Bytes, SHA: u.SHA, At: u.At})
	}
	for _, pick := range st.Roles {
		if pick.Project == r.Name && pick.URL == r.URL && slices.ContainsFunc(v.Files, func(f AgentFileView) bool { return f.Name == pick.Name }) {
			v.Roles[pick.Role] = pick.Name
		}
	}
	return v
}

func (a *ReviewerAgents) defaultsFor(project string) map[review.Role]string {
	out := map[review.Role]string{}
	var ps PackSetup
	if a.cfg.Packs != nil {
		ps = a.cfg.Packs.PackSetup(project)
	}
	for _, role := range review.Roles {
		out[role] = DefaultReviewers[role]
		if a.cfg.Packs != nil && ps.Kind != PackKnackpack {
			out[role] = ps.Roles[role]
		}
	}
	return out
}

func (a *ReviewerAgents) AgentFiles(path string) (ReviewerAgentsView, error) {
	r, err := a.repoByPath(path)
	if err != nil {
		return ReviewerAgentsView{}, err
	}
	st, files := a.state()
	return a.viewFor(r, st, files), nil
}

func (a *ReviewerAgents) change(r Repository, fn func(*AgentFileState) error) (ReviewerAgentsView, error) {
	a.mu.Lock()
	st, err := a.cfg.Store.Load()
	if err != nil {
		a.mu.Unlock()
		return ReviewerAgentsView{}, conflict("state", "The engine couldn't read its reviewer agent files.")
	}
	if err := fn(&st); err != nil {
		a.mu.Unlock()
		return ReviewerAgentsView{}, err
	}
	if err := a.cfg.Store.Save(st); err != nil {
		a.mu.Unlock()
		return ReviewerAgentsView{}, conflict("state", "The engine couldn't save its reviewer agent files.")
	}
	a.mu.Unlock()
	st, files := a.state()
	return a.viewFor(r, st, files), nil
}

func ownedBy(r Repository) func(project, url string) bool {
	return func(project, url string) bool { return project == r.Name && url == r.URL }
}

func (a *ReviewerAgents) AddAgentFile(path, text string) (ReviewerAgentsView, error) {
	r, err := a.repoByPath(path)
	if err != nil {
		return ReviewerAgentsView{}, err
	}
	text = normalAgentText(text)
	parsed, problems := ParseAgentFile(text)
	if len(problems) == 0 && len(parsed.Name) > MaxAgentName {
		problems = []string{"name is over " + strconv.Itoa(MaxAgentName) + " characters"}
	}
	if len(problems) > 0 {
		a.cfg.Logger.Info("reviewer agents", "event", "refused", "problems", len(problems))
		return ReviewerAgentsView{}, invalid("agent file", "The engine refused this agent file: "+strings.Join(problems[:min(len(problems), 8)], "; ")+".")
	}
	mine := ownedBy(r)
	result := ResultAdded
	v, err := a.change(r, func(st *AgentFileState) error {
		kept := st.Files[:0:0]
		count := 0
		for _, f := range st.Files {
			if u, ok := uploadedOf(f); ok && mine(f.Project, f.URL) && u.File.Name == parsed.Name {
				result = ResultReplaced
				continue
			}
			if mine(f.Project, f.URL) {
				count++
			}
			kept = append(kept, f)
		}
		if count >= MaxProjectAgents {
			return limited("agent files", "This project already keeps "+strconv.Itoa(MaxProjectAgents)+" agent files, its limit. Remove one first.")
		}
		if len(kept) >= MaxAgentFilesAll {
			return limited("agent files", "The engine already keeps "+strconv.Itoa(MaxAgentFilesAll)+" agent files across projects, its limit. Remove one first.")
		}
		st.Files = append(kept, StoredAgentFile{Project: r.Name, URL: r.URL, Text: text, At: a.cfg.Now()})
		return nil
	})
	if err != nil {
		return ReviewerAgentsView{}, err
	}
	sum := sha256.Sum256([]byte(text))
	a.audit(AuditEntry{Repository: r.Name, Result: result, Reviewer: AgentID(parsed.Name, text), Hash: hex.EncodeToString(sum[:]),
		Reason: "the developer " + result + " the reviewer agent file " + parsed.Name + " on the page"})
	a.cfg.Logger.Info("reviewer agents", "event", result)
	return v, nil
}

func (a *ReviewerAgents) RemoveAgentFile(path, name string) (ReviewerAgentsView, error) {
	r, err := a.repoByPath(path)
	if err != nil {
		return ReviewerAgentsView{}, err
	}
	mine := ownedBy(r)
	removed := ""
	v, err := a.change(r, func(st *AgentFileState) error {
		st.Files = slices.DeleteFunc(st.Files, func(f StoredAgentFile) bool {
			u, ok := uploadedOf(f)
			if mine(f.Project, f.URL) && (ok && u.File.Name == name) {
				removed = u.ID
				return true
			}
			return false
		})
		if removed == "" {
			return notFound("agent file", "This project has no agent file named "+clip(name, 80)+".")
		}
		st.Roles = slices.DeleteFunc(st.Roles, func(p AgentFileRole) bool { return mine(p.Project, p.URL) && p.Name == name })
		return nil
	})
	if err != nil {
		return ReviewerAgentsView{}, err
	}
	a.audit(AuditEntry{Repository: r.Name, Result: ResultRemoved, Reviewer: removed,
		Reason: "the developer removed the reviewer agent file " + clip(name, 80) + " on the page; its roles fall back to the project's pack"})
	a.cfg.Logger.Info("reviewer agents", "event", ResultRemoved)
	return v, nil
}

func (a *ReviewerAgents) PickAgentFile(path string, role review.Role, name string) (ReviewerAgentsView, error) {
	if !slices.Contains(review.Roles, role) {
		return ReviewerAgentsView{}, invalid("role", "Only the bug hunter and simplicity checker roles can be filled by an agent file.")
	}
	r, err := a.repoByPath(path)
	if err != nil {
		return ReviewerAgentsView{}, err
	}
	mine := ownedBy(r)
	agent := a.defaultsFor(r.Name)[role]
	v, err := a.change(r, func(st *AgentFileState) error {
		if name != "" {
			i := slices.IndexFunc(st.Files, func(f StoredAgentFile) bool {
				u, ok := uploadedOf(f)
				return ok && mine(f.Project, f.URL) && u.File.Name == name
			})
			if i < 0 {
				return notFound("agent file", "This project has no agent file named "+clip(name, 80)+".")
			}
			u, _ := uploadedOf(st.Files[i])
			agent = u.ID
		}
		st.Roles = slices.DeleteFunc(st.Roles, func(p AgentFileRole) bool { return mine(p.Project, p.URL) && p.Role == role })
		if name != "" {
			st.Roles = append(st.Roles, AgentFileRole{Project: r.Name, URL: r.URL, Role: role, Name: name})
		}
		return nil
	})
	if err != nil {
		return ReviewerAgentsView{}, err
	}
	picked := agent
	if picked == "" {
		picked = "no agent"
	}
	a.audit(AuditEntry{Repository: r.Name, Result: ResultPicked, Reviewer: agent,
		Reason: "the developer picked " + picked + " for the " + role.Name() + " role on the page"})
	a.cfg.Logger.Info("reviewer agents", "event", ResultPicked)
	return v, nil
}

func (a *ReviewerAgents) audit(e AuditEntry) {
	if a.cfg.Audit == nil {
		return
	}
	e.Time, e.By, e.Kind = a.cfg.Now(), ByPage, AuditReviewerAgent
	if err := a.cfg.Audit.Append(e); err != nil {
		a.cfg.Logger.Warn("reviewer agents", "event", "audit-not-written")
	}
}

func ProjectAgentsFull(files int) string {
	if files < MaxProjectAgents {
		return ""
	}
	return "This project keeps " + strconv.Itoa(MaxProjectAgents) + " agent files, its limit. Remove one to add another."
}
