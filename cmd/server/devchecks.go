package server

import (
	"github.com/bezmoradi/software-factory/internal/adapters/sandbox/boxes"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const bundledFakeACP = "/usr/local/bin/sf-fake-acp"

func devCheckKeys(devAgent string, dev bool, logger shared.Logger) bool {
	switch {
	case devAgent == "":
		return false
	case !dev:
		logger.Warn("development ACP agent", "event", "check-key-ignored", "why", "the engine gives the development ACP agent a check-only key for /check only in a development image, and this image has no "+boxes.DevMarker)
		return false
	case devAgent != bundledFakeACP:
		logger.Warn("development ACP agent", "event", "check-key-ignored", "why", "the engine gives a check-only key only to the bundled fake agent, "+bundledFakeACP+", and SF_DEV_ACP_AGENT names another command; a copy, a link or a wrapper elsewhere gets none, since the command runs in the session's container, whose files the engine can't see")
		return false
	}
	logger.Warn("development switch: each development ACP session gets a check-only key that answers only /check, from its own container", "variable", session.DevCheckVariable)
	return true
}
