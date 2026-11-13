package server

import (
	"fmt"
	"path/filepath"

	"github.com/bezmoradi/software-factory/internal/adapters/http/page"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

func useUploads(reg *session.Registry, getenv func(string) string, logger shared.Logger) error {
	dir := getenv("SF_UPLOADS")
	if dir == "" {
		logger.Info("uploads", "event", "off")
		return nil
	}
	agent := getenv("SF_AGENT_UPLOADS")
	if agent == "" {
		agent = dir
	}
	if !filepath.IsAbs(dir) || !filepath.IsAbs(agent) {
		return fmt.Errorf("SF_UPLOADS and SF_AGENT_UPLOADS must be absolute paths")
	}
	files, err := page.NewUploadDir(dir, agent)
	if err != nil {
		return err
	}
	reg.UseUploads(files)
	return nil
}
