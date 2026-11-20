package state

import (
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/review"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

const packsFile = "packs.json"

var (
	packCommit = regexp.MustCompile(`^[0-9a-f]{40}$`)
	packAgent  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
)

type Packs struct {
	Dir string
	mu  *sync.Mutex
}

func NewPacks(dir string) Packs { return Packs{Dir: dir, mu: &sync.Mutex{}} }

var _ session.PackStore = Packs{}

type packSourceJSON struct {
	Kind   string `json:"kind"`
	URL    string `json:"url"`
	Commit string `json:"commit"`
}

type packDocsJSON struct {
	Folder       string `json:"folder"`
	Requirements string `json:"requirements"`
	Plan         string `json:"plan"`
}

type packJSON struct {
	Digest        string            `json:"digest"`
	SessionDigest string            `json:"sessionDigest"`
	Name          string            `json:"name"`
	Version       string            `json:"version"`
	Source        packSourceJSON    `json:"source"`
	Roles         map[string]string `json:"roles"`
	Steps         map[string]string `json:"steps"`
	Documents     *packDocsJSON     `json:"documents,omitempty"`
	Agents        []string          `json:"agents"`
	CodeFiles     []string          `json:"codeFiles"`
	AddedAt       time.Time         `json:"addedAt"`
	Owner         string            `json:"owner,omitempty"`
}

type packChoiceJSON struct {
	Project string `json:"project"`
	URL     string `json:"url"`
	Pack    string `json:"pack"`
	Owner   string `json:"owner,omitempty"`
}

type packsJSON struct {
	Version int              `json:"version"`
	Packs   []packJSON       `json:"packs"`
	Choices []packChoiceJSON `json:"choices"`
}

func (s Packs) Load() (session.PackState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := read(s.Dir, packsFile)
	if err != nil || data == nil {
		return session.PackState{}, err
	}
	var f packsJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 {
		return session.PackState{}, errors.New(packsFile + " isn't a packs record this engine reads")
	}
	var out session.PackState
	for _, p := range f.Packs {
		if sp, ok := storedPack(p); ok && len(out.Packs) < session.MaxPacks && !slices.ContainsFunc(out.Packs, func(x session.StoredPack) bool { return x.Digest == sp.Digest }) {
			out.Packs = append(out.Packs, sp)
		}
	}
	for _, c := range f.Choices {
		known := c.Pack == session.PackKnackpack || c.Pack == session.PackNone || slices.ContainsFunc(out.Packs, func(x session.StoredPack) bool { return x.Digest == c.Pack })
		if c.Project == "" || !known {
			continue
		}
		out.Choices = append(out.Choices, session.PackChoice{Project: c.Project, URL: c.URL, Pack: c.Pack})
	}
	return out, nil
}

func storedPack(p packJSON) (session.StoredPack, bool) {
	if !session.PackDigest.MatchString(p.Digest) || !session.PackDigest.MatchString(p.SessionDigest) || workflow.PackNameProblem(p.Name) != "" || !workflow.PackVersion(p.Version) ||
		!sourceKnown(p.Source) || len(p.Agents) > 100 || len(p.CodeFiles) > 51 {
		return session.StoredPack{}, false
	}
	m := workflow.PackManifest{Name: p.Name, Version: p.Version, Roles: map[review.Role]string{}, Steps: map[workflow.Step]string{}}
	for k, v := range p.Roles {
		if role := review.Role(k); slices.Contains(review.Roles, role) && slices.Contains(p.Agents, v) {
			m.Roles[role] = v
		}
	}
	for k, v := range p.Steps {
		if len(v) <= 140 {
			m.Steps[workflow.Step(k)] = v
		}
	}
	if d := p.Documents; d != nil {
		m.Documents = &workflow.PackDocuments{Folder: d.Folder, Requirements: d.Requirements, Plan: d.Plan}
	}
	var agents []string
	for _, a := range p.Agents {
		if packAgent.MatchString(a) {
			agents = append(agents, a)
		}
	}
	return session.StoredPack{Digest: p.Digest, SessionDigest: p.SessionDigest, Name: p.Name, Version: p.Version,
		Source: session.PackSource{Kind: p.Source.Kind, URL: p.Source.URL, Commit: p.Source.Commit}, Manifest: m, Agents: agents,
		CodeFiles: slices.Clone(p.CodeFiles), AddedAt: p.AddedAt}, true
}

func sourceKnown(s packSourceJSON) bool {
	switch s.Kind {
	case session.PackFromGit:
		return packCommit.MatchString(s.Commit) && s.URL != "" && len(s.URL) <= session.MaxPackURL
	case session.PackUploaded:
		return s.Commit == "" && s.URL == ""
	}
	return false
}

func (s Packs) Save(st session.PackState) error {
	owner := ownerIn(s.Dir)
	s.mu.Lock()
	defer s.mu.Unlock()
	f := packsJSON{Version: 1, Packs: make([]packJSON, 0, len(st.Packs)), Choices: make([]packChoiceJSON, 0, len(st.Choices))}
	for _, p := range st.Packs {
		j := packJSON{Owner: owner, Digest: p.Digest, SessionDigest: p.SessionDigest, Name: p.Name, Version: p.Version,
			Source: packSourceJSON{Kind: p.Source.Kind, URL: p.Source.URL, Commit: p.Source.Commit}, Roles: map[string]string{}, Steps: map[string]string{},
			Agents: nonNilStrings(p.Agents), CodeFiles: nonNilStrings(p.CodeFiles), AddedAt: p.AddedAt.UTC()}
		for role, agent := range p.Manifest.Roles {
			j.Roles[string(role)] = agent
		}
		for step, cmd := range p.Manifest.Steps {
			j.Steps[string(step)] = cmd
		}
		if d := p.Manifest.Documents; d != nil {
			j.Documents = &packDocsJSON{Folder: d.Folder, Requirements: d.Requirements, Plan: d.Plan}
		}
		f.Packs = append(f.Packs, j)
	}
	for _, c := range st.Choices {
		f.Choices = append(f.Choices, packChoiceJSON{Owner: owner, Project: c.Project, URL: c.URL, Pack: c.Pack})
	}
	return write(s.Dir, packsFile, f)
}

func nonNilStrings(list []string) []string {
	if list == nil {
		return []string{}
	}
	return slices.Clone(list)
}
