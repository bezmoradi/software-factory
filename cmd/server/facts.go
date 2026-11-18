package server

import (
	"context"

	"github.com/bezmoradi/software-factory/internal/adapters/git/files"
	"github.com/bezmoradi/software-factory/internal/adapters/git/github"
	"github.com/bezmoradi/software-factory/internal/adapters/git/remote"
	"github.com/bezmoradi/software-factory/internal/adapters/git/state"
	"github.com/bezmoradi/software-factory/internal/adapters/http/page"
	"github.com/bezmoradi/software-factory/internal/adapters/install/registry"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

func useChangeFacts(base context.Context, srv *page.Server, reg *session.Registry, parts engineParts, cfg PageConfig, logger shared.Logger) {
	if parts.dispatcher == nil || parts.repos == nil {
		return
	}
	reg.WatchWorktrees(session.WorktreeWatch{Targets: parts.dispatcher.PushTarget, Heads: files.Reviews{}, Reader: files.Worktrees{Workspace: cfg.Workspace},
		Commits: files.Reader{}, History: files.Reader{}, Main: parts.repos.Main})
	var locks session.TestLocks
	if parts.work != nil {
		locks = parts.work
	}
	if parts.reviews != nil {
		parts.reviews.UseChangeFacts(files.Reader{}, locks)
	}
	var reader session.ProtectionReader
	if client, err := (remote.Remote{CAFile: cfg.DevGitCA}).HTTPClient(); err == nil {
		reader = github.API{Client: client}
	} else {
		logger.Warn("project facts", "event", "no-http-client")
	}
	facts := session.NewProjectFacts(session.ProjectFactsConfig{Repos: parts.repos, Workflows: files.Reader{}, Protection: reader,
		Lookups: state.NewPackageLookups(cfg.StateDir), Files: files.Reader{}, Check: checkCommandOf(cfg.StateDir, parts.repos), Audit: state.NewAudit(cfg.StateDir), Logger: logger})
	parts.repos.UseProjectData(facts)
	srv.UseProjectFacts(facts)
	if parts.pushes != nil {
		parts.pushes.UseExtras(session.PushExtras{Files: files.Reader{}, Workflows: files.Reader{}, Registry: &registry.Registry{Client: registry.NewClient()},
			LookupsOn: facts.LookupsOn, Locks: locks, Base: base})
		if parts.work != nil {
			parts.work.OnLockMoved(parts.pushes.LockMoved)
		}
	}
}
