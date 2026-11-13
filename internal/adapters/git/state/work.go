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
}

type projectSettingsJSON struct {
	Rounds       int       `json:"rounds,omitempty"`
	Type         string    `json:"type,omitempty"`
	AutoFindings bool      `json:"autoFindings,omitempty"`
	PRComment    bool      `json:"prComment,omitempty"`
	Hold         *holdJSON `json:"hold,omitempty"`
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
}

type workJSON struct {
	Version  int                            `json:"version"`
	Teams    map[string]teamJSON            `json:"teams"`
	Projects map[string]projectSettingsJSON `json:"projects"`
	Sessions map[string]sessionWorkJSON     `json:"sessions,omitempty"`
}

func (s Work) lock() func() {
	if s.mu == nil {
		return func() {}
	}
	s.mu.Lock()
	return s.mu.Unlock
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
		if !repoName.MatchString(name) {
			continue
		}
		v := session.ProjectSettings{Findings: p.AutoFindings, PRComment: p.PRComment}
		if p.Rounds >= 1 && p.Rounds <= 20 {
			v.Rounds = p.Rounds
		}
		if typeName.MatchString(p.Type) {
			v.Type = p.Type
		}
		v.Hold = holdOf(p.Hold)
		out.Projects[name] = v
	}
	for sid, w := range f.Sessions {
		if v, ok := sessionWorkOf(sid, w); ok && len(out.Sessions) < maxSessions {
			out.Sessions[sid] = v
		}
	}
	return out, nil
}

func (s Work) Save(st session.WorkState) error {
	defer s.lock()()
	f := workJSON{Version: 1, Teams: map[string]teamJSON{}, Projects: map[string]projectSettingsJSON{}}
	for name, t := range st.Teams {
		f.Teams[name] = teamJSON{SHA: t.SHA, ApprovedAt: t.At.UTC()}
	}
	for name, p := range st.Projects {
		f.Projects[name] = projectSettingsJSON{Rounds: p.Rounds, Type: p.Type, AutoFindings: p.Findings, PRComment: p.PRComment, Hold: holdJSONOf(p.Hold)}
	}
	if len(st.Sessions) > 0 {
		f.Sessions = map[string]sessionWorkJSON{}
		for sid, w := range st.Sessions {
			f.Sessions[sid] = sessionWorkJSON{Repo: w.Repo, Claude: w.Claude, Type: w.Type, Chosen: w.Chosen,
				Requirements: approvalJSONOf(w.Requirements), Plan: approvalJSONOf(w.Plan), Allowed: w.Allowed}
		}
	}
	return write(s.Dir, workFile, f)
}
