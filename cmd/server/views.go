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

func newWorkspace(parts engineParts, cfg PageConfig, logger shared.Logger) *session.Workspace {
	d := parts.dispatcher
	if d == nil || parts.repos == nil || cfg.Workspace == "" {
		return nil
	}
	return session.NewWorkspace(session.WorkspaceConfig{Dir: cfg.Workspace, Disk: workspace.Disk{}, Sizes: workspace.Sizes{OpenTree: files.OpenTree}, Sessions: d.WorkspaceSessions,
		Heads: files.Reviews{}, History: files.Reader{}, Main: parts.repos.Main, Audit: state.NewAudit(cfg.StateDir), Remove: d.Remove, Logger: logger})
}

func useViews(srv *page.Server, parts engineParts, cfg PageConfig, reg *session.Registry, w *session.Workspace, logger shared.Logger) {
	d := parts.dispatcher
	if d == nil {
		return
	}
	audit := state.NewAudit(cfg.StateDir)
	recaps := session.NewRecaps(session.RecapsConfig{Audit: audit, Store: state.NewRecapSeen(cfg.StateDir), Targets: d.PushTarget, Doubled: d.Doubled, Held: d.HeldTarget, Waiting: waitingIn(reg), Logger: logger})
	var ws session.WorkspacePage
	if w != nil {
		d.UseBranchState(w.BranchState)
		ws = w
	}
	ex := session.ExceptionsConfig{Audit: audit}
	if parts.allowlist != nil {
		ex.Egress = parts.allowlist.EgressView
	}
	if parts.repos != nil && parts.routes != nil {
		ex.Repos = parts.repos
		ex.Rules = &session.AllowRules{Hosts: d.RuleHosts, Repos: parts.repos, Audit: audit}
	}
	srv.UseViews(recaps, ws, session.NewExceptions(ex))
	vc := session.ViewedConfig{Store: state.NewViewed(cfg.StateDir), Targets: d.PushTarget, Doubled: d.Doubled, Heads: files.Reviews{}, History: files.Reader{}, Files: files.Reader{}, Logger: logger}
	if parts.repos != nil {
		vc.Main = parts.repos.Main
	}
	srv.UseViewed(session.NewViewed(vc))
}
