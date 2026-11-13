package state

import (
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	approvalsFile = "approvals.json"
	signInFile    = "signin.json"
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
		out = append(out, session.Approval{Repo: a.Repo, Agent: a.Agent, Hash: a.Hash, ApprovedAt: a.ApprovedAt})
	}
	return out, nil
}

func (s Approvals) Save(list []session.Approval) error {
	defer s.lock()()
	f := approvalsJSON{Version: 1, Approvals: make([]approvalJSON, 0, len(list))}
	for _, a := range list {
		f.Approvals = append(f.Approvals, approvalJSON{Repo: a.Repo, Agent: a.Agent, Hash: a.Hash, ApprovedAt: a.ApprovedAt.UTC()})
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
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 {
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
	defer s.lock()()
	f := expectationsJSON{Version: 1, Expect: map[string]string{}}
	for k, v := range all {
		f.Expect[k] = string(v)
	}
	return write(s.Dir, signInFile, f)
}
