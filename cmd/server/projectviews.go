package server

import (
	"github.com/bezmoradi/software-factory/internal/adapters/git/state"
	"github.com/bezmoradi/software-factory/internal/adapters/http/page"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

func checkCommandOf(dir string, repos *session.Repos) func(project string) string {
	store := state.NewProjectCommands(dir)
	return func(project string) string {
		list, err := repos.List()
		if err != nil {
			return ""
		}
		for _, r := range list {
			if r.Name != project || r.URL == "" {
				continue
			}
			all, err := store.Load()
			if err != nil {
				return ""
			}
			return all[session.ProjectID{Name: r.Name, URL: r.URL}].Check
		}
		return ""
	}
}

func useProjectViews(srv *page.Server, parts engineParts, cfg PageConfig, reg *session.Registry, logger shared.Logger) {
	if parts.repos == nil {
		return
	}
	audit := state.NewAudit(cfg.StateDir)
	follows := state.NewPRFollows(cfg.StateDir)
	srv.UseMetrics(session.NewMetrics(session.MetricsConfig{Repos: parts.repos, Audit: audit, Reviews: state.Reviews{Dir: cfg.StateDir}.Load,
		Imported: state.ImportedReviews(cfg.StateDir), Runs: state.NewSessions(cfg.StateDir).Load, Caps: state.NewCaps(cfg.StateDir).Load, Follows: follows.Load}))
	decisions := session.NewDecisionLog(session.DecisionLogConfig{Store: state.NewDecisionLog(cfg.StateDir), Audit: audit, Writer: audit, Follows: follows.Load,
		Repos: parts.repos, Note: reg.NoteFor(session.DecisionNoteSource), Logger: logger})
	parts.repos.UseProjectData(decisions)
	srv.UseDecisionLog(decisions)
	if parts.dispatcher != nil {
		parts.dispatcher.UseDecisionLog(decisions)
	}
}
