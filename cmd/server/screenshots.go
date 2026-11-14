package server

import (
	"context"
	"time"

	"github.com/bezmoradi/software-factory/internal/adapters/agenthost"
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
	host  *agenthost.Client
	sweep *hostSweep
}

func (s hostedScreenshots) Drop(name string) error {
	err := s.Screenshots.Drop(name)
	if err == nil || s.host == nil {
		return err
	}
	s.askHost(name)
	_ = s.Screenshots.Drop(name)
	return s.Gone(name)
}

func (s hostedScreenshots) askHost(name string) {
	parent := context.Background()
	if s.sweep != nil {
		if s.sweep.down {
			return
		}
		parent = s.sweep.ctx
	}
	ctx, cancel := context.WithTimeout(parent, shotsHostTimeout)
	_, err := s.host.RemoveScreenshots(ctx, name)
	cancel()
	if err != nil && s.sweep != nil {
		s.sweep.down = true
		if s.sweep.logger != nil {
			s.sweep.logger.Warn("screenshots", "event", "agent-host-skipped")
		}
	}
}

func useScreenshots(reg *session.Registry, workspace, agentHost string, logger shared.Logger) *hostedScreenshots {
	if workspace == "" {
		return nil
	}
	files := page.Screenshots{Workspace: workspace}
	if err := files.Prepare(); err != nil {
		logger.Warn("screenshots", "event", "top-folder-not-made")
	}
	hosted := hostedScreenshots{Screenshots: files}
	if agentHost != "" {
		if client, err := agenthost.NewClient(agentHost); err == nil {
			hosted.host = client
		}
	}
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
