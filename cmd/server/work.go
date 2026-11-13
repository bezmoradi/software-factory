package server

import (
	"github.com/bezmoradi/software-factory/internal/adapters/git/files"
	"github.com/bezmoradi/software-factory/internal/adapters/git/state"
	"github.com/bezmoradi/software-factory/internal/adapters/install/npm"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

func workflowParts(cfg PageConfig, reg *session.Registry, repos *session.Repos, reviews *session.Reviews, pins *npm.Plugins, targets func(string) (session.PushTarget, bool), logger shared.Logger) (*session.Work, error) {
	if repos == nil {
		return nil, nil
	}
	wc := session.WorkConfig{
		Targets: targets, Heads: files.Reviews{}, History: files.Reader{}, Files: files.Reader{}, Repos: repos,
		Reported: reg.Workflow, Store: state.NewWork(cfg.StateDir), Logger: logger,
		Contents: files.Reader{}, Audit: state.NewAudit(cfg.StateDir), Tell: tellSession(reg),
	}
	if reviews != nil {
		wc.Reviews = reviews
	}
	if pins != nil {
		wc.Installed = func() []string { return pins.Agents("knackpack") }
	}
	return session.NewWork(wc)
}

func knackpackDir(pins *npm.Plugins) func() (string, error) {
	if pins == nil {
		return nil
	}
	return func() (string, error) { return pins.PluginPath("knackpack") }
}

func tellSession(reg *session.Registry) func(sid, text string) error {
	return func(sid, text string) error { return reg.Message(sid, text, false, nil, nil) }
}

func (p engineParts) auditPage(cfg PageConfig) session.AuditPage {
	if p.dispatcher == nil {
		return nil
	}
	return session.NewAuditLog(state.NewAudit(cfg.StateDir), p.dispatcher.PushTarget)
}

func (p engineParts) workPage() session.WorkPage {
	if p.work == nil {
		return nil
	}
	return p.work
}
