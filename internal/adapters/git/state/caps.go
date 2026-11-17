package state

import (
	"encoding/json"
	"errors"
	"maps"
	"sort"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const capsFile = "caps.json"

type Caps struct {
	Dir string
	mu  *sync.Mutex
}

func NewCaps(dir string) Caps { return Caps{Dir: dir, mu: &sync.Mutex{}} }

var _ session.CapsStore = Caps{}

type capsJSON struct {
	MoneyUSD float64 `json:"money"`
	Minutes  int     `json:"minutes"`
	Turns    int     `json:"turns"`
	Owner    string  `json:"owner,omitempty"`
}

type capsUsedJSON struct {
	CostUSD float64 `json:"costUsd"`
	Seconds int64   `json:"seconds"`
	Turns   int     `json:"turns"`
}

type capsSessionJSON struct {
	SID       string       `json:"sid"`
	Project   string       `json:"project"`
	Own       *capsJSON    `json:"own,omitempty"`
	Used      capsUsedJSON `json:"used"`
	Reached   string       `json:"reached,omitempty"`
	ReachedAt time.Time    `json:"reachedAt,omitzero"`
}

type removedCapsJSON struct {
	Project string `json:"project"`
	URL     string `json:"url"`
	capsJSON
}

type capsFileJSON struct {
	Version  int                  `json:"version"`
	Projects map[string]capsJSON  `json:"projects"`
	Removed  []removedCapsJSON    `json:"removed,omitempty"`
	Sessions []capsSessionJSON    `json:"sessions"`
	Tokens   map[string]time.Time `json:"tokens"`
}

func (s Caps) lock() func() {
	if s.mu == nil {
		return func() {}
	}
	s.mu.Lock()
	return s.mu.Unlock
}

func capsOf(c capsJSON) session.Caps {
	return session.Caps{MoneyUSD: c.MoneyUSD, Minutes: c.Minutes, Turns: c.Turns}
}

func (s Caps) Load() (session.CapsState, error) {
	defer s.lock()()
	data, err := read(s.Dir, capsFile)
	if err != nil || data == nil {
		return session.CapsState{}, err
	}
	var f capsFileJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 {
		return session.CapsState{}, errors.New("caps.json isn't a record of caps this engine reads")
	}
	out := session.CapsState{Projects: map[string]session.Caps{}, Tokens: map[string]time.Time{}}
	for name, c := range f.Projects {
		if len(out.Projects) < session.MaxCapProjects {
			out.Projects[name] = capsOf(c)
		}
	}
	for _, r := range f.Removed {
		id := session.ProjectID{Name: r.Project, URL: r.URL}
		if removedID(id) && session.RoomForRemoved(out.Removed, id) {
			out.Removed = append(out.Removed, session.RemovedProject[session.Caps]{ID: id, Data: capsOf(r.capsJSON)})
		}
	}
	for _, x := range f.Sessions {
		if !sessionID.MatchString(x.SID) || len(out.Sessions) >= session.MaxCapSessions {
			continue
		}
		cs := session.CapsSession{SID: x.SID, Project: x.Project, Reached: x.Reached, ReachedAt: x.ReachedAt,
			Used: session.CapsUsed{CostUSD: x.Used.CostUSD, Time: time.Duration(x.Used.Seconds) * time.Second, Turns: x.Used.Turns}}
		if x.Own != nil {
			own := capsOf(*x.Own)
			cs.Own = &own
		}
		out.Sessions = append(out.Sessions, cs)
	}
	maps.Copy(out.Tokens, f.Tokens)
	return out, nil
}

func (s Caps) Save(st session.CapsState) error {
	owner := ownerIn(s.Dir)
	defer s.lock()()
	f := capsFileJSON{Version: 1, Projects: map[string]capsJSON{}, Sessions: []capsSessionJSON{}, Tokens: map[string]time.Time{}}
	for name, c := range st.Projects {
		if len(f.Projects) < session.MaxCapProjects {
			f.Projects[name] = capsJSON{Owner: owner, MoneyUSD: c.MoneyUSD, Minutes: c.Minutes, Turns: c.Turns}
		}
	}
	for _, r := range st.Removed {
		if removedID(r.ID) {
			f.Removed = append(f.Removed, removedCapsJSON{Project: r.ID.Name, URL: r.ID.URL, Owner: owner, MoneyUSD: r.Data.MoneyUSD, Minutes: r.Data.Minutes, Turns: r.Data.Turns})
		}
	}
	for _, x := range st.Sessions {
		if !sessionID.MatchString(x.SID) || len(f.Sessions) >= session.MaxCapSessions {
			continue
		}
		cs := capsSessionJSON{SID: x.SID, Project: x.Project, Reached: x.Reached, ReachedAt: x.ReachedAt.UTC(),
			Used: capsUsedJSON{CostUSD: x.Used.CostUSD, Seconds: int64(x.Used.Time / time.Second), Turns: x.Used.Turns}}
		if x.ReachedAt.IsZero() {
			cs.ReachedAt = time.Time{}
		}
		if x.Own != nil {
			cs.Own = &capsJSON{Owner: owner, MoneyUSD: x.Own.MoneyUSD, Minutes: x.Own.Minutes, Turns: x.Own.Turns}
		}
		f.Sessions = append(f.Sessions, cs)
	}
	sort.Slice(f.Sessions, func(i, j int) bool { return f.Sessions[i].SID < f.Sessions[j].SID })
	for host, at := range st.Tokens {
		f.Tokens[host] = at.UTC()
	}
	return write(s.Dir, capsFile, f)
}
