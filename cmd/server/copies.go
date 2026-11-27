package server

import (
	"context"
	"net/netip"

	"github.com/bezmoradi/software-factory/internal/adapters/git/files"
	"github.com/bezmoradi/software-factory/internal/adapters/git/remote"
	"github.com/bezmoradi/software-factory/internal/adapters/git/state"
	"github.com/bezmoradi/software-factory/internal/adapters/http/page"
	"github.com/bezmoradi/software-factory/internal/adapters/sandbox/boxes"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const devGitHost = "gitserver.test"

func copyPrivateHosts(cfg PageConfig, dev bool, logger shared.Logger) []string {
	if cfg.DevGitCA == "" {
		return nil
	}
	if !dev {
		logger.Warn("copies", "event", "dev-host-ignored", "why", "the engine lets "+devGitHost+" resolve to a private address for read-only copies only in a development image, and this image has no "+boxes.DevMarker)
		return nil
	}
	logger.Warn("copies", "event", "dev-host-private", "host", devGitHost, "why", "SF_DEV_GIT_CA is set in a development image, so read-only copies may come from the dev git host on its private network")
	return []string{devGitHost}
}

func useCopies(base context.Context, srv *page.Server, reg *session.Registry, parts engineParts, cfg PageConfig, logger shared.Logger) {
	if parts.dispatcher == nil || cfg.Workspace == "" {
		return
	}
	own := func() []netip.Prefix { return nil }
	if parts.routes != nil {
		own = parts.routes.ownNetworks
	}
	fetcher := remote.Copies{Workspace: cfg.Workspace, CAFile: cfg.DevGitCA, Trees: files.Reader{}, Own: own,
		Private: copyPrivateHosts(cfg, developmentImage(boxes.DevMarker), logger)}
	fetcher.Sweep()
	known := parts.dispatcher.Known
	if parts.dispatcher.RunsUnread() {
		known = nil
	}
	copies, err := session.NewCopies(session.CopiesConfig{Store: state.NewCopies(cfg.StateDir), Files: fetcher, Tokens: state.NewTokens(cfg.StateDir),
		Box: parts.dispatcher.SessionBox, Known: known, Show: reg.SetCopies, Note: reg.NotesFor("copy"),
		Base: base, Logger: logger})
	if err != nil {
		logger.Warn("copies", "event", "not-started")
		return
	}
	copies.Restore()
	parts.dispatcher.OnRemoved(copies.Forget)
	srv.UseCopies(copies)
}
