package server

import (
	"github.com/bezmoradi/software-factory/internal/adapters/git/state"
	"github.com/bezmoradi/software-factory/internal/adapters/http/page"
	"github.com/bezmoradi/software-factory/internal/core/session"
)

func usePushRecords(srv *page.Server, reg *session.Registry, parts engineParts, cfg PageConfig, verifier *session.Verifier) {
	if parts.dispatcher == nil || parts.pushes == nil {
		return
	}
	rc := session.PushRecordsConfig{Targets: parts.dispatcher.PushTarget, Audit: state.NewAudit(cfg.StateDir), Checks: reg.AgentChecks, CardChecks: parts.pushes.CardChecks, Version: Version}
	if parts.reviews != nil {
		rc.Reviews = parts.reviews.Entries
	}
	if parts.work != nil {
		rc.Work = parts.work
	}
	if verifier != nil {
		rc.Verify = verifier.PushedRun
	}
	srv.UsePushRecords(session.NewPushRecords(rc))
}
