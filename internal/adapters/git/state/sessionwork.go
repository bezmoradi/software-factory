package state

import (
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/review"
	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	projectCommandsFile = "project-commands.json"
	queuesFile          = "queues.json"
	portsFile           = "ports.json"
	modelsFile          = "models.json"
	maxStoredProjects   = 1000
	maxStoredSessions   = 1000
	maxStoredRoles      = 4000
)

type ProjectCommands struct {
	Dir string
	mu  *sync.Mutex
}

type Queues struct {
	Dir string
	mu  *sync.Mutex
}

type Ports struct {
	Dir string
	mu  *sync.Mutex
}

type Models struct {
	Dir string
	mu  *sync.Mutex
}

func NewProjectCommands(dir string) ProjectCommands {
	return ProjectCommands{Dir: dir, mu: &sync.Mutex{}}
}

func NewQueues(dir string) Queues { return Queues{Dir: dir, mu: &sync.Mutex{}} }

func NewPorts(dir string) Ports { return Ports{Dir: dir, mu: &sync.Mutex{}} }

func NewModels(dir string) Models { return Models{Dir: dir, mu: &sync.Mutex{}} }

var (
	_ session.ProjectCommandStore = ProjectCommands{}
	_ session.QueueStore          = Queues{}
	_ session.PortStore           = Ports{}
	_ session.ModelStore          = Models{}
)

func locked(mu *sync.Mutex) func() {
	if mu == nil {
		return func() {}
	}
	mu.Lock()
	return mu.Unlock
}

type projectCommandJSON struct {
	Project string `json:"project"`
	URL     string `json:"url"`
	Check   string `json:"check,omitempty"`
	Setup   string `json:"setup,omitempty"`
	Image   string `json:"image,omitempty"`
	Browser string `json:"browser,omitempty"`
	Owner   string `json:"owner,omitempty"`
}

type projectCommandsJSON struct {
	Version  int                  `json:"version"`
	Projects []projectCommandJSON `json:"projects"`
}

func (s ProjectCommands) Load() (map[session.ProjectID]session.ProjectCommands, error) {
	defer locked(s.mu)()
	data, err := read(s.Dir, projectCommandsFile)
	out := map[session.ProjectID]session.ProjectCommands{}
	if err != nil || data == nil {
		return out, err
	}
	var f projectCommandsJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 {
		return out, errors.New("project-commands.json isn't a record of project commands this engine reads")
	}
	for _, p := range f.Projects {
		check, okCheck := session.CheckProjectCommand(p.Check)
		setup, okSetup := session.CheckProjectCommand(p.Setup)
		image, okImage := session.CheckVerifyImage(p.Image)
		browser, okBrowser := session.CheckProjectCommand(p.Browser)
		id := session.ProjectID{Name: p.Project, URL: p.URL}
		if len(p.Project) > 400 || !validProject(id) || !okCheck || !okSetup || !okImage || !okBrowser || len(out) >= maxStoredProjects {
			continue
		}
		out[id] = session.ProjectCommands{Check: check, Setup: setup, Image: image, Browser: browser}
	}
	return out, nil
}

func (s ProjectCommands) Save(all map[session.ProjectID]session.ProjectCommands) error {
	owner := ownerIn(s.Dir)
	defer locked(s.mu)()
	f := projectCommandsJSON{Version: 1, Projects: []projectCommandJSON{}}
	for id, c := range all {
		if len(f.Projects) < maxStoredProjects {
			f.Projects = append(f.Projects, projectCommandJSON{Owner: owner, Project: id.Name, URL: id.URL, Check: c.Check, Setup: c.Setup, Image: c.Image, Browser: c.Browser})
		}
	}
	sort.Slice(f.Projects, func(i, j int) bool {
		a, b := f.Projects[i], f.Projects[j]
		if a.Project != b.Project {
			return a.Project < b.Project
		}
		return a.URL < b.URL
	})
	return write(s.Dir, projectCommandsFile, f)
}

