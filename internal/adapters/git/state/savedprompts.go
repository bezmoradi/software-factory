package state

import (
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const savedPromptsFile = "savedprompts.json"

type SavedPrompts struct {
	Dir string
	mu  *sync.Mutex
}

func NewSavedPrompts(dir string) SavedPrompts { return SavedPrompts{Dir: dir, mu: &sync.Mutex{}} }

var _ session.SavedPromptStore = SavedPrompts{}

type savedPromptJSON struct {
	ID    string    `json:"id"`
	Name  string    `json:"name"`
	Text  string    `json:"text"`
	At    time.Time `json:"at"`
	Owner string    `json:"owner,omitempty"`
}

type promptProjectJSON struct {
	Repo    string            `json:"repo"`
	Prompts []savedPromptJSON `json:"prompts"`
}

type removedPromptsJSON struct {
	URL string `json:"url"`
	promptProjectJSON
}

type savedPromptsJSON struct {
	Version  int                  `json:"version"`
	Projects []promptProjectJSON  `json:"projects"`
	Removed  []removedPromptsJSON `json:"removed,omitempty"`
}

func promptOK(p savedPromptJSON) bool {
	return session.ValidPromptID(p.ID) && session.ValidPromptName(p.Name) && session.ValidPromptText(p.Text)
}

func promptsOf(p promptProjectJSON, total *int, budget int) []session.SavedPrompt {
	var list []session.SavedPrompt
	seen := map[string]bool{}
	for _, x := range p.Prompts {
		if len(list) >= session.MaxSavedPrompts || seen[x.ID] || !promptOK(x) || *total+len(x.Name)+len(x.Text) > budget {
			continue
		}
		seen[x.ID] = true
		*total += len(x.Name) + len(x.Text)
		list = append(list, session.SavedPrompt{ID: x.ID, Name: x.Name, Text: x.Text, At: x.At})
	}
	return list
}

func promptsJSONOf(repo string, list []session.SavedPrompt, owner string) promptProjectJSON {
	p := promptProjectJSON{Repo: repo, Prompts: []savedPromptJSON{}}
	for _, x := range list {
		j := savedPromptJSON{Owner: owner, ID: x.ID, Name: x.Name, Text: x.Text, At: x.At.UTC()}
		if promptOK(j) && len(p.Prompts) < session.MaxSavedPrompts {
			p.Prompts = append(p.Prompts, j)
		}
	}
	return p
}

func (s SavedPrompts) Load() (session.SavedPromptState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := read(s.Dir, savedPromptsFile)
	if err != nil {
		return session.SavedPromptState{}, err
	}
	out := session.SavedPromptState{Projects: map[string][]session.SavedPrompt{}}
	if data == nil {
		return out, nil
	}
	var f savedPromptsJSON
	if err := json.Unmarshal(data, &f); err != nil || !readsVersion(savedPromptsFile, f.Version) {
		return session.SavedPromptState{}, errors.New("savedprompts.json isn't a record of saved prompts this engine reads")
	}
	total, removedTotal := 0, 0
	for _, p := range f.Projects {
		if !repoName.MatchString(p.Repo) || len(out.Projects) >= session.MaxPromptProjects {
			continue
		}
		if _, dup := out.Projects[p.Repo]; dup {
			continue
		}
		if list := promptsOf(p, &total, session.MaxSavedPromptBytes); len(list) > 0 {
			out.Projects[p.Repo] = list
		}
	}
	for _, r := range f.Removed {
		id := session.ProjectID{Name: r.Repo, URL: r.URL}
		if !repoName.MatchString(r.Repo) || !removedID(id) || !session.RoomForRemoved(out.Removed, id) {
			continue
		}
		if list := promptsOf(r.promptProjectJSON, &removedTotal, maxFile); len(list) > 0 {
			out.Removed = append(out.Removed, session.RemovedProject[[]session.SavedPrompt]{ID: id, Data: list})
		}
	}
	return out, nil
}

func (s SavedPrompts) Save(st session.SavedPromptState) (int, error) {
	owner := ownerIn(s.Dir)
	s.mu.Lock()
	defer s.mu.Unlock()
	f := savedPromptsJSON{Version: writesVersion(savedPromptsFile), Projects: []promptProjectJSON{}}
	for repo, list := range st.Projects {
		if repoName.MatchString(repo) && len(list) > 0 {
			f.Projects = append(f.Projects, promptsJSONOf(repo, list, owner))
		}
	}
	if len(f.Projects) > session.MaxPromptProjects {
		return 0, errors.New("too many projects with saved prompts")
	}
	sort.Slice(f.Projects, func(i, j int) bool { return f.Projects[i].Repo < f.Projects[j].Repo })
	var removed []session.RemovedProject[[]session.SavedPrompt]
	for _, r := range st.Removed {
		if repoName.MatchString(r.ID.Name) && removedID(r.ID) && len(r.Data) > 0 {
			removed = append(removed, r)
		}
	}
	data, dropped, err := fitted(removed, func(r session.RemovedProject[[]session.SavedPrompt]) removedPromptsJSON {
		return removedPromptsJSON{URL: r.ID.URL, promptProjectJSON: promptsJSONOf(r.ID.Name, r.Data, owner)}
	}, func(list []removedPromptsJSON) ([]byte, error) {
		f.Removed = list
		return encode(f)
	}, func(l []session.SavedPrompt) int { return len(l) })
	if err != nil {
		return 0, err
	}
	if err := writeBytes(s.Dir, savedPromptsFile, data); err != nil {
		return 0, err
	}
	noteDropped(s.Dir, savedPromptsFile, dropped)
	return dropped, nil
}
