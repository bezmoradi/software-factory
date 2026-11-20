package server

import (
	"context"
	"time"

	"github.com/bezmoradi/software-factory/internal/adapters/git/files"
	"github.com/bezmoradi/software-factory/internal/adapters/git/remote"
	"github.com/bezmoradi/software-factory/internal/adapters/git/state"
	"github.com/bezmoradi/software-factory/internal/adapters/install/npm"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

func packParts(cfg PageConfig, pins *npm.Plugins, repos *session.Repos, logger shared.Logger) (*session.Packs, *npm.Packs, error) {
	if pins == nil || repos == nil {
		return nil, nil, nil
	}
	stored, err := npm.NewPacks(npm.PacksConfig{Dir: cfg.Install.Dir, Plugins: pins, Fetch: remote.Remote{CAFile: cfg.DevGitCA}, Trees: files.Reader{}, Logger: logger})
	if err != nil {
		return nil, nil, err
	}
	packs, err := session.NewPacks(session.PacksConfig{Store: state.NewPacks(cfg.StateDir), Files: stored, Repos: repos, Audit: state.NewAudit(cfg.StateDir), Logger: logger})
	if err != nil {
		return nil, nil, err
	}
	return packs, stored, nil
}

func (p engineParts) packsPage() session.PacksPage {
	if p.packs == nil {
		return nil
	}
	return p.packs
}

func sessionPacks(packs *session.Packs) session.SessionPacks {
	if packs == nil {
		return nil
	}
	return packs
}

func prunePacks(ctx context.Context, parts engineParts, logger shared.Logger) {
	if parts.packs == nil || parts.packFiles == nil {
		return
	}
	if parts.dispatcher != nil && parts.dispatcher.RunsUnread() {
		logger.Warn("pack", "event", "prune-skipped", "why", "the saved sessions couldn't be read, so the engine can't tell which packs they still run")
		return
	}
	keep, err := parts.packs.Kept()
	if parts.dispatcher != nil {
		keep = append(keep, parts.dispatcher.SessionPacks()...)
	}
	if err != nil {
		logger.Warn("pack", "event", "prune-skipped", "why", "packs.json couldn't be read, so the engine can't tell which pack folders projects still use")
		return
	}
	pctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := parts.packFiles.Prune(pctx, keep); err != nil {
		logger.Warn("pack", "event", "prune-failed")
	}
}
