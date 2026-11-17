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

type removedLookupJSON struct {
	Project string `json:"project"`
	URL     string `json:"url"`
}

type lookupsJSON struct {
	Version  int                 `json:"version"`
	Projects []string            `json:"projects"`
	Removed  []removedLookupJSON `json:"removed,omitempty"`
	Owner    string              `json:"owner,omitempty"`
}

var errLookups = errors.New("package-lookups.json isn't a list of projects this engine reads")

func (s PackageLookups) lock() func() {
	if s.mu == nil {
		return func() {}
	}
	s.mu.Lock()
	return s.mu.Unlock
}

func (s PackageLookups) Load() (session.LookupState, error) {
	defer s.lock()()
	out := session.LookupState{Projects: map[string]bool{}}
	data, err := read(s.Dir, lookupsFile)
	if err != nil {
		return session.LookupState{}, err
	}
	if data == nil {
		return out, nil
	}
	var f lookupsJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 || len(f.Projects) > session.MaxLookupProjects {
		return session.LookupState{}, errLookups
	}
	for _, p := range f.Projects {
		if !lookupProject.MatchString(p) {
			return session.LookupState{}, errLookups
		}
		out.Projects[p] = true
	}
	for _, r := range f.Removed {
		id := session.ProjectID{Name: r.Project, URL: r.URL}
		if lookupProject.MatchString(r.Project) && removedID(id) && session.RoomForRemoved(out.Removed, id) {
			out.Removed = append(out.Removed, session.RemovedProject[bool]{ID: id, Data: true})
		}
	}
	return out, nil
}

func (s PackageLookups) Save(st session.LookupState) error {
	owner := ownerIn(s.Dir)
	defer s.lock()()
	out := []string{}
	for p, on := range st.Projects {
		if on && lookupProject.MatchString(p) {
			out = append(out, p)
		}
	}
	if len(out) > session.MaxLookupProjects {
		return errLookups
	}
	slices.Sort(out)
	f := lookupsJSON{Owner: owner, Version: 1, Projects: out}
	for _, r := range st.Removed {
		if r.Data && lookupProject.MatchString(r.ID.Name) && removedID(r.ID) {
			f.Removed = append(f.Removed, removedLookupJSON{Project: r.ID.Name, URL: r.ID.URL})
		}
	}
	return write(s.Dir, lookupsFile, f)
}
