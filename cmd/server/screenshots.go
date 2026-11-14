package server

import (
	"context"
	"time"

	"github.com/bezmoradi/software-factory/internal/adapters/agenthost"
	"github.com/bezmoradi/software-factory/internal/adapters/git/files"
	"github.com/bezmoradi/software-factory/internal/adapters/http/page"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	shotsHostTimeout = 5 * time.Second
	shotsSweepLimit  = 30 * time.Second
)

type hostSweep struct {
	ctx    context.Context
	down   bool
	logger shared.Logger
}

type hostedScreenshots struct {
	page.Screenshots
	routes *boxRoutes
	sweep  *hostSweep
}

type shotRemover interface {
	RemoveScreenshots(ctx context.Context, name string) (bool, error)
}

func (s hostedScreenshots) Drop(box, name string) error {
	err := s.Screenshots.Drop(box, name)
	if err == nil || s.routes == nil {
		return err
	}
	s.askHost(box, name)
	_ = s.Screenshots.Drop(box, name)
	return s.Gone(box, name)
}

func (s hostedScreenshots) askHost(box, name string) {
	h, ok := s.routes.Host(box)
	if !ok {
		return
	}
	var remover shotRemover
	switch v := h.(type) {
	case *agenthost.Client:
		remover = v
	case gatewayHost:
		remover = v.Client
	default:
		return
	}
	parent := context.Background()
	if s.sweep != nil {
		if s.sweep.down {
			return
		}
		parent = s.sweep.ctx
	}
	ctx, cancel := context.WithTimeout(parent, shotsHostTimeout)
	_, err := remover.RemoveScreenshots(ctx, name)
	cancel()
	if err != nil && s.sweep != nil {
		s.sweep.down = true
		if s.sweep.logger != nil {
			s.sweep.logger.Warn("screenshots", "event", "agent-host-skipped")
		}
	}
}

func useScreenshots(reg *session.Registry, workspace string, routes *boxRoutes, logger shared.Logger) *hostedScreenshots {
	if workspace == "" {
		return nil
	}
	hosted := hostedScreenshots{Workspace: workspace, OpenBox: files.OpenBox, routes: routes}
	reg.UseScreenshots(hosted, logger)
	return &hosted
}

func dropStrayScreenshots(ctx context.Context, d *session.Dispatcher, files *hostedScreenshots, logger shared.Logger) {
	if d == nil || files == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, shotsSweepLimit)
	defer cancel()
	sweeping := *files
	sweeping.sweep = &hostSweep{ctx: ctx, logger: logger}
	if n := d.DropStrayScreenshots(ctx, sweeping); n > 0 {
		logger.Info("screenshots", "event", "stray-removed", "entries", n)
	}
}
