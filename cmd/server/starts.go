package server

import (
	"context"

	"github.com/bezmoradi/software-factory/internal/adapters/git/state"
	"github.com/bezmoradi/software-factory/internal/adapters/http/page"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

func useStarts(base context.Context, srv *page.Server, reg *session.Registry, parts engineParts, cfg PageConfig, logger shared.Logger) {
	if parts.dispatcher == nil {
		return
	}
	var repos session.SplitRepos
	if parts.repos != nil {
		repos = parts.repos
	}
	splits, err := session.NewSplits(session.SplitsConfig{Dispatcher: parts.dispatcher, Store: state.NewSplits(cfg.StateDir), Repos: repos, Show: reg.SetSplits, Tell: tellSession(reg),
		Base: base, Logger: logger})
	if err != nil {
		logger.Warn("splits", "event", "not-started")
		return
	}
	splits.Restore()
	go splits.Run(base)
	srv.UseSplits(splits)
}
