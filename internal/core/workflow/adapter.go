package workflow

import "github.com/bezmoradi/software-factory/internal/core/review"

type AdapterID string

const Knackpack AdapterID = "knackpack"

type Adapter struct {
	Commands      map[Step]string
	Agents        map[review.Role]string
	Artifacts     map[Step][]string
	RootArtifacts map[Step][]string
	AnyFolder     string
	Documents     Documents
}

type Documents struct {
	Feature      string
	Requirements string
	Plan         string
}

var defaultAgents = map[review.Role]string{review.BugHunter: "knackpack:adversarial-reviewer", review.Simplicity: "knackpack:slop-reviewer"}

var Adapters = map[AdapterID]Adapter{
	Knackpack: {
		Commands: map[Step]string{Define: "/knackpack:challenge-requirements", Plan: "/knackpack:plan-implementation", Review: "/knackpack:handoff"},
		Agents:   defaultAgents,
		Artifacts: map[Step][]string{
			Define: {"REQUIREMENTS.md"},
			Plan:   {"PLAN.md"},
		},
		RootArtifacts: map[Step][]string{Define: {KnackpackFeature}, Plan: {KnackpackFeature}},
		AnyFolder:     ".knackpack/*",
		Documents:     Documents{Feature: KnackpackFeature, Requirements: "REQUIREMENTS.md", Plan: "PLAN.md"},
	},
}
