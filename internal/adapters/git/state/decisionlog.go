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

type spanJSON struct {
	Name string    `json:"name"`
	From time.Time `json:"from,omitzero"`
	To   time.Time `json:"to,omitzero"`
}

type projectSpansJSON struct {
	Repo  string     `json:"repo"`
	Spans []spanJSON `json:"spans"`
}

type removedSpansJSON struct {
	URL string `json:"url"`
	projectSpansJSON
}

type decisionLogJSON struct {
	Version      int                    `json:"version"`
	Projects     []decisionProjectJSON  `json:"projects"`
	Removed      []removedDecisionsJSON `json:"removed,omitempty"`
	Spans        []projectSpansJSON     `json:"spans,omitempty"`
	RemovedSpans []removedSpansJSON     `json:"removedSpans,omitempty"`
}

func spanListOf(list []spanJSON, open bool) []session.ProjectSpan {
	var out []session.ProjectSpan
	for i, j := range list {
		last := i == len(list)-1
		if !repoName.MatchString(j.Name) || len(out) >= session.MaxProjectSpans || (j.To.IsZero() && (!open || !last)) || (!j.To.IsZero() && j.To.Before(j.From)) {
			continue
		}
		out = append(out, session.ProjectSpan{Name: j.Name, From: j.From, To: j.To})
	}
	return out
}

func spansJSONOf(repo string, list []session.ProjectSpan) projectSpansJSON {
	p := projectSpansJSON{Repo: repo, Spans: []spanJSON{}}
	for _, x := range list {
		p.Spans = append(p.Spans, spanJSON{Name: x.Name, From: x.From.UTC(), To: x.To.UTC()})
	}
	return p
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
	out := session.DecisionState{Projects: map[string][]session.DecisionEdit{}, Spans: map[string][]session.ProjectSpan{}}
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
	for _, p := range f.Spans {
		if _, dup := out.Spans[p.Repo]; dup || !repoName.MatchString(p.Repo) || len(out.Spans) >= session.MaxDecisionProjects {
			continue
		}
		if list := spanListOf(p.Spans, true); len(list) > 0 {
			out.Spans[p.Repo] = list
		}
	}
	for _, r := range f.RemovedSpans {
		id := session.ProjectID{Name: r.Repo, URL: r.URL}
		if !repoName.MatchString(r.Repo) || !removedID(id) || !session.RoomForRemoved(out.RemovedSpans, id) {
			continue
		}
		if list := spanListOf(r.Spans, false); len(list) > 0 {
			out.RemovedSpans = append(out.RemovedSpans, session.RemovedProject[[]session.ProjectSpan]{ID: id, Data: list})
		}
	}
	return out, nil
}

func (s DecisionLog) Save(st session.DecisionState) (int, error) {
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
	if len(f.Projects) > session.MaxDecisionProjects {
		return 0, errors.New("too many projects with decision log edits")
	}
	sort.Slice(f.Projects, func(i, j int) bool { return f.Projects[i].Repo < f.Projects[j].Repo })
	for repo, list := range st.Spans {
		if repoName.MatchString(repo) && len(list) > 0 {
			f.Spans = append(f.Spans, spansJSONOf(repo, list))
		}
	}
	sort.Slice(f.Spans, func(i, j int) bool { return f.Spans[i].Repo < f.Spans[j].Repo })
	for _, r := range st.RemovedSpans {
		if repoName.MatchString(r.ID.Name) && removedID(r.ID) && len(r.Data) > 0 {
			f.RemovedSpans = append(f.RemovedSpans, removedSpansJSON{URL: r.ID.URL, projectSpansJSON: spansJSONOf(r.ID.Name, r.Data)})
		}
	}
	var removed []session.RemovedProject[[]session.DecisionEdit]
	for _, r := range st.Removed {
		if repoName.MatchString(r.ID.Name) && removedID(r.ID) && len(r.Data) > 0 {
			removed = append(removed, r)
		}
	}
	data, trimmed, err := fitted(removed, func(r session.RemovedProject[[]session.DecisionEdit]) removedDecisionsJSON {
		p, n := editsJSONOf(r.ID.Name, r.Data, owner)
		skipped += n
		return removedDecisionsJSON{URL: r.ID.URL, decisionProjectJSON: p}
	}, func(list []removedDecisionsJSON) ([]byte, error) {
		f.Removed = list
		return encode(f)
	}, func(e []session.DecisionEdit) int { return len(e) })
	if err != nil {
		return 0, err
	}
	if err := writeBytes(s.Dir, decisionLogFile, data); err != nil {
		return 0, err
	}
	noteDropped(s.Dir, decisionLogFile, skipped+trimmed)
	return trimmed, nil
}

func (s DecisionLog) Dropped(n int) { noteDropped(s.Dir, decisionLogFile, n) }
