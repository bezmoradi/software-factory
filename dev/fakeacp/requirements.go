package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

const (
	challengeRequirements = "/knackpack:challenge-requirements"
	requirementsTask      = "fake-feature"
)

func requirementsLine(text string) (string, bool) {
	rest, ok := strings.CutPrefix(text, challengeRequirements)
	if !ok {
		return "", false
	}
	_, brief, _ := strings.Cut(rest, "\n")
	return requirementsStep(strings.TrimRight(brief, "\n") + "\n"), true
}

func requirementsStep(text string) string {
	rel := filepath.Join(".knackpack", requirementsTask, "REQUIREMENTS.md")
	abs := filepath.Join(worktreeTop(), rel)
	feature, _ := json.Marshal(map[string]string{"feature_directory": ".knackpack/" + requirementsTask})
	if os.MkdirAll(filepath.Dir(abs), 0o755) != nil || os.WriteFile(abs, []byte(text), 0o644) != nil || os.WriteFile(filepath.Join(worktreeTop(), ".knackpack", "feature.json"), append(feature, '\n'), 0o644) != nil {
		return "The fake agent couldn't write " + rel + "."
	}
	commit, ok := commitFiles("docs(knackpack): requirements for "+requirementsTask, rel, ".knackpack/feature.json")
	if !ok {
		return "The fake agent wrote " + rel + " and couldn't commit it."
	}
	sum := sha256.Sum256([]byte(text))
	return "The fake agent wrote the requirements brief: " + rel + ", committed as " + commit + ", SHA-256 " + hex.EncodeToString(sum[:]) + "."
}

const planImplementation = "/knackpack:plan-implementation"

const fakePlan = "# Plan: the fake export\n\n1. Write the export.\n2. Refuse an export over 10,000 rows.\n"

func planLine(text string) (string, bool) {
	if !strings.HasPrefix(text, planImplementation) {
		return "", false
	}
	return planStep(), true
}

func planStep() string {
	rel := filepath.Join(".knackpack", requirementsTask, "PLAN.md")
	abs := filepath.Join(worktreeTop(), rel)
	if os.MkdirAll(filepath.Dir(abs), 0o755) != nil || os.WriteFile(abs, []byte(fakePlan), 0o644) != nil {
		return "The fake agent couldn't write " + rel + "."
	}
	commit, ok := commitFiles("docs(knackpack): plan for "+requirementsTask, rel)
	if !ok {
		return "The fake agent wrote " + rel + " and couldn't commit it."
	}
	sum := sha256.Sum256([]byte(fakePlan))
	return "The fake agent wrote the plan: " + rel + ", committed as " + commit + ", SHA-256 " + hex.EncodeToString(sum[:]) + "."
}
