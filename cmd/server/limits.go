package server

import (
	"github.com/bezmoradi/software-factory/internal/adapters/git/state"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

func (p engineParts) useLimits(reg *session.Registry, cfg PageConfig, logger shared.Logger) {
	limits := reg.Limits()
	lc := session.LimitsConfig{Store: state.NewCaps(cfg.StateDir), Audit: state.NewAudit(cfg.StateDir), Logger: logger}
	if d := p.dispatcher; d != nil {
		lc.Targets, lc.Stop = d.PushTarget, d.Stop
		lc.Wake = d.WakeAtReset
	}
	if p.reviews != nil {
		lc.Reviewer = p.reviews.SignIn
		p.reviews.OnRateWindows(limits.Windows)
	}
	if p.pushes != nil {
		p.pushes.UseLimits(limits)
		lc.Continued = p.pushes.Continued
	}
	limits.Use(lc)
	if p.repos != nil {
		p.repos.UseProjectData(limits)
	}
}
