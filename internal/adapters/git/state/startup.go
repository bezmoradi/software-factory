package state

import (
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	approvalsFile        = "approvals.json"
	signInFile           = "signin.json"
	maxAgentExpectations = 100
)

type Approvals struct {
	Dir string
	mu  *sync.Mutex
}

func NewApprovals(dir string) Approvals { return Approvals{Dir: dir, mu: &sync.Mutex{}} }

var _ session.Approvals = Approvals{}

type approvalJSON struct {
	Repo       string    `json:"repo"`
	Agent      string    `json:"agent"`
	Hash       string    `json:"hash"`
	ApprovedAt time.Time `json:"approvedAt"`
	Seen       []string  `json:"seen,omitempty"`
	Owner      string    `json:"owner,omitempty"`
}

type approvalsJSON struct {
	Version   int            `json:"version"`
	Approvals []approvalJSON `json:"approvals"`
}

func (s Approvals) lock() func() {
	if s.mu == nil {
		return func() {}
	}
	s.mu.Lock()
	return s.mu.Unlock
}

func (s Approvals) Load() ([]session.Approval, error) {
	defer s.lock()()
	data, err := read(s.Dir, approvalsFile)
	if err != nil || data == nil {
		return []session.Approval{}, err
	}
	var f approvalsJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 {
		return nil, errors.New("approvals.json isn't an approval record this engine reads")
	}
	out := make([]session.Approval, 0, len(f.Approvals))
	for _, a := range f.Approvals {
		if !session.ValidApproval(a.Hash) || a.Hash == "" {
			continue
		}
		seen := slices.DeleteFunc(a.Seen, func(h string) bool { return h == "" || !session.ValidApproval(h) })
		if len(seen) > session.MaxInstructionFiles {
			seen = seen[:session.MaxInstructionFiles]
		}
		out = append(out, session.Approval{Repo: a.Repo, Agent: a.Agent, Hash: a.Hash, ApprovedAt: a.ApprovedAt, Seen: seen})
	}
	return out, nil
}

func (s Approvals) Save(list []session.Approval) error {
	owner := ownerIn(s.Dir)
	defer s.lock()()
	f := approvalsJSON{Version: 1, Approvals: make([]approvalJSON, 0, len(list))}
	for _, a := range list {
		f.Approvals = append(f.Approvals, approvalJSON{Owner: owner, Repo: a.Repo, Agent: a.Agent, Hash: a.Hash, ApprovedAt: a.ApprovedAt.UTC(), Seen: a.Seen})
	}
	return write(s.Dir, approvalsFile, f)
}

type Expectations struct {
	Dir string
	mu  *sync.Mutex
}

func NewExpectations(dir string) Expectations { return Expectations{Dir: dir, mu: &sync.Mutex{}} }

var _ session.Expectations = Expectations{}

type expectationsJSON struct {
	Version int               `json:"version"`
	Expect  map[string]string `json:"expect"`
	Owner   string            `json:"owner,omitempty"`
}

func (s Expectations) lock() func() {
	if s.mu == nil {
		return func() {}
	}
	s.mu.Lock()
	return s.mu.Unlock
}

func (s Expectations) Load() (map[string]session.Expect, error) {
	defer s.lock()()
	data, err := read(s.Dir, signInFile)
	if err != nil || data == nil {
		return map[string]session.Expect{}, err
	}
	var f expectationsJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 || len(f.Expect) > session.MaxBoxRecords+maxAgentExpectations {
		return nil, errors.New("signin.json isn't a sign-in record this engine reads")
	}
	out := map[string]session.Expect{}
	for k, v := range f.Expect {
		if x := session.Expect(v); session.ValidExpect(x) {
			out[k] = x
		}
	}
	return out, nil
}

func (s Expectations) Save(all map[string]session.Expect) error {
	owner := ownerIn(s.Dir)
	defer s.lock()()
	f := expectationsJSON{Owner: owner, Version: 1, Expect: map[string]string{}}
	for k, v := range all {
		f.Expect[k] = string(v)
	}
	return write(s.Dir, signInFile, f)
}
