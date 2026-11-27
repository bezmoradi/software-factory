package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

const (
	challengeRequirements = "/knackpack:challenge-requirements"
	docsVariable          = "SF_DOCS"
)

func docsFile(name string) (string, bool) {
	dir := os.Getenv(docsVariable)
	for _, s := range stdio {
		for _, e := range s.Env {
			if e.Name == docsVariable && e.Value != "" {
				dir = e.Value
			}
		}
	}
	if dir == "" || !filepath.IsAbs(dir) || filepath.Clean(dir) != strings.TrimRight(dir, "/") {
		return "", false
	}
	return filepath.Join(dir, name), true
}

func writeDoc(name, what, text string) string {
	abs, ok := docsFile(name)
	if !ok {
		return "The fake agent has no documents folder: " + docsVariable + " isn't an absolute path, so it wrote no " + what + "."
	}
	if err := os.WriteFile(abs, []byte(text), 0o644); err != nil {
		return "The fake agent couldn't write " + abs + "."
	}
	sum := sha256.Sum256([]byte(text))
	return "The fake agent wrote the " + what + " to " + abs + ", SHA-256 " + hex.EncodeToString(sum[:]) + ", and committed nothing."
}

func requirementsLine(text string) (string, bool) {
	rest, ok := strings.CutPrefix(text, challengeRequirements)
	if !ok {
		return "", false
	}
	_, brief, _ := strings.Cut(rest, "\n")
	return requirementsStep(strings.TrimRight(brief, "\n") + "\n"), true
}

func requirementsStep(text string) string {
	return writeDoc("REQUIREMENTS.md", "requirements brief", text)
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
	return writeDoc("PLAN.md", "plan", fakePlan)
}
