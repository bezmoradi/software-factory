package workflow

import (
	"maps"
	"slices"

	"github.com/bezmoradi/software-factory/internal/core/review"
)

type AdapterID string

const Knackpack AdapterID = "knackpack"

type Adapter struct {
	Name      string
	Commands  map[Step]string
	Agents    map[review.Role]string
	Folder    string
	Documents Documents
}

type Documents struct {
	Requirements string
	Plan         string
	RootCause    string
}

var defaultAgents = map[review.Role]string{review.BugHunter: "knackpack:adversarial-reviewer", review.Simplicity: "knackpack:slop-reviewer"}

var Adapters = map[AdapterID]Adapter{
	Knackpack: {
		Name:      string(Knackpack),
		Commands:  map[Step]string{Define: "/knackpack:challenge-requirements", Plan: "/knackpack:plan-implementation"},
		Agents:    defaultAgents,
		Folder:    KnackpackFolder,
		Documents: Documents{Requirements: "REQUIREMENTS.md", Plan: "PLAN.md", RootCause: RootCauseFile},
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
		a.Folder = d.Folder
		a.Documents = Documents{Requirements: d.Requirements, Plan: d.Plan}
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
		case t.Builtin && t.ID == DefaultType:
			t.Steps = slices.DeleteFunc(slices.Clone(t.Steps), func(s Step) bool { return s == Define || s == Plan })
			out = append(out, t)
		}
	}
	return out
}
