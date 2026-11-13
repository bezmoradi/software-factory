package state

import (
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/review"
	"github.com/bezmoradi/software-factory/internal/core/session"
)

const reviewerAgentsFile = "reviewer-agents.json"

type ReviewerAgents struct {
	Dir string
	mu  *sync.Mutex
}

func NewReviewerAgents(dir string) ReviewerAgents { return ReviewerAgents{Dir: dir, mu: &sync.Mutex{}} }

var _ session.AgentFileStore = ReviewerAgents{}

type agentFileJSON struct {
	Project string    `json:"project"`
	URL     string    `json:"url"`
	Text    []byte    `json:"text"`
	At      time.Time `json:"at"`
}

type agentRoleJSON struct {
	Project string `json:"project"`
	URL     string `json:"url"`
	Role    string `json:"role"`
	Name    string `json:"name"`
}

type reviewerAgentsJSON struct {
	Version int             `json:"version"`
	Files   []agentFileJSON `json:"files"`
	Roles   []agentRoleJSON `json:"roles"`
}

func (s ReviewerAgents) Load() (session.AgentFileState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := read(s.Dir, reviewerAgentsFile)
	if err != nil || data == nil {
		return session.AgentFileState{}, err
	}
	var f reviewerAgentsJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 {
		return session.AgentFileState{}, errors.New(reviewerAgentsFile + " isn't a reviewer agents record this engine reads")
	}
	var out session.AgentFileState
	for _, a := range f.Files {
		if a.Project == "" || len(a.Text) > session.MaxAgentFile || len(out.Files) >= session.MaxAgentFilesAll {
			continue
		}
		out.Files = append(out.Files, session.StoredAgentFile{Project: a.Project, URL: a.URL, Text: string(a.Text), At: a.At})
	}
	for _, r := range f.Roles {
		role := review.Role(r.Role)
		if r.Project == "" || r.Name == "" || !slices.Contains(review.Roles, role) {
			continue
		}
		out.Roles = append(out.Roles, session.AgentFileRole{Project: r.Project, URL: r.URL, Role: role, Name: r.Name})
	}
	return out, nil
}

func (s ReviewerAgents) Save(st session.AgentFileState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := reviewerAgentsJSON{Version: 1, Files: make([]agentFileJSON, 0, len(st.Files)), Roles: make([]agentRoleJSON, 0, len(st.Roles))}
	for _, a := range st.Files {
		f.Files = append(f.Files, agentFileJSON{Project: a.Project, URL: a.URL, Text: []byte(a.Text), At: a.At.UTC()})
	}
	for _, r := range st.Roles {
		f.Roles = append(f.Roles, agentRoleJSON{Project: r.Project, URL: r.URL, Role: string(r.Role), Name: r.Name})
	}
	return write(s.Dir, reviewerAgentsFile, f)
}
