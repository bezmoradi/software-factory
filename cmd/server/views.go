package server

import (
	"github.com/bezmoradi/software-factory/internal/adapters/git/files"
	"github.com/bezmoradi/software-factory/internal/adapters/git/state"
	"github.com/bezmoradi/software-factory/internal/adapters/git/workspace"
	"github.com/bezmoradi/software-factory/internal/adapters/http/page"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

func waitingIn(reg *session.Registry) func(sid string) int {
	return func(sid string) int {
		for _, i := range reg.Sessions() {
			if i.SID == sid {
				return i.Needs
			}
		}
		return 0
	}
}

func useViews(srv *page.Server, parts engineParts, cfg PageConfig, reg *session.Registry, logger shared.Logger) {
	d := parts.dispatcher
	if d == nil {
		return
	}
	audit := state.NewAudit(cfg.StateDir)
	recaps := session.NewRecaps(session.RecapsConfig{Audit: audit, Store: state.NewRecapSeen(cfg.StateDir), Targets: d.PushTarget, Waiting: waitingIn(reg), Logger: logger})
	var ws session.WorkspacePage
	if parts.repos != nil && cfg.Workspace != "" {
		ws = session.NewWorkspace(session.WorkspaceConfig{Dir: cfg.Workspace, Disk: workspace.Disk{}, Sizes: workspace.Sizes{OpenTree: files.OpenTree}, Sessions: d.WorkspaceSessions,
			Heads: files.Reviews{}, History: files.Reader{}, Main: parts.repos.Main, Audit: audit, Remove: d.Remove, Logger: logger})
	}
	ex := session.ExceptionsConfig{Audit: audit}
	if parts.allowlist != nil {
		ex.Egress = parts.allowlist.EgressView
	}
	if parts.repos != nil && parts.routes != nil {
		ex.Repos = parts.repos
		ex.Rules = &session.AllowRules{Host: anyBox{routes: parts.routes}, Repos: parts.repos, Audit: audit}
	}
	srv.UseViews(recaps, ws, session.NewExceptions(ex))
}
