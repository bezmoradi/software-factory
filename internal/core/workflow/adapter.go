package workflow

import (
	"maps"
	"slices"

	"github.com/bezmoradi/software-factory/internal/core/review"
)

type AdapterID string

const Knackpack AdapterID = "knackpack"

type Adapter struct {
	Name          string
	Commands      map[Step]string
	Agents        map[review.Role]string
	Artifacts     map[Step][]string
	RootArtifacts map[Step][]string
	AnyFolder     string
	Folder        string
	Documents     Documents
}

type Documents struct {
	Feature      string
	Requirements string
	Plan         string
	RootCause    string
}

var defaultAgents = map[review.Role]string{review.BugHunter: "knackpack:adversarial-reviewer", review.Simplicity: "knackpack:slop-reviewer"}

var Adapters = map[AdapterID]Adapter{
	Knackpack: {
		Name:     string(Knackpack),
		Commands: map[Step]string{Define: "/knackpack:challenge-requirements", Plan: "/knackpack:plan-implementation", Review: "/knackpack:handoff"},
		Agents:   defaultAgents,
		Artifacts: map[Step][]string{
			Define: {"REQUIREMENTS.md"},
			Plan:   {"PLAN.md"},
		},
		RootArtifacts: map[Step][]string{Define: {KnackpackFeature}, Plan: {KnackpackFeature}},
		AnyFolder:     ".knackpack/*",
		Folder:        ".knackpack",
		Documents:     Documents{Feature: KnackpackFeature, Requirements: "REQUIREMENTS.md", Plan: "PLAN.md", RootCause: RootCauseFile},
	},
}

func (a Adapter) HasDocuments() bool {
	return a.Documents.Requirements != "" && a.Documents.Plan != ""
}

func PackAdapter(m PackManifest) Adapter {
	a := Adapter{Name: m.Name, Commands: map[Step]string{}, Agents: map[review.Role]string{}}
	maps.Copy(a.Commands, m.Steps)
	for role, agent := range m.Roles {
		a.Agents[role] = m.Name + ":" + agent
	}
	if d := m.Documents; d != nil {
		feature := d.Folder + "/feature.json"
		a.Artifacts = map[Step][]string{Define: {d.Requirements}, Plan: {d.Plan}}
		a.RootArtifacts = map[Step][]string{Define: {feature}, Plan: {feature}}
		a.AnyFolder, a.Folder = d.Folder+"/*", d.Folder
		a.Documents = Documents{Feature: feature, Requirements: d.Requirements, Plan: d.Plan}
	}
	return a
}

const NoPack AdapterID = "none"

func NoAdapter() Adapter {
	return Adapter{Name: string(NoPack), Commands: map[Step]string{}, Agents: map[review.Role]string{}}
}

func Offered(types []WorkType, a Adapter) []WorkType {
	out := make([]WorkType, 0, len(types))
	for _, t := range types {
		switch {
		case t.Profile() == Incident && a.Documents.RootCause == "":
		case a.HasDocuments() || (!slices.Contains(t.Steps, Define) && !slices.Contains(t.Steps, Plan)):
			out = append(out, t)
		}
	}
	return out
}
