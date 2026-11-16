package server

import (
	"path/filepath"

	"github.com/bezmoradi/software-factory/internal/adapters/git/files"
	"github.com/bezmoradi/software-factory/internal/adapters/git/keep"
	"github.com/bezmoradi/software-factory/internal/adapters/git/state"
	"github.com/bezmoradi/software-factory/internal/adapters/http/page"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

func useKeep(srv *page.Server, reg *session.Registry, parts engineParts, cfg PageConfig, logger shared.Logger) {
	d := parts.dispatcher
	if d == nil || parts.repos == nil || cfg.Workspace == "" || cfg.Helper == "" {
		return
	}
	k := session.NewKeep(reg, d, session.KeepConfig{
		Store:  keep.Store{Dir: filepath.Join(cfg.StateDir, state.KeepFolder), Workspace: cfg.Workspace, OpenTree: files.OpenTree},
		Reader: files.Reader{}, Heads: files.Reviews{}, Main: parts.repos.Main,
		Audit: state.NewAudit(cfg.StateDir), Logger: logger,
	})
	k.Prune()
	srv.UseKeep(k)
}
