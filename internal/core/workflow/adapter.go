package workflow

import "github.com/bezmoradi/software-factory/internal/core/review"

type AdapterID string

const (
	Knackpack AdapterID = "knackpack"
	SpecKit   AdapterID = "spec-kit"
)

type Adapter struct {
	Commands      map[Step]string
	Agents        map[review.Role]string
	Artifacts     map[Step][]string
	RootArtifacts map[Step][]string
	Documents     Documents
}

type Documents struct {
	Feature      string
	Requirements string
	Research     string
	Plan         string
	Tasks        string
}

var defaultAgents = map[review.Role]string{review.BugHunter: "knackpack:adversarial-reviewer", review.Simplicity: "knackpack:slop-reviewer"}

var Adapters = map[AdapterID]Adapter{
	Knackpack: {
		Commands: map[Step]string{Define: "/knackpack:challenge-requirements", Plan: "/knackpack:plan-implementation", Review: "/knackpack:handoff"},
		Agents:   defaultAgents,
	},
	SpecKit: {
		Commands: map[Step]string{Define: "/speckit-specify", Plan: "/speckit-plan", Tasks: "/speckit-tasks", Implement: "/speckit-implement"},
		Agents:   defaultAgents,
		Artifacts: map[Step][]string{
			Define: {"spec.md", "checklists/**"},
			Plan:   {"plan.md", "research.md", "data-model.md", "quickstart.md", "contracts/**"},
			Tasks:  {"tasks.md"},
		},
		RootArtifacts: map[Step][]string{Define: {".specify/feature.json"}},
		Documents:     Documents{Feature: ".specify/feature.json", Requirements: "spec.md", Research: "research.md", Plan: "plan.md", Tasks: "tasks.md"},
	},
}
