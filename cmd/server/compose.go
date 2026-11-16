package server

import (
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

func ApplyComposeProject(getenv func(string) string, logger shared.Logger) {
	v := getenv("SF_COMPOSE_PROJECT")
	if v == "" || session.SetComposeProject(v) {
		return
	}
	logger.Warn("compose project", "event", "invalid", "fix", "SF_COMPOSE_PROJECT must be a Compose project name (lowercase letters, digits, - and _, starting with a letter or digit); the commands the engine shows leave out -p")
}
