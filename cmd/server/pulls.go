package server

import (
	"context"

	"github.com/bezmoradi/software-factory/internal/adapters/git/github"
	"github.com/bezmoradi/software-factory/internal/adapters/git/remote"
	"github.com/bezmoradi/software-factory/internal/adapters/git/state"
	"github.com/bezmoradi/software-factory/internal/adapters/http/page"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

func approvedRequirements(work *session.Work) func(sid string) string {
	return func(sid string) string {
		v, err := work.View(sid)
		if err != nil || v.Requirements == nil || !v.Requirements.Approved {
			return ""
		}
		return v.Requirements.Text
	}
}

func usePulls(base context.Context, srv *page.Server, reg *session.Registry, parts engineParts, cfg PageConfig, logger shared.Logger) {
	if parts.forges == nil || parts.dispatcher == nil || parts.repos == nil {
		return
	}
	client, err := remote.Remote{CAFile: cfg.DevGitCA}.HTTPClient()
	if err != nil {
		logger.Warn("pull follow", "event", "no-http-client")
		return
	}
	fc := session.FollowsConfig{
		API: github.API{Client: client, Expiry: reg.Limits().TokenExpiry, Rates: github.NewRates()}, Forges: parts.forges, Store: state.NewPRFollows(cfg.StateDir),
		Tell: tellSession(reg), SignIn: parts.dispatcher.SessionSignIn, Sessions: reg.Sessions, Show: reg.SetFollow, Note: reg.NotesFor("follow"),
		Audit: state.NewAudit(cfg.StateDir), MainCommit: parts.repos.Main, Logger: logger,
	}
	if parts.work != nil {
		fc.Approved = approvedRequirements(parts.work)
	}
	if parts.reviews != nil {
		fc.Reviews = parts.reviews.Entries
	}
	follows, err := session.NewFollows(fc)
	if err != nil {
		logger.Warn("pull follow", "event", "not-started")
		return
	}
	parts.repos.OnRemoved(follows.ForgetProject)
	follows.Restore()
	go follows.Run(base)
	issues, err := session.NewIssueStarts(session.IssueStartsConfig{Follows: follows, Projects: parts.dispatcher.Projects, Tell: tellSession(reg), Base: base, Logger: logger})
	if err != nil {
		srv.UsePulls(follows, nil, nil)
		return
	}
	srv.UsePulls(follows, issues, session.IssueDispatcher{Dispatcher: parts.dispatcher, Issues: issues})
}
