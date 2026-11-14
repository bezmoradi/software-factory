package state

import (
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const decisionLogFile = "decisionlog.json"

type DecisionLog struct {
	Dir string
	mu  *sync.Mutex
}

func NewDecisionLog(dir string) DecisionLog { return DecisionLog{Dir: dir, mu: &sync.Mutex{}} }

var _ session.DecisionStore = DecisionLog{}

type decisionEditJSON struct {
	ID     string    `json:"id"`
	Added  bool      `json:"added,omitempty"`
	Name   string    `json:"name,omitempty"`
	Text   string    `json:"text,omitempty"`
	Hidden bool      `json:"hidden,omitempty"`
	At     time.Time `json:"at"`
}

type decisionProjectJSON struct {
	Repo    string             `json:"repo"`
	Entries []decisionEditJSON `json:"entries"`
}

type decisionLogJSON struct {
	Version  int                   `json:"version"`
	Projects []decisionProjectJSON `json:"projects"`
}

func editOf(j decisionEditJSON) session.DecisionEdit {
	return session.DecisionEdit{ID: j.ID, Added: j.Added, Name: j.Name, Text: j.Text, Hidden: j.Hidden, At: j.At}
}

func (s DecisionLog) Load() (map[string][]session.DecisionEdit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := read(s.Dir, decisionLogFile)
	if err != nil {
		return nil, err
	}
	out := map[string][]session.DecisionEdit{}
	if data == nil {
		return out, nil
	}
	var f decisionLogJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 {
		return nil, errors.New("decisionlog.json isn't a record of decision log edits this engine reads")
	}
	for _, p := range f.Projects {
		if !repoName.MatchString(p.Repo) || len(out) >= session.MaxDecisionProjects {
			continue
		}
		if _, dup := out[p.Repo]; dup {
			continue
		}
		var list []session.DecisionEdit
		seen := map[string]bool{}
		added := 0
		for _, j := range p.Entries {
			e := editOf(j)
			if len(list) >= session.MaxDecisionEdits || seen[e.ID] || !session.ValidDecisionEdit(e) || e.Added && added >= session.MaxDecisionAdded {
				continue
			}
			if e.Added {
				added++
			}
			seen[e.ID] = true
			list = append(list, e)
		}
		if len(list) > 0 {
			out[p.Repo] = list
		}
	}
	return out, nil
}

func (s DecisionLog) Save(all map[string][]session.DecisionEdit) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := decisionLogJSON{Version: 1, Projects: []decisionProjectJSON{}}
	skipped := 0
	for repo, list := range all {
		if !repoName.MatchString(repo) || len(list) == 0 {
			continue
		}
		p := decisionProjectJSON{Repo: repo, Entries: []decisionEditJSON{}}
		for _, e := range list {
			if !session.ValidDecisionEdit(e) || len(p.Entries) >= session.MaxDecisionEdits {
				skipped++
				continue
			}
			p.Entries = append(p.Entries, decisionEditJSON{ID: e.ID, Added: e.Added, Name: e.Name, Text: e.Text, Hidden: e.Hidden, At: e.At.UTC()})
		}
		f.Projects = append(f.Projects, p)
	}
	if len(f.Projects) > session.MaxDecisionProjects {
		return errors.New("too many projects with decision log edits")
	}
	sort.Slice(f.Projects, func(i, j int) bool { return f.Projects[i].Repo < f.Projects[j].Repo })
	if err := write(s.Dir, decisionLogFile, f); err != nil {
		return err
	}
	noteDropped(s.Dir, decisionLogFile, skipped)
	return nil
}

func (s DecisionLog) Dropped(n int) { noteDropped(s.Dir, decisionLogFile, n) }
