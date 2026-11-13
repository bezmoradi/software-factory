package state

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const reposFile = "repos.json"

type Repos struct {
	Dir string
}

var _ session.Records = Repos{}

type repoJSON struct {
	Name    string    `json:"name"`
	URL     string    `json:"url"`
	Branch  string    `json:"branch"`
	Commit  string    `json:"commit,omitempty"`
	AddedAt time.Time `json:"addedAt"`
}

type reposJSON struct {
	Version int        `json:"version"`
	Repos   []repoJSON `json:"repos"`
}

func (s Repos) Load() ([]session.Record, error) {
	data, err := read(s.Dir, reposFile)
	if err != nil || data == nil {
		return []session.Record{}, err
	}
	var f reposJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 {
		return nil, errors.New("repos.json isn't a repository record this engine reads")
	}
	out := make([]session.Record, 0, len(f.Repos))
	for _, r := range f.Repos {
		out = append(out, session.Record{Name: r.Name, URL: r.URL, Branch: r.Branch, Commit: r.Commit, AddedAt: r.AddedAt})
	}
	return out, nil
}

func (s Repos) Save(list []session.Record) error {
	f := reposJSON{Version: 1, Repos: make([]repoJSON, 0, len(list))}
	for _, r := range list {
		f.Repos = append(f.Repos, repoJSON{Name: r.Name, URL: r.URL, Branch: r.Branch, Commit: r.Commit, AddedAt: r.AddedAt.UTC()})
	}
	return write(s.Dir, reposFile, f)
}
