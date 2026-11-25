package state

import (
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	grantsFile    = "grants.json"
	grantsVersion = 4
	maxGrantField = 200
)

var grantID = regexp.MustCompile(`^[0-9a-f]{32}$`)

type Grants struct {
	Dir string
	mu  *sync.Mutex
}

func NewGrants(dir string) Grants { return Grants{Dir: dir, mu: &sync.Mutex{}} }

var _ session.GrantStore = Grants{}

type grantJSON struct {
	ID          string   `json:"id"`
	Host        string   `json:"host,omitempty"`
	Port        int      `json:"port,omitempty"`
	Credential  string   `json:"credential,omitempty"`
	Name        string   `json:"name,omitempty"`
	Bound       []string `json:"bound,omitempty"`
	Delivery    string   `json:"delivery,omitempty"`
	Scope       string   `json:"scope"`
	Session     string   `json:"session"`
	Project     string   `json:"project"`
	Expires     int64    `json:"expires,omitempty"`
	Reason      string   `json:"reason,omitempty"`
	AskedBy     string   `json:"askedBy"`
	Preset      string   `json:"preset,omitempty"`
	Made        int64    `json:"made"`
	GitAccepted []string `json:"gitAccepted,omitempty"`
	Owner       string   `json:"owner,omitempty"`
}

type grantsJSON struct {
	Version int         `json:"version"`
	Grants  []grantJSON `json:"grants"`
}

var errGrantsFile = errors.New("grants.json isn't a list of grants this engine reads")

func (s Grants) lock() func() {
	if s.mu == nil {
		return func() {}
	}
	s.mu.Lock()
	return s.mu.Unlock
}

func boundOK(list []string) bool {
	if len(list) == 0 || len(list) > session.MaxCredentialBound {
		return false
	}
	for _, h := range list {
		if session.HostName(h) != h || !session.ValidBoundEntry(h) {
			return false
		}
	}
	return true
}

func gitAcceptedOK(list, hosts []string) bool {
	if len(list) > session.MaxCredentialBound {
		return false
	}
	for i, h := range list {
		if !session.ValidBoundEntry(h) || slices.Contains(list[:i], h) || hosts != nil && !slices.Contains(hosts, h) {
			return false
		}
	}
	return true
}

func grantTargetOK(j grantJSON) bool {
	if j.Credential == "" && j.Port != 0 {
		return session.ValidPortEntry(session.PortTarget(j.Host, j.Port)) && session.HostName(j.Host) == j.Host && j.Name == "" && j.Bound == nil && j.Delivery == ""
	}
	if j.Credential == "" {
		return session.HostName(j.Host) == j.Host && session.ValidAllowEntry(j.Host) && j.Name == "" && j.Bound == nil && j.Delivery == ""
	}
	return j.Host == "" && j.Port == 0 && grantID.MatchString(j.Credential) && credentialNameOK(j.Name) && boundOK(j.Bound) && (j.Delivery == session.DeliveryHanded || j.Delivery == session.DeliverySigned || j.Delivery == session.DeliveryCopy)
}

func grantOf(j grantJSON) (session.Grant, bool) {
	reason, err := session.CleanHostReason(j.Reason)
	ok := err == nil && grantID.MatchString(j.ID) && grantTargetOK(j) &&
		(j.Scope == session.GrantSession || j.Scope == session.GrantProject) && j.Session != "" && len(j.Session) <= maxGrantField &&
		j.Project != "" && len(j.Project) <= maxGrantField && (j.AskedBy == session.AskedByAgent || j.AskedBy == session.AskedByPerson || j.AskedBy == session.AskedByPreset) &&
		len(j.Preset) <= session.MaxPresetName*4 && j.Expires >= 0 && j.Made > 0
	if !ok {
		return session.Grant{}, false
	}
	g := session.Grant{ID: j.ID, Host: j.Host, Port: j.Port, Credential: j.Credential, Name: j.Name, Bound: j.Bound, Delivery: j.Delivery, Scope: j.Scope, Session: j.Session, Project: j.Project,
		Reason: reason, AskedBy: j.AskedBy, Preset: j.Preset, Made: time.Unix(j.Made, 0)}
	if !gitAcceptedOK(j.GitAccepted, g.Hosts()) {
		return session.Grant{}, false
	}
	g.GitAccepted = j.GitAccepted
	if j.Expires > 0 {
		g.Expires = time.Unix(j.Expires, 0)
	}
	return g, true
}

func (s Grants) Load() ([]session.Grant, error) {
	defer s.lock()()
	data, err := read(s.Dir, grantsFile)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return []session.Grant{}, nil
	}
	var f grantsJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version < 1 || f.Version > grantsVersion || len(f.Grants) > session.MaxGrants {
		return nil, errGrantsFile
	}
	out := make([]session.Grant, 0, len(f.Grants))
	for _, j := range f.Grants {
		g, ok := grantOf(j)
		if !ok {
			return nil, errGrantsFile
		}
		out = append(out, g)
	}
	return out, nil
}

func (s Grants) Save(list []session.Grant) error {
	owner := ownerIn(s.Dir)
	defer s.lock()()
	f := grantsJSON{Version: grantsVersion, Grants: make([]grantJSON, 0, len(list))}
	for _, g := range list {
		j := grantJSON{ID: g.ID, Host: g.Host, Port: g.Port, Credential: g.Credential, Name: g.Name, Bound: g.Bound, Delivery: g.Delivery, Scope: g.Scope, Session: g.Session, Project: g.Project,
			Reason: g.Reason, AskedBy: g.AskedBy, Preset: g.Preset, Made: g.Made.Unix(), GitAccepted: g.GitAccepted, Owner: owner}
		if !g.Expires.IsZero() {
			j.Expires = g.Expires.Unix()
		}
		f.Grants = append(f.Grants, j)
	}
	return write(s.Dir, grantsFile, f)
}
