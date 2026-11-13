package state

import (
	"encoding/json"
	"errors"
	"sync"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const identityFile = "identity.json"

type Identity struct {
	Dir string
	mu  *sync.Mutex
}

func NewIdentity(dir string) Identity { return Identity{Dir: dir, mu: &sync.Mutex{}} }

var _ session.IdentityStore = Identity{}

type identityFileJSON struct {
	Version int    `json:"version"`
	Name    string `json:"name"`
	Email   string `json:"email"`
}

func (s Identity) lock() func() {
	if s.mu == nil {
		return func() {}
	}
	s.mu.Lock()
	return s.mu.Unlock
}

func (s Identity) Load() (session.CommitIdentity, error) {
	defer s.lock()()
	data, err := read(s.Dir, identityFile)
	if err != nil || data == nil {
		return session.CommitIdentity{}, err
	}
	var f identityFileJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 {
		return session.CommitIdentity{}, errors.New("identity.json isn't a commit identity this engine reads")
	}
	id, err := session.CheckIdentity(f.Name, f.Email)
	if err != nil {
		return session.CommitIdentity{}, errors.New("identity.json holds a name or email this engine refuses")
	}
	return id, nil
}

func (s Identity) Save(id session.CommitIdentity) error {
	defer s.lock()()
	return write(s.Dir, identityFile, identityFileJSON{Version: 1, Name: id.Name, Email: id.Email})
}
