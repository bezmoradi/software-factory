package server

import (
	"context"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/adapters/install/npm"
	"github.com/bezmoradi/software-factory/internal/core/session"
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

func (p engineParts) settle(ctx context.Context, open session.AuditOpen) {
	if p.pushes != nil {
		p.pushes.Reconcile(ctx, open)
	}
	if p.forges != nil {
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
	workflow := "spec-kit"
	if agent == "claude" {
		workflow = "knackpack"
	}
	commit := ""
	for _, pl := range npm.PinnedPlugins {
		if pl.ID == workflow {
			commit = pl.Commit
		}
	}
	return version, commit
}
