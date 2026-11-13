package page

import (
	"encoding/json"
	"sync"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	MaxStreamQueue = 16 << 20
)

type sessionJSON struct {
	SID          string      `json:"sid"`
	Cwd          string      `json:"cwd"`
	StartedAt    int64       `json:"startedAt"`
	Name         string      `json:"name"`
	ID           string      `json:"id"`
	Kind         string      `json:"kind"`
	Agent        string      `json:"agent,omitempty"`
	Project      string      `json:"project,omitempty"`
	Verified     bool        `json:"verified"`
	Running      bool        `json:"running"`
	Live         bool        `json:"live"`
	Disputed     bool        `json:"disputed"`
	Claims       []string    `json:"claims,omitempty"`
	Outdated     bool        `json:"outdated"`
	Controlled   bool        `json:"controlled"`
	Mode         string      `json:"mode"`
	Busy         bool        `json:"busy"`
	Login        bool        `json:"login"`
	Needs        int         `json:"needs"`
	Checkout     string      `json:"checkout"`
	Epoch        string      `json:"epoch"`
	LastSeq      int64       `json:"lastSeq"`
	Level        string      `json:"level"`
	LevelReason  string      `json:"levelReason"`
	LevelDropped bool        `json:"levelDropped"`
	Review       *reviewJSON `json:"review,omitempty"`
	Push         *pushJSON   `json:"push,omitempty"`
	Forge        []forgeJSON `json:"forge,omitempty"`
	SignIn       *signInJSON `json:"signIn,omitempty"`
	State        string      `json:"state,omitempty"`
	Status       string      `json:"status,omitempty"`
	WaitingFor   string      `json:"waitingFor,omitempty"`
	Queued       int         `json:"queued,omitempty"`
}

type roleJSON struct {
	Role        string  `json:"role"`
	Name        string  `json:"name"`
	Agent       string  `json:"agent"`
	State       string  `json:"state"`
	Verdict     string  `json:"verdict"`
	Message     string  `json:"message"`
	Reason      string  `json:"reason"`
	CostUSD     float64 `json:"costUsd,omitempty"`
	Why         string  `json:"why,omitempty"`
	Round       int     `json:"round"`
	Limit       int     `json:"limit"`
	Since       string  `json:"since"`
	Incremental bool    `json:"incremental"`
	Previous    bool    `json:"previous"`
	Override    string  `json:"override"`

	PluginCommit string   `json:"pluginCommit,omitempty"`
	ClaudeCode   string   `json:"claudeCode,omitempty"`
	Models       []string `json:"models,omitempty"`
}

type reviewJSON struct {
	Commit         string        `json:"commit"`
	Base           string        `json:"base"`
	Branch         string        `json:"branch"`
	Running        bool          `json:"running"`
	Note           string        `json:"note"`
	Roles          []roleJSON    `json:"roles"`
	SignIn         *signInJSON   `json:"signIn,omitempty"`
	CostUSD        float64       `json:"costUsd,omitempty"`
	SessionCostUSD float64       `json:"sessionCostUsd,omitempty"`
	TestList       *testListJSON `json:"testList,omitempty"`
}

func reviewOf(v *session.ReviewView) *reviewJSON {
	if v == nil {
		return nil
	}
	out := &reviewJSON{Commit: v.Commit, Base: v.Base, Branch: v.Branch, Running: v.Running, Note: v.Note, Roles: make([]roleJSON, 0, len(v.Roles)),
		SignIn: signInOf(v.SignIn), CostUSD: v.CostUSD, SessionCostUSD: v.SessionCostUSD, TestList: testListOf(v.TestList)}
	for _, r := range v.Roles {
		out.Roles = append(out.Roles, roleJSON{Role: string(r.Role), Name: r.Name, Agent: r.Agent, State: string(r.State), Verdict: r.Verdict, Message: r.Message, Reason: r.Reason, CostUSD: r.CostUSD, Why: r.Why,
			Round: r.Round, Limit: r.Limit, Since: r.Since, Incremental: r.Since != "", Previous: r.Previous, Override: r.Override,
			PluginCommit: r.PluginCommit, ClaudeCode: r.ClaudeCode, Models: r.Models})
	}
	return out
}

type sessionsFrame struct {
	Type       string        `json:"type"`
	Stream     string        `json:"stream,omitempty"`
	Sessions   []sessionJSON `json:"sessions"`
	Supervisor string        `json:"supervisor"`
}

func sessionsJSON(list []session.Info) []sessionJSON {
	out := make([]sessionJSON, 0, len(list))
	for _, s := range list {
		var started int64
		if !s.StartedAt.IsZero() {
			started = s.StartedAt.UnixMilli()
		}
		out = append(out, sessionJSON{
			SID: s.SID, Cwd: s.Cwd, StartedAt: started, Name: s.Name, ID: s.ID, Kind: s.Kind, Agent: s.Agent, Project: s.Project,
			Verified: s.Verified, Running: s.Running, Live: s.Live, Disputed: s.Disputed, Claims: s.Claims,
			Outdated: s.Outdated, Controlled: s.Controlled, Mode: s.Mode, Busy: s.Busy, Login: s.Login,
			Needs: s.Needs, Epoch: s.Epoch, LastSeq: s.LastSeq,
			Level: string(s.Level), LevelReason: s.LevelReason, LevelDropped: s.LevelDropped,
			Review: reviewOf(s.Review),
			Push:   pushOf(s.Push),
			Forge:  forgeOf(s.Forge),
			SignIn: signInOf(s.SignIn),
			State:  s.State, Status: s.Status, WaitingFor: s.WaitingFor, Queued: s.Queued,
		})
	}
	return out
}

type stream struct {
	mu     sync.Mutex
	queue  [][]byte
	bytes  int
	max    int
	closed bool
	notify chan struct{}
}

func newStream(max int) *stream {
	return &stream{max: max, notify: make(chan struct{}, 1)}
}

func (s *stream) push(v any) bool {
	line, err := json.Marshal(v)
	if err != nil {
		return true
	}
	line = append(line, '\n')
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	if s.bytes+len(line) > s.max {
		s.closed = true
		s.signal()
		return false
	}
	s.queue = append(s.queue, line)
	s.bytes += len(line)
	s.signal()
	return true
}

func (s *stream) signal() {
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

func (s *stream) Hello(id string, list []session.Info) bool {
	return s.push(sessionsFrame{Type: "hello", Stream: id, Sessions: sessionsJSON(list)})
}

func (s *stream) Sessions(list []session.Info) bool {
	return s.push(sessionsFrame{Type: "sessions", Sessions: sessionsJSON(list)})
}

func (s *stream) Frame(frame session.Fields) bool {
	return s.push(frame)
}

func (s *stream) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.signal()
}

func (s *stream) take() ([][]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.queue
	s.queue = nil
	s.bytes = 0
	return out, s.closed
}
