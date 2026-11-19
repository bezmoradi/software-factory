package server

import (
	"context"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/adapters/git/state"
	"github.com/bezmoradi/software-factory/internal/adapters/install/npm"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	pushGrace  = 25 * time.Second
	sendsAfter = 2 * time.Second
)

func (p engineParts) closeSends() {
	if p.dispatcher != nil {
		p.dispatcher.MarkStopping()
	}
	if p.pushes != nil {
		p.pushes.Close()
	}
	if p.forges != nil {
		p.forges.Close()
	}
}

func (p engineParts) waitSends(limit time.Duration) {
	var wg sync.WaitGroup
	if p.pushes != nil {
		wg.Go(p.pushes.Wait)
	}
	if p.forges != nil {
		wg.Go(p.forges.Wait)
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(limit):
	}
}

func (p engineParts) useHalt(reg *session.Registry, cfg PageConfig, logger shared.Logger) {
	hc := session.HaltConfig{Store: state.NewHalt(cfg.StateDir), Audit: state.NewAudit(cfg.StateDir), Logger: logger}
	if p.dispatcher != nil {
		hc.Targets, hc.Stopper = p.dispatcher.PushTarget, p.dispatcher
	}
	if p.pushes != nil {
		p.pushes.UseHalt(reg.Halt())
		hc.Resume = p.pushes.Resume
	}
	reg.Halt().Use(hc)
}

func (p engineParts) settle(ctx context.Context, open session.AuditOpen) {
	if p.pushes != nil {
		p.pushes.Reconcile(ctx, open)
	}
	if p.forges != nil {
		p.forges.Restore(open)
		p.forges.Reconcile(ctx, open)
	}
	if p.pushes != nil {
		p.pushes.Resume(ctx)
	}
}

func agentVersions(agent string) (string, string) {
	version := ""
	for _, a := range npm.Pinned {
		if a.ID == agent {
			version = a.Version
		}
	}
	commit := ""
	for _, pl := range npm.PinnedPlugins {
		if pl.ID == "knackpack" {
			commit = pl.Commit
		}
	}
	return version, commit
}