type queuedJSON struct {
	ID   string    `json:"id"`
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

type queueJSON struct {
	SID     string       `json:"sid"`
	Items   []queuedJSON `json:"items"`
	Stopped string       `json:"stopped,omitempty"`
}

type queuesJSON struct {
	Version  int         `json:"version"`
	Sessions []queueJSON `json:"sessions"`
}

func (s Queues) Load() (map[string]session.QueueState, error) {
	defer locked(s.mu)()
	data, err := read(s.Dir, queuesFile)
	out := map[string]session.QueueState{}
	if err != nil || data == nil {
		return out, err
	}
	var f queuesJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 {
		return out, errors.New("queues.json isn't a record of queued messages this engine reads")
	}
	for _, q := range f.Sessions {
		if !sessionID.MatchString(q.SID) || len(out) >= maxStoredSessions {
			continue
		}
		st := session.QueueState{Stopped: q.Stopped}
		for _, m := range q.Items {
			if len(st.Items) < session.MaxQueue {
				st.Items = append(st.Items, session.QueuedMessage{ID: m.ID, Text: m.Text, At: m.At})
			}
		}
		out[q.SID] = st
	}
	return out, nil
}

func (s Queues) Save(all map[string]session.QueueState) error {
	defer locked(s.mu)()
	f := queuesJSON{Version: 1, Sessions: []queueJSON{}}
	for sid, st := range all {
		if !sessionID.MatchString(sid) || len(st.Items) == 0 || len(f.Sessions) >= maxStoredSessions {
			continue
		}
		q := queueJSON{SID: sid, Stopped: st.Stopped, Items: []queuedJSON{}}
		for _, m := range st.Items[:min(len(st.Items), session.MaxQueue)] {
			q.Items = append(q.Items, queuedJSON{ID: m.ID, Text: m.Text, At: m.At.UTC()})
		}
		f.Sessions = append(f.Sessions, q)
	}
	sort.Slice(f.Sessions, func(i, j int) bool { return f.Sessions[i].SID < f.Sessions[j].SID })
	return write(s.Dir, queuesFile, f)
}

type portJSON struct {
	SID  string `json:"sid"`
	Base int    `json:"base"`
}

type portsJSON struct {
	Version  int        `json:"version"`
	Sessions []portJSON `json:"sessions"`
}

func (s Ports) Load() (map[string]int, error) {
	defer locked(s.mu)()
	data, err := read(s.Dir, portsFile)
	out := map[string]int{}
	if err != nil || data == nil {
		return out, err
	}
	var f portsJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 {
		return out, errors.New("ports.json isn't a record of port ranges this engine reads")
	}
	for _, p := range f.Sessions {
		if sessionID.MatchString(p.SID) && session.ValidPortBase(p.Base) && len(out) < maxStoredSessions {
			out[p.SID] = p.Base
		}
	}
	return out, nil
}

func (s Ports) Save(all map[string]int) error {
	defer locked(s.mu)()
	f := portsJSON{Version: 1, Sessions: []portJSON{}}
	for sid, base := range all {
		if sessionID.MatchString(sid) && session.ValidPortBase(base) && len(f.Sessions) < maxStoredSessions {
			f.Sessions = append(f.Sessions, portJSON{SID: sid, Base: base})
		}
	}
	sort.Slice(f.Sessions, func(i, j int) bool { return f.Sessions[i].SID < f.Sessions[j].SID })
	return write(s.Dir, portsFile, f)
}

type sessionModelJSON struct {
	SID         string `json:"sid"`
	Agent       string `json:"agent"`
	StartModel  string `json:"startModel,omitempty"`
	StartEffort string `json:"startEffort,omitempty"`
	Model       string `json:"model,omitempty"`
	Effort      string `json:"effort,omitempty"`
}

type roleModelJSON struct {
	Project string `json:"project"`
	URL     string `json:"url"`
	Role    string `json:"role"`
	Model   string `json:"model,omitempty"`
	Effort  string `json:"effort,omitempty"`
	Owner   string `json:"owner,omitempty"`
}

type modelsJSON struct {
	Version  int                `json:"version"`
	Sessions []sessionModelJSON `json:"sessions"`
	Roles    []roleModelJSON    `json:"roles"`
}

func (s Models) Load() (session.ModelState, error) {
	defer locked(s.mu)()
	out := session.ModelState{Sessions: map[string]session.SessionModel{}}
	data, err := read(s.Dir, modelsFile)
	if err != nil || data == nil {
		return out, err
	}
	var f modelsJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 {
		return out, errors.New("models.json isn't a record of model choices this engine reads")
	}
	for _, m := range f.Sessions {
		if sessionID.MatchString(m.SID) && len(out.Sessions) < maxStoredSessions {
			out.Sessions[m.SID] = session.SessionModel{Agent: m.Agent, StartModel: m.StartModel, StartEffort: m.StartEffort, Model: m.Model, Effort: m.Effort}
		}
	}
	for _, r := range f.Roles {
		if slices.Contains(review.Roles, review.Role(r.Role)) && len(out.Roles) < maxStoredRoles {
			out.Roles = append(out.Roles, session.RoleModel{Project: r.Project, URL: r.URL, Role: review.Role(r.Role), Model: r.Model, Effort: r.Effort})
		}
	}
	return out, nil
}

func (s Models) Save(st session.ModelState) error {
	owner := ownerIn(s.Dir)
	defer locked(s.mu)()
	f := modelsJSON{Version: 1, Sessions: []sessionModelJSON{}, Roles: []roleModelJSON{}}
	for sid, m := range st.Sessions {
		if sessionID.MatchString(sid) && len(f.Sessions) < maxStoredSessions {
			f.Sessions = append(f.Sessions, sessionModelJSON{SID: sid, Agent: m.Agent, StartModel: m.StartModel, StartEffort: m.StartEffort, Model: m.Model, Effort: m.Effort})
		}
	}
	for _, r := range st.Roles {
		if len(f.Roles) < maxStoredRoles {
			f.Roles = append(f.Roles, roleModelJSON{Owner: owner, Project: r.Project, URL: r.URL, Role: string(r.Role), Model: r.Model, Effort: r.Effort})
		}
	}
	sort.Slice(f.Sessions, func(i, j int) bool { return f.Sessions[i].SID < f.Sessions[j].SID })
	sort.Slice(f.Roles, func(i, j int) bool {
		a, b := f.Roles[i], f.Roles[j]
		if a.Project != b.Project {
			return a.Project < b.Project
		}
		if a.URL != b.URL {
			return a.URL < b.URL
		}
		return a.Role < b.Role
	})
	return write(s.Dir, modelsFile, f)
}
