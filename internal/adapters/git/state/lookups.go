package state

import (
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"sync"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const lookupsFile = "package-lookups.json"

var lookupProject = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

type PackageLookups struct {
	Dir string
	mu  *sync.Mutex
}

func NewPackageLookups(dir string) PackageLookups { return PackageLookups{Dir: dir, mu: &sync.Mutex{}} }

var _ session.LookupStore = PackageLookups{}

type lookupsJSON struct {
	Version  int      `json:"version"`
	Projects []string `json:"projects"`
}

var errLookups = errors.New("package-lookups.json isn't a list of projects this engine reads")

func (s PackageLookups) lock() func() {
	if s.mu == nil {
		return func() {}
	}
	s.mu.Lock()
	return s.mu.Unlock
}

func (s PackageLookups) Load() ([]string, error) {
	defer s.lock()()
	data, err := read(s.Dir, lookupsFile)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return []string{}, nil
	}
	var f lookupsJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 || len(f.Projects) > session.MaxLookupProjects {
		return nil, errLookups
	}
	for _, p := range f.Projects {
		if !lookupProject.MatchString(p) {
			return nil, errLookups
		}
	}
	return slices.Clone(f.Projects), nil
}

func (s PackageLookups) Save(list []string) error {
	defer s.lock()()
	if len(list) > session.MaxLookupProjects {
		return errLookups
	}
	out := make([]string, 0, len(list))
	for _, p := range list {
		if lookupProject.MatchString(p) {
			out = append(out, p)
		}
	}
	return write(s.Dir, lookupsFile, lookupsJSON{Version: 1, Projects: out})
}
