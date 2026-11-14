package server

import (
	"errors"
	"net/url"
	"os"

	"github.com/bezmoradi/software-factory/internal/adapters/git/remote"
	"github.com/bezmoradi/software-factory/internal/adapters/git/state"
	"github.com/bezmoradi/software-factory/internal/adapters/http/page"
	"github.com/bezmoradi/software-factory/internal/adapters/install/osv"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

func citedLookup(getenv func(string) string, cfg PageConfig, logger shared.Logger) (session.CitedLookup, error) {
	if getenv("SF_OSV") == "off" {
		return nil, nil
	}
	dev := getenv("SF_DEV_OSV")
	if dev == "" {
		return osv.Cited{Client: osv.NewClient(), API: osv.DefaultAPI}, nil
	}
	u, err := url.Parse(dev)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("SF_DEV_OSV must be an https URL with no user, query or fragment, such as https://gitserver.test:8443/osv")
	}
	client, err := remote.Remote{CAFile: cfg.DevGitCA}.HTTPClient()
	if err != nil {
		return nil, err
	}
	logger.Warn("development switch: the cited vulnerability IDs of reviews are looked up at a fake OSV, not api.osv.dev", "variable", "SF_DEV_OSV")
	return osv.Cited{Client: client, API: dev}, nil
}

func useReviewExtras(srv *page.Server, reg *session.Registry, parts engineParts, cfg PageConfig, logger shared.Logger) {
	if parts.work != nil {
		srv.UseReviewDepth(parts.work)
	}
	if parts.reviews == nil {
		return
	}
	lookup, err := citedLookup(os.Getenv, cfg, logger)
	if err != nil {
		logger.Warn("cited ids", "event", "lookup-off", "why", err.Error())
	}
	if lookup != nil {
		parts.reviews.UseCitedLookup(lookup)
	}
	lc := session.LessonsConfig{Store: state.NewLessons(cfg.StateDir), Reviews: parts.reviews, Sessions: reg.Sessions, Note: reg.NotesFor("lesson"),
		Audit: state.NewAudit(cfg.StateDir), Logger: logger}
	if d := parts.dispatcher; d != nil {
		lc.History = d.HistoryOnly
		lc.Branch = func(sid string) (string, bool) {
			t, ok := d.PushTarget(sid)
			return t.Branch, ok
		}
	}
	lessons, err := session.NewLessons(lc)
	if err != nil {
		logger.Warn("lessons", "event", "not-started")
		return
	}
	srv.UseLessons(lessons)
}
