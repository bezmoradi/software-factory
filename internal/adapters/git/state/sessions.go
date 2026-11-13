package state

import (
	"encoding/json"
	"errors"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	sessionsFile = "sessions.json"
	maxSessions  = 1000
	maxAllowed   = 200
	maxVersions  = 100
	maxDisplay   = 80
	maxPath      = 1000
	maxRelPath   = 4096
)

var (
	sessionID   = regexp.MustCompile(`^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$`)
	jobID       = regexp.MustCompile(`^[0-9a-f]{8}$`)
	agentID     = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	worktree    = regexp.MustCompile(`^sf-[0-9a-f]{8}$`)
	branchName  = regexp.MustCompile(`^sf/[0-9a-f]{8}$`)
	commitID    = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	controlChar = regexp.MustCompile(`[\x00-\x1f\x7f]`)
)

type Sessions struct {
	Dir string
	mu  *sync.Mutex
}

func NewSessions(dir string) Sessions { return Sessions{Dir: dir, mu: &sync.Mutex{}} }

var _ session.RunStore = Sessions{}

type runJSON struct {
	SID       string    `json:"sid"`
	ID        string    `json:"id"`
	Agent     string    `json:"agent"`
	Repo      string    `json:"repo"`
	RepoPath  string    `json:"repoPath"`
	Base      string    `json:"base"`
	Name      string    `json:"name"`
	Branch    string    `json:"branch"`
	Cwd       string    `json:"cwd"`
	Display   string    `json:"display,omitempty"`
	StartedAt time.Time `json:"startedAt"`
	Sandboxed bool      `json:"sandboxed,omitempty"`

	InterruptedAt  time.Time `json:"interruptedAt,omitzero"`
	AgentVersion   string    `json:"agentVersion,omitempty"`
	EngineVersion  string    `json:"engineVersion,omitempty"`
	WorkflowCommit string    `json:"workflowCommit,omitempty"`
}

type sessionsJSON struct {
	Version  int       `json:"version"`
	Sessions []runJSON `json:"sessions"`
}

func (s Sessions) lock() func() {
	if s.mu == nil {
		return func() {}
	}
	s.mu.Lock()
	return s.mu.Unlock
}

func cleanAbs(p string) bool {
	return p != "" && len(p) <= maxPath && path.IsAbs(p) && path.Clean(p) == p && utf8.ValidString(p) && !controlChar.MatchString(p)
}

func cleanText(t string, n int) bool {
	return utf8.ValidString(t) && utf8.RuneCountInString(t) <= n && !controlChar.MatchString(t)
}

func runOf(r runJSON) (session.SavedRun, bool) {
	ok := sessionID.MatchString(r.SID) && jobID.MatchString(r.ID) && agentID.MatchString(r.Agent) && repoName.MatchString(r.Repo) &&
		cleanAbs(r.RepoPath) && commitID.MatchString(r.Base) && worktree.MatchString(r.Name) && branchName.MatchString(r.Branch) &&
		cleanAbs(r.Cwd) && cleanText(r.Display, maxDisplay)
	if !ok {
		return session.SavedRun{}, false
	}
	return session.SavedRun{SID: r.SID, ID: r.ID, Agent: r.Agent, Repo: r.Repo, RepoPath: r.RepoPath, Base: r.Base, Name: r.Name,
		Branch: r.Branch, Cwd: r.Cwd, Display: r.Display, StartedAt: r.StartedAt, Sandboxed: r.Sandboxed, InterruptedAt: r.InterruptedAt,
		AgentVersion: matching(r.AgentVersion, versionText), EngineVersion: matching(r.EngineVersion, versionText), WorkflowCommit: matching(r.WorkflowCommit, commitID)}, true
}

func (s Sessions) Load() ([]session.SavedRun, error) {
	defer s.lock()()
	data, err := read(s.Dir, sessionsFile)
	if err != nil || data == nil {
		return nil, err
	}
	var f sessionsJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 {
		return nil, errors.New("sessions.json isn't a sessions record this engine reads")
	}
	var out []session.SavedRun
	seen := map[string]bool{}
	for _, r := range f.Sessions {
		v, ok := runOf(r)
		if !ok || seen[v.SID] || len(out) >= maxSessions {
			continue
		}
		seen[v.SID] = true
		out = append(out, v)
	}
	return out, nil
}

func (s Sessions) Save(runs []session.SavedRun) error {
	defer s.lock()()
	f := sessionsJSON{Version: 1, Sessions: make([]runJSON, 0, len(runs))}
	for _, r := range runs {
		f.Sessions = append(f.Sessions, runJSON{SID: r.SID, ID: r.ID, Agent: r.Agent, Repo: r.Repo, RepoPath: r.RepoPath, Base: r.Base, Name: r.Name,
			Branch: r.Branch, Cwd: r.Cwd, Display: r.Display, StartedAt: r.StartedAt.UTC(), Sandboxed: r.Sandboxed, InterruptedAt: r.InterruptedAt.UTC(),
			AgentVersion: matching(r.AgentVersion, versionText), EngineVersion: matching(r.EngineVersion, versionText), WorkflowCommit: matching(r.WorkflowCommit, commitID)})
	}
	return write(s.Dir, sessionsFile, f)
}

func approvalOf(a *workApprovalJSON) (*session.SavedApproval, bool) {
	if a == nil {
		return nil, true
	}
	if !teamSHA.MatchString(a.Hash) || !commitID.MatchString(a.Commit) {
		return nil, false
	}
	v := &session.SavedApproval{Hash: a.Hash, Commit: a.Commit, At: a.At}
	if utf8.ValidString(a.Text) {
		v.Text = a.Text
	}
	return v, true
}

func approvalJSONOf(a *session.SavedApproval) *workApprovalJSON {
	if a == nil {
		return nil
	}
	return &workApprovalJSON{Hash: a.Hash, Commit: a.Commit, At: a.At.UTC(), Text: a.Text}
}

func relPath(p string) bool {
	return p != "" && len(p) <= maxRelPath && !path.IsAbs(p) && path.Clean(p) == p && p != ".." && !strings.HasPrefix(p, "../") &&
		utf8.ValidString(p) && !controlChar.MatchString(p)
}

func sessionWorkOf(sid string, w sessionWorkJSON) (session.SavedWork, bool) {
	if !sessionID.MatchString(sid) || !repoName.MatchString(w.Repo) || (w.Type != "" && !typeName.MatchString(w.Type)) || len(w.Allowed) > maxAllowed {
		return session.SavedWork{}, false
	}
	req, ok1 := approvalOf(w.Requirements)
	plan, ok2 := approvalOf(w.Plan)
	if !ok1 || !ok2 {
		return session.SavedWork{}, false
	}
	v := session.SavedWork{Repo: w.Repo, Claude: w.Claude, Type: w.Type, Chosen: w.Chosen, Requirements: req, Plan: plan}
	for p, blobs := range w.Allowed {
		if !relPath(p) || len(blobs) == 0 || len(blobs) > maxVersions || slices.ContainsFunc(blobs, func(b string) bool { return !commitID.MatchString(b) }) {
			continue
		}
		if v.Allowed == nil {
			v.Allowed = map[string][]string{}
		}
		v.Allowed[p] = append([]string(nil), blobs...)
	}
	return v, true
}
