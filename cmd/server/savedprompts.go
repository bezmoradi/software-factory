package server

import (
	"github.com/bezmoradi/software-factory/internal/adapters/git/state"
	"github.com/bezmoradi/software-factory/internal/adapters/http/page"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

func useSavedPrompts(srv *page.Server, parts engineParts, cfg PageConfig, logger shared.Logger) {
	repos := parts.repos
	if repos == nil {
		return
	}
	known := func(name string) bool {
		list, err := repos.List()
		if err != nil {
			return false
		}
		for _, r := range list {
			if r.Name == name {
				return true
			}
		}
		return false
	}
	srv.UseSavedPrompts(session.NewSavedPrompts(session.SavedPromptsConfig{Store: state.NewSavedPrompts(cfg.StateDir), Known: known, Logger: logger}))
}
