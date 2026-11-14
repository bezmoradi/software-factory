package server

import (
	"context"

	"github.com/bezmoradi/software-factory/internal/adapters/git/files"
	"github.com/bezmoradi/software-factory/internal/adapters/git/state"
	"github.com/bezmoradi/software-factory/internal/adapters/http/page"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

func useStarts(base context.Context, srv *page.Server, reg *session.Registry, parts engineParts, cfg PageConfig, logger shared.Logger) {
	if parts.dispatcher == nil {
		return
	}
	pairs, err := session.NewPairs(session.PairsConfig{Dispatcher: parts.dispatcher, Store: state.NewPairs(cfg.StateDir), Tell: tellSession(reg),
		Changes: files.Reader{}, History: files.Reader{}, Base: base, Logger: logger})
	if err != nil {
		logger.Warn("pairs", "event", "not-started")
	} else {
		pairs.Restore()
		srv.UsePairs(pairs)
	}
	splits, err := session.NewSplits(session.SplitsConfig{Dispatcher: parts.dispatcher, Store: state.NewSplits(cfg.StateDir), Show: reg.SetSplits, Tell: tellSession(reg),
		Base: base, Logger: logger})
	if err != nil {
		logger.Warn("splits", "event", "not-started")
		return
	}
	splits.Restore()
	srv.UseSplits(splits)
}
