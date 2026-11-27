package server

import (
	"context"

	"github.com/bezmoradi/software-factory/internal/adapters/git/files"
	"github.com/bezmoradi/software-factory/internal/adapters/git/state"
	"github.com/bezmoradi/software-factory/internal/adapters/http/page"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

func useSessionWork(base context.Context, srv *page.Server, reg *session.Registry, parts engineParts, cfg PageConfig, logger shared.Logger) *session.SessionWork {
	d := parts.dispatcher
	if d == nil || cfg.Helper == "" {
		return nil
	}
	wc := session.SessionWorkConfig{
		Commands: state.NewProjectCommands(cfg.StateDir), Queues: state.NewQueues(cfg.StateDir),
		Ports: state.NewPorts(cfg.StateDir), Models: state.NewModels(cfg.StateDir),
		Audit: state.NewAudit(cfg.StateDir), Note: reg.NoteFor("check"), PortNote: reg.NoteFor("ports"), Base: base, Logger: logger,
		HelperCount: func(box string) (int, error) { return files.HelperCount(cfg.Workspace, box) },
	}
	if parts.repos != nil {
		wc.Repos = parts.repos
	}
	w := session.NewSessionWork(reg, d, wc)
	if parts.repos != nil {
		parts.repos.OnRemoved(w.ForgetProject)
	}
	if !d.RunsUnread() {
		w.PrunePorts(func(sid string) bool {
			_, ok := d.PushTarget(sid)
			return ok || d.SkipDoubled(sid, "ports-prune")
		})
	}
	if parts.reviews != nil {
		parts.reviews.UseRoleModels(w.RoleModel)
	}
	srv.UseSessionWork(w)
	return w
}
