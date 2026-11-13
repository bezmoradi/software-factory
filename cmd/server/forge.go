package server

import (
	"context"

	"github.com/bezmoradi/software-factory/internal/adapters/git/github"
	"github.com/bezmoradi/software-factory/internal/adapters/git/remote"
	"github.com/bezmoradi/software-factory/internal/adapters/git/state"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

func forges(base context.Context, cfg PageConfig, reg *session.Registry, d *session.Dispatcher, repos *session.Repos, work *session.Work, logger shared.Logger) (*session.Forges, error) {
	if repos == nil || d == nil {
		return nil, nil
	}
	client, err := remote.Remote{CAFile: cfg.DevGitCA}.HTTPClient()
	if err != nil {
		return nil, err
	}
	fc := session.ForgesConfig{
		API: github.API{Client: client}, Repos: repos, Targets: d.PushTarget, Show: reg.SetForge, Note: reg.NoteFor("forge"),
		Audit: state.NewAudit(cfg.StateDir), Base: base, Logger: logger, Version: Version,
	}
	if work != nil {
		fc.PRComments = func(name string) bool { return work.Project(name).PRComment }
	}
	return session.NewForges(fc)
}

func commentAfterPush(pushes *session.Pushes, forged *session.Forges) {
	if pushes != nil && forged != nil {
		pushes.OnPushed(forged.AfterPush)
	}
}

func (p engineParts) forging() session.ForgeService {
	if p.forges == nil {
		return nil
	}
	return p.forges
}
