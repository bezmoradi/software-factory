package server

import (
	"github.com/bezmoradi/software-factory/internal/adapters/install/npm"
	"github.com/bezmoradi/software-factory/internal/core/review"
)

const fakeReviewer = "sf-fake-claude"

func pinnedProvenance(dev bool) (string, string) {
	plugin, claude := "", ""
	for _, p := range npm.PinnedPlugins {
		if p.ID == "knackpack" {
			plugin = p.Commit
		}
	}
	for _, a := range npm.Pinned {
		if a.ID == "claude" {
			claude = a.Version
		}
	}
	if dev {
		claude = fakeReviewer
	}
	return plugin, claude
}

func reviewerMinimum(plugin string) review.Minimum {
	return review.Minimum{ClaudeCode: npm.MinimumReviewerClaudeCode, Plugin: npm.MinimumReviewerKnackpack, Pinned: plugin}
}
