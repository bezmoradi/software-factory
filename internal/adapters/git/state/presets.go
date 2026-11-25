package state

import (
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	presetsFile    = "presets.json"
	presetsVersion = 3
)

type Presets struct {
	Dir string
	mu  *sync.Mutex
}

func NewPresets(dir string) Presets { return Presets{Dir: dir, mu: &sync.Mutex{}} }

var _ session.PresetStore = Presets{}

type presetJSON struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Host        string   `json:"host,omitempty"`
	Port        int      `json:"port,omitempty"`
	Credential  string   `json:"credential,omitempty"`
	Scope       string   `json:"scope"`
	Expires     string   `json:"expires"`
	Project     string   `json:"project,omitempty"`
	Auto        bool     `json:"auto,omitempty"`
	Made        int64    `json:"made"`
	GitAccepted []string `json:"gitAccepted,omitempty"`
	Owner       string   `json:"owner,omitempty"`
}

type presetsJSON struct {
	Version int          `json:"version"`
	Presets []presetJSON `json:"presets"`
}

var errPresetsFile = errors.New("presets.json isn't a list of presets this engine reads")

func (s Presets) lock() func() {
	if s.mu == nil {
		return func() {}
	}
	s.mu.Lock()
	return s.mu.Unlock
}

func presetOf(j presetJSON) (session.Preset, bool) {
	target := j.Credential == "" && j.Port == 0 && session.HostName(j.Host) == j.Host && session.ValidAllowEntry(j.Host) ||
		j.Credential == "" && j.Port != 0 && session.HostName(j.Host) == j.Host && session.ValidPortEntry(session.PortTarget(j.Host, j.Port)) ||
		j.Host == "" && j.Port == 0 && grantID.MatchString(j.Credential)
	ok := grantID.MatchString(j.ID) && j.Name != "" && len(j.Name) <= session.MaxPresetName*4 && target &&
		(j.Scope == session.GrantSession || j.Scope == session.GrantProject) && len(j.Expires) <= 16 && j.Expires != "" &&
		len(j.Project) <= maxGrantField && (!j.Auto || j.Project != "") && j.Made > 0 && gitAcceptedOK(j.GitAccepted, presetHosts(j))
	if !ok {
		return session.Preset{}, false
	}
	return session.Preset{ID: j.ID, Name: j.Name, Host: j.Host, Port: j.Port, Credential: j.Credential, Scope: j.Scope, Expires: j.Expires, Project: j.Project, Auto: j.Auto, Made: time.Unix(j.Made, 0), GitAccepted: j.GitAccepted}, true
}

func presetHosts(j presetJSON) []string {
	switch {
	case j.Credential != "":
		return nil
	case j.Port != 0:
		return []string{session.PortTarget(j.Host, j.Port)}
	}
	return []string{j.Host}
}

func (s Presets) Load() ([]session.Preset, error) {
	defer s.lock()()
	data, err := read(s.Dir, presetsFile)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return []session.Preset{}, nil
	}
	var f presetsJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version < 1 || f.Version > presetsVersion || len(f.Presets) > session.MaxPresets {
		return nil, errPresetsFile
	}
	out := make([]session.Preset, 0, len(f.Presets))
	for _, j := range f.Presets {
		p, ok := presetOf(j)
		if !ok {
			return nil, errPresetsFile
		}
		out = append(out, p)
	}
	return out, nil
}

func (s Presets) Save(list []session.Preset) error {
	owner := ownerIn(s.Dir)
	defer s.lock()()
	f := presetsJSON{Version: presetsVersion, Presets: make([]presetJSON, 0, len(list))}
	for _, p := range list {
		f.Presets = append(f.Presets, presetJSON{ID: p.ID, Name: p.Name, Host: p.Host, Port: p.Port, Credential: p.Credential, Scope: p.Scope, Expires: p.Expires, Project: p.Project, Auto: p.Auto, Made: p.Made.Unix(), GitAccepted: p.GitAccepted, Owner: owner})
	}
	return write(s.Dir, presetsFile, f)
}
