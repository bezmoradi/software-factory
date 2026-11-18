package state

import (
	"encoding/json"
	"errors"
	"regexp"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const workFile = "work.json"

var (
	teamSHA  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	repoName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
	typeName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
)

type Work struct {
	Dir string
	mu  *sync.Mutex
}

func NewWork(dir string) Work { return Work{Dir: dir, mu: &sync.Mutex{}} }

var _ session.WorkStore = Work{}

type teamJSON struct {
	SHA        string    `json:"sha"`
	ApprovedAt time.Time `json:"approvedAt"`
	Owner      string    `json:"owner,omitempty"`
}

type projectSettingsJSON struct {
	Rounds       int        `json:"rounds,omitempty"`
	Type         string     `json:"type,omitempty"`
	AutoFindings bool       `json:"autoFindings,omitempty"`
	PRComment    bool       `json:"prComment,omitempty"`
	Hold         *holdJSON  `json:"hold,omitempty"`
	Depth        *depthJSON `json:"reviewDepth,omitempty"`
	Owner        string     `json:"owner,omitempty"`
}

type workApprovalJSON struct {
	Hash   string    `json:"hash"`
	Commit string    `json:"commit"`
	At     time.Time `json:"at"`
	Text   string    `json:"text,omitempty"`
}

type sessionWorkJSON struct {
	Repo         string              `json:"repo"`
	Claude       bool                `json:"claude"`
	Type         string              `json:"type"`
	Chosen       bool                `json:"chosen"`
	Requirements *workApprovalJSON   `json:"requirements,omitempty"`
	Plan         *workApprovalJSON   `json:"plan,omitempty"`
	Allowed      map[string][]string `json:"allowed,omitempty"`
	Lock         *testLockJSON       `json:"testLock,omitempty"`
	Owner        string              `json:"owner,omitempty"`
}

type testLockJSON struct {
	Commit     string            `json:"commit"`
	At         time.Time         `json:"at"`
	Files      map[string]string `json:"files"`
	Cut        bool              `json:"cut,omitempty"`
	BranchLeft int               `json:"branchLeft,omitempty"`
}

type removedSettingsJSON struct {
	Project string `json:"project"`
	URL     string `json:"url"`
	projectSettingsJSON
}

type removedTeamJSON struct {
	Project string `json:"project"`
	URL     string `json:"url"`
	teamJSON
}

type workJSON struct {
	Version      int                            `json:"version"`
	Teams        map[string]teamJSON            `json:"teams"`
	Projects     map[string]projectSettingsJSON `json:"projects"`
	Removed      []removedSettingsJSON          `json:"removed,omitempty"`
	RemovedTeams []removedTeamJSON              `json:"removedTeams,omitempty"`
	Sessions     map[string]sessionWorkJSON     `json:"sessions,omitempty"`
}

func (s Work) lock() func() {
	if s.mu == nil {
		return func() {}
	}
	s.mu.Lock()
	return s.mu.Unlock
}

func projectSettingsOf(p projectSettingsJSON) session.ProjectSettings {
	v := session.ProjectSettings{Findings: p.AutoFindings, PRComment: p.PRComment}
	if p.Rounds >= 1 && p.Rounds <= 20 {
		v.Rounds = p.Rounds
	}
	if typeName.MatchString(p.Type) {
		v.Type = p.Type
	}
	v.Hold = holdOf(p.Hold)
	v.Depth = depthOf(p.Depth)
	return v
}

func projectSettingsJSONOf(p session.ProjectSettings, owner string) projectSettingsJSON {
	return projectSettingsJSON{Owner: owner, Rounds: p.Rounds, Type: p.Type, AutoFindings: p.Findings, PRComment: p.PRComment, Hold: holdJSONOf(p.Hold), Depth: depthJSONOf(p.Depth)}
}

func (s Work) Load() (session.WorkState, error) {
	defer s.lock()()
	out := session.WorkState{Teams: map[string]session.TeamApproval{}, Projects: map[string]session.ProjectSettings{}, Sessions: map[string]session.SavedWork{}}
	data, err := read(s.Dir, workFile)
	if err != nil || data == nil {
		return out, err
	}
	var f workJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 {
		return out, errors.New("work.json isn't a settings record this engine reads")
	}
	for name, t := range f.Teams {
		if repoName.MatchString(name) && teamSHA.MatchString(t.SHA) {
			out.Teams[name] = session.TeamApproval{SHA: t.SHA, At: t.ApprovedAt}
		}
	}
	for name, p := range f.Projects {
		if repoName.MatchString(name) {
			out.Projects[name] = projectSettingsOf(p)
		}
	}
	for _, r := range f.Removed {
		id := session.ProjectID{Name: r.Project, URL: r.URL}
		if repoName.MatchString(r.Project) && removedID(id) && session.RoomForRemoved(out.Removed, id) {
			out.Removed = append(out.Removed, session.RemovedProject[session.ProjectSettings]{ID: id, Data: projectSettingsOf(r.projectSettingsJSON)})
		}
	}
	for _, r := range f.RemovedTeams {
		id := session.ProjectID{Name: r.Project, URL: r.URL}
		if repoName.MatchString(r.Project) && teamSHA.MatchString(r.SHA) && removedID(id) && session.RoomForRemoved(out.RemovedTeams, id) {
			out.RemovedTeams = append(out.RemovedTeams, session.RemovedProject[session.TeamApproval]{ID: id, Data: session.TeamApproval{SHA: r.SHA, At: r.ApprovedAt}})
		}
	}
	for sid, w := range f.Sessions {
		if v, ok := sessionWorkOf(sid, w); ok && len(out.Sessions) < maxSessions {
			out.Sessions[sid] = v
		}
	}
	return out, nil
}

func (s Work) Save(st session.WorkState) error {
	owner := ownerIn(s.Dir)
	defer s.lock()()
	f := workJSON{Version: 1, Teams: map[string]teamJSON{}, Projects: map[string]projectSettingsJSON{}}
	for name, t := range st.Teams {
		f.Teams[name] = teamJSON{Owner: owner, SHA: t.SHA, ApprovedAt: t.At.UTC()}
	}
	for name, p := range st.Projects {
		f.Projects[name] = projectSettingsJSONOf(p, owner)
	}
	for _, r := range st.Removed {
		if repoName.MatchString(r.ID.Name) && removedID(r.ID) {
			f.Removed = append(f.Removed, removedSettingsJSON{Project: r.ID.Name, URL: r.ID.URL, projectSettingsJSON: projectSettingsJSONOf(r.Data, owner)})
		}
	}
	for _, r := range st.RemovedTeams {
		if repoName.MatchString(r.ID.Name) && removedID(r.ID) {
			f.RemovedTeams = append(f.RemovedTeams, removedTeamJSON{Project: r.ID.Name, URL: r.ID.URL, Owner: owner, SHA: r.Data.SHA, ApprovedAt: r.Data.At.UTC()})
		}
	}
	if len(st.Sessions) > 0 {
		f.Sessions = map[string]sessionWorkJSON{}
		for sid, w := range st.Sessions {
			f.Sessions[sid] = sessionWorkJSON{Owner: owner, Repo: w.Repo, Claude: w.Claude, Type: w.Type, Chosen: w.Chosen,
				Requirements: approvalJSONOf(w.Requirements), Plan: approvalJSONOf(w.Plan), Allowed: w.Allowed, Lock: testLockJSONOf(w.Lock)}
		}
	}
	return write(s.Dir, workFile, f)
}
