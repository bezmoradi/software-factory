package server

import (
	"context"

	"github.com/bezmoradi/software-factory/internal/adapters/git/files"
	"github.com/bezmoradi/software-factory/internal/adapters/git/state"
	"github.com/bezmoradi/software-factory/internal/adapters/git/workspace"
	"github.com/bezmoradi/software-factory/internal/adapters/http/page"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

func useVerify(base context.Context, srv *page.Server, reg *session.Registry, parts engineParts, work *session.SessionWork, facts *session.ProjectFacts, cfg PageConfig, logger shared.Logger) *session.Verifier {
	host, ok := parts.boxes.(session.VerifierHost)
	if !ok || work == nil || parts.dispatcher == nil || cfg.Verify == "" {
		logger.Info("verify", "event", "off")
		return nil
	}
	vc := session.VerifyConfig{
		Host: host, Trees: files.Verify{Dir: cfg.Verify, Volume: workspace.Disk{}.Volume}, Store: state.NewVerifyRuns(cfg.StateDir), Heads: files.Reviews{},
		Targets: parts.dispatcher.PushTarget, Project: work.ProjectOf, Audit: state.NewAudit(cfg.StateDir), Base: base, Logger: logger,
	}
	if parts.work != nil {
		vc.Kinds = parts.work.VerifyKinds
	}
	if parts.repos != nil {
		vc.Mains = parts.repos
	}
	v := session.NewVerifier(reg, vc)
	parts.dispatcher.OnRemoved(v.Forget)
	if parts.repos != nil {
		parts.repos.OnRemoved(v.ForgetMain)
	}
	if facts != nil {
		facts.UseVerified(v.MainLine)
	}
	if parts.pushes != nil {
		parts.pushes.UseVerify(v)
		v.OnDone(parts.pushes.ReviewDone)
		v.UseReady(parts.pushes.VerifyReady)
		v.OnCommitDone(parts.pushes.CommitSettled)
	}
	go v.Watch(base)
	srv.UseVerify(v)
	return v
}
