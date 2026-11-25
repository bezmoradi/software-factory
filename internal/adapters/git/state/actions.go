package state

import (
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	actionsFile    = "actions.json"
	actionsVersion = 1
)

type Actions struct {
	Dir string
	mu  *sync.Mutex
}

func NewActions(dir string) Actions { return Actions{Dir: dir, mu: &sync.Mutex{}} }

var _ session.ActionStore = Actions{}

type slotJSON struct {
	Name    string `json:"name"`
	Pattern string `json:"pattern"`
	Max     int    `json:"max"`
}

type actionJSON struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Program    string     `json:"program"`
	Args       []string   `json:"args"`
	Credential string     `json:"credential"`
	Target     string     `json:"target"`
	Slots      []slotJSON `json:"slots,omitempty"`
	Files      bool       `json:"files,omitempty"`
	Minutes    int        `json:"minutes"`
	Saved      int64      `json:"saved"`
	Owner      string     `json:"owner,omitempty"`
}

type actionsJSON struct {
	Version int          `json:"version"`
	Actions []actionJSON `json:"actions"`
}

var errActionsFile = errors.New("actions.json isn't a list of actions this engine reads")

func (s Actions) lock() func() {
	if s.mu == nil {
		return func() {}
	}
	s.mu.Lock()
	return s.mu.Unlock
}

func actionOf(j actionJSON) (session.Action, bool) {
	a := session.Action{ID: j.ID, Name: j.Name, Program: j.Program, Args: j.Args, Credential: j.Credential, Target: j.Target, Files: j.Files, Minutes: j.Minutes, Saved: time.Unix(j.Saved, 0)}
	if a.Args == nil {
		a.Args = []string{}
	}
	for _, s := range j.Slots {
		a.Slots = append(a.Slots, session.ActionSlot{Name: s.Name, Pattern: s.Pattern, Max: s.Max})
	}
	if !grantID.MatchString(j.ID) || !grantID.MatchString(j.Credential) || j.Saved <= 0 || !session.SavedActionShape(a) {
		return session.Action{}, false
	}
	return a, true
}

func (s Actions) Load() ([]session.Action, error) {
	defer s.lock()()
	data, err := read(s.Dir, actionsFile)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return []session.Action{}, nil
	}
	var f actionsJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version < 1 || f.Version > actionsVersion || len(f.Actions) > session.MaxActions {
		return nil, errActionsFile
	}
	out := make([]session.Action, 0, len(f.Actions))
	for _, j := range f.Actions {
		a, ok := actionOf(j)
		if !ok {
			return nil, errActionsFile
		}
		out = append(out, a)
	}
	return out, nil
}

func (s Actions) Save(list []session.Action) error {
	owner := ownerIn(s.Dir)
	defer s.lock()()
	f := actionsJSON{Version: actionsVersion, Actions: make([]actionJSON, 0, len(list))}
	for _, a := range list {
		j := actionJSON{ID: a.ID, Name: a.Name, Program: a.Program, Args: a.Args, Credential: a.Credential, Target: a.Target, Files: a.Files, Minutes: a.Minutes, Saved: a.Saved.Unix(), Owner: owner}
		if j.Args == nil {
			j.Args = []string{}
		}
		for _, sl := range a.Slots {
			j.Slots = append(j.Slots, slotJSON{Name: sl.Name, Pattern: sl.Pattern, Max: sl.Max})
		}
		f.Actions = append(f.Actions, j)
	}
	return write(s.Dir, actionsFile, f)
}
