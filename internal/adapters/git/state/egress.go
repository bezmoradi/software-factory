package state

import (
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const egressFile = "egress.json"

type Egress struct {
	Dir string
	mu  *sync.Mutex
}

func NewEgress(dir string) Egress { return Egress{Dir: dir, mu: &sync.Mutex{}} }

var _ session.EgressStore = Egress{}

type addedHostJSON struct {
	Name    string `json:"name"`
	Expires int64  `json:"expires,omitempty"`
	AddedAt int64  `json:"addedAt,omitempty"`
}

type gatewayJSON struct {
	URL       string `json:"url"`
	Agents    bool   `json:"agents"`
	Reviewers bool   `json:"reviewers"`
}

type egressJSON struct {
	Version int             `json:"version"`
	Agents  []string        `json:"agents"`
	Added   []addedHostJSON `json:"added"`
	Gateway *gatewayJSON    `json:"gateway,omitempty"`
}

func (s Egress) lock() func() {
	if s.mu == nil {
		return func() {}
	}
	s.mu.Lock()
	return s.mu.Unlock
}

var errEgressFile = errors.New("egress.json isn't an allowlist this engine reads")

func (s Egress) Load() (session.EgressSettings, bool, error) {
	defer s.lock()()
	data, err := read(s.Dir, egressFile)
	if err != nil {
		return session.EgressSettings{}, false, err
	}
	if data == nil {
		return session.EgressSettings{}, false, nil
	}
	var f egressJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 || len(f.Agents) > 8 || len(f.Added) > session.MaxAddedHosts {
		return session.EgressSettings{}, false, errEgressFile
	}
	var out session.EgressSettings
	known := map[string]bool{}
	for _, a := range session.AgentHostTable() {
		known[a.ID] = true
	}
	for _, id := range f.Agents {
		if !known[id] {
			return session.EgressSettings{}, false, errEgressFile
		}
		out.Agents = append(out.Agents, id)
	}
	for _, h := range f.Added {
		if session.HostName(h.Name) != h.Name || !session.ValidAllowEntry(h.Name) || h.Expires < 0 || h.AddedAt < 0 {
			return session.EgressSettings{}, false, errEgressFile
		}
		a := session.AddedHost{Name: h.Name}
		if h.AddedAt > 0 {
			a.AddedAt = time.Unix(h.AddedAt, 0)
		}
		if h.Expires > 0 {
			a.Expires = time.Unix(h.Expires, 0)
		}
		out.Added = append(out.Added, a)
	}
	if f.Gateway != nil {
		u, host, err := session.CheckGatewayURL(f.Gateway.URL)
		if err != nil {
			return session.EgressSettings{}, false, errEgressFile
		}
		out.Gateway = session.Gateway{URL: u, Host: host, Agents: f.Gateway.Agents, Reviewers: f.Gateway.Reviewers}
	}
	return out, true, nil
}

func (s Egress) Save(e session.EgressSettings) error {
	defer s.lock()()
	f := egressJSON{Version: 1, Agents: e.Agents, Added: make([]addedHostJSON, 0, len(e.Added))}
	if f.Agents == nil {
		f.Agents = []string{}
	}
	for _, h := range e.Added {
		j := addedHostJSON{Name: h.Name}
		if !h.AddedAt.IsZero() {
			j.AddedAt = h.AddedAt.Unix()
		}
		if !h.Expires.IsZero() {
			j.Expires = h.Expires.Unix()
		}
		f.Added = append(f.Added, j)
	}
	if e.Gateway.URL != "" {
		f.Gateway = &gatewayJSON{URL: e.Gateway.URL, Agents: e.Gateway.Agents, Reviewers: e.Gateway.Reviewers}
	}
	return write(s.Dir, egressFile, f)
}
