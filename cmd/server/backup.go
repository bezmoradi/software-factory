package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/adapters/git/state"
	"github.com/bezmoradi/software-factory/internal/adapters/http/page"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

type restartHook struct {
	mu   sync.Mutex
	stop func()
}

func (h *restartHook) set(stop func()) {
	h.mu.Lock()
	h.stop = stop
	h.mu.Unlock()
}

func (h *restartHook) restart() {
	h.mu.Lock()
	stop := h.stop
	h.mu.Unlock()
	if stop != nil {
		stop()
	}
}

type stateStart struct {
	applied *session.ImportReport
}

func startState(cfg PageConfig, getenv func(string) string, logger shared.Logger) (stateStart, error) {
	var out stateStart
	if v := getenv("SF_DEV_STATE_VERSION"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return out, fmt.Errorf("SF_DEV_STATE_VERSION must be 2, got %q", v)
		}
		if err := state.DevStoreVersion(n); err != nil {
			return out, err
		}
		logger.Warn("development store version switched on; that store is written in a format older engines don't read", "variable", "SF_DEV_STATE_VERSION", "store", state.DevVersioned)
	}
	if v := getenv("SF_DEV_AUDIT_ROTATE"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return out, fmt.Errorf("SF_DEV_AUDIT_ROTATE must be a number of bytes, got %q", v)
		}
		if err := state.DevAuditLimit(n); err != nil {
			return out, err
		}
		logger.Warn("development audit rotation size switched on", "variable", "SF_DEV_AUDIT_ROTATE", "bytes", n)
	}
	before, err := state.StampEngine(cfg.StateDir, Version)
	if err != nil {
		return out, fmt.Errorf("state folder: %w", err)
	}
	if before != "" && before != Version {
		logger.Info("engine version changed", "before", before, "now", Version)
	}
	if _, err := state.LocalPerson(cfg.StateDir); err != nil {
		logger.Warn("local person", "event", "unreadable", "why", "person-json")
	}
	report, ok, err := state.ApplyImport(cfg.StateDir, time.Now())
	if err != nil {
		logger.Warn("backup", "event", "import-not-finished")
	}
	if ok {
		out.applied = &report
	}
	stamped, err := state.StampOwners(cfg.StateDir)
	if err != nil {
		logger.Warn("local person", "event", "owners-not-written", "why", "store-unreadable")
	}
	if len(stamped) > 0 {
		logger.Info("local person", "event", "owners-written", "stores", len(stamped))
	}
	return out, nil
}

func useBackups(srv *page.Server, parts engineParts, cfg PageConfig, start stateStart, hook *restartHook, logger shared.Logger) {
	bc := session.BackupsConfig{
		Archive: state.Archive{Dir: cfg.StateDir, Workspace: cfg.Workspace, Version: Version},
		Repos:   parts.repos,
		Audit:   state.NewAudit(cfg.StateDir),
		Restart: hook.restart,
		Logger:  logger,
	}
	if parts.dispatcher != nil {
		bc.Sessions = parts.dispatcher.SessionCount
	}
	backups := session.NewBackups(bc)
	if start.applied != nil {
		backups.Applied(*start.applied)
	}
	srv.UseBackups(backups)
	if parts.dispatcher == nil || parts.reviews == nil {
		return
	}
	branches := parts.dispatcher.HistoryBranches()
	if len(branches) == 0 {
		return
	}
	shown := filepath.Join(cfg.StateDir, "imported")
	if _, err := os.Lstat(shown); err != nil {
		return
	}
	list, err := state.Reviews{Dir: shown}.Load()
	if err != nil {
		logger.Warn("backup", "event", "imported-reviews-not-read")
		return
	}
	if n := parts.reviews.ShowHistory(branches, list); n > 0 {
		logger.Info("backup", "event", "history-shown", "sessions", n)
	}
}
