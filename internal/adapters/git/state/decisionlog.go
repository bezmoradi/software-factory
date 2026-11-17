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
	Owner  string    `json:"owner,omitempty"`
}

type decisionProjectJSON struct {
	Repo    string             `json:"repo"`
	Entries []decisionEditJSON `json:"entries"`
}

type removedDecisionsJSON struct {
	URL string `json:"url"`
	decisionProjectJSON
}

type decisionLogJSON struct {
	Version  int                    `json:"version"`
	Projects []decisionProjectJSON  `json:"projects"`
	Removed  []removedDecisionsJSON `json:"removed,omitempty"`
}

func editOf(j decisionEditJSON) session.DecisionEdit {
	return session.DecisionEdit{ID: j.ID, Added: j.Added, Name: j.Name, Text: j.Text, Hidden: j.Hidden, At: j.At}
}

func editsOf(p decisionProjectJSON) []session.DecisionEdit {
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
	return list
}

func editsJSONOf(repo string, list []session.DecisionEdit, owner string) (decisionProjectJSON, int) {
	p := decisionProjectJSON{Repo: repo, Entries: []decisionEditJSON{}}
	skipped := 0
	for _, e := range list {
		if !session.ValidDecisionEdit(e) || len(p.Entries) >= session.MaxDecisionEdits {
			skipped++
			continue
		}
		p.Entries = append(p.Entries, decisionEditJSON{Owner: owner, ID: e.ID, Added: e.Added, Name: e.Name, Text: e.Text, Hidden: e.Hidden, At: e.At.UTC()})
	}
	return p, skipped
}

func (s DecisionLog) Load() (session.DecisionState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := read(s.Dir, decisionLogFile)
	if err != nil {
		return session.DecisionState{}, err
	}
	out := session.DecisionState{Projects: map[string][]session.DecisionEdit{}}
	if data == nil {
		return out, nil
	}
	var f decisionLogJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 {
		return session.DecisionState{}, errors.New("decisionlog.json isn't a record of decision log edits this engine reads")
	}
	for _, p := range f.Projects {
		if !repoName.MatchString(p.Repo) || len(out.Projects) >= session.MaxDecisionProjects {
			continue
		}
		if _, dup := out.Projects[p.Repo]; dup {
			continue
		}
		if list := editsOf(p); len(list) > 0 {
			out.Projects[p.Repo] = list
		}
	}
	for _, r := range f.Removed {
		id := session.ProjectID{Name: r.Repo, URL: r.URL}
		if !repoName.MatchString(r.Repo) || !removedID(id) || !session.RoomForRemoved(out.Removed, id) {
			continue
		}
		if list := editsOf(r.decisionProjectJSON); len(list) > 0 {
			out.Removed = append(out.Removed, session.RemovedProject[[]session.DecisionEdit]{ID: id, Data: list})
		}
	}
	return out, nil
}

func (s DecisionLog) Save(st session.DecisionState) error {
	owner := ownerIn(s.Dir)
	s.mu.Lock()
	defer s.mu.Unlock()
	f := decisionLogJSON{Version: 1, Projects: []decisionProjectJSON{}}
	skipped := 0
	for repo, list := range st.Projects {
		if !repoName.MatchString(repo) || len(list) == 0 {
			continue
		}
		p, n := editsJSONOf(repo, list, owner)
		skipped += n
		f.Projects = append(f.Projects, p)
	}
	for _, r := range st.Removed {
		if !repoName.MatchString(r.ID.Name) || !removedID(r.ID) || len(r.Data) == 0 {
			continue
		}
		p, n := editsJSONOf(r.ID.Name, r.Data, owner)
		skipped += n
		f.Removed = append(f.Removed, removedDecisionsJSON{URL: r.ID.URL, decisionProjectJSON: p})
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
