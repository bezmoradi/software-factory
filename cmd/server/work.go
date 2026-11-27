package server

import (
	"slices"

	"github.com/bezmoradi/software-factory/internal/adapters/git/files"
	"github.com/bezmoradi/software-factory/internal/adapters/git/state"
	"github.com/bezmoradi/software-factory/internal/adapters/install/npm"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

func workflowParts(cfg PageConfig, reg *session.Registry, repos *session.Repos, reviews *session.Reviews, pins *npm.Plugins, packs *session.Packs, catalogs *session.Catalogs, targets func(string) (session.PushTarget, bool), doubled func(string) bool, held func(string) (session.PushTarget, bool), history func(string) error, agentFiles *session.ReviewerAgents, logger shared.Logger) (*session.Work, error) {
	if repos == nil {
		return nil, nil
	}
	wc := session.WorkConfig{
		Targets: targets, Doubled: doubled, Held: held, Heads: files.Reviews{}, History: files.Reader{}, Files: files.Reader{}, Bounded: files.Reader{}, Blobs: files.Reader{}, Repos: repos,
		Reported: reg.Workflow, Store: state.NewWork(cfg.StateDir), Logger: logger,
		Contents: files.Reader{}, Audit: state.NewAudit(cfg.StateDir), Tell: tellSession(reg), Note: reg.NoteFor("work-type"), HistoryOnly: history, Texts: state.NewApprovedTexts(cfg.StateDir),
		Changes: files.Reader{}, Level: reg.PushLevel,
	}
	if reviews != nil {
		wc.Reviews = reviews
		wc.ApprovalMoved = reviews.ApprovalMoved
		wc.NothingToExplain = reviews.NothingToExplain
	}
	if pins != nil {
		wc.Installed = func() []string { return pins.Agents("knackpack") }
		wc.Catalog = catalogs.Current
	}
	if agentFiles != nil {
		wc.RoleAgents = agentFiles
	}
	if packs != nil {
		wc.Packs = packs
	}
	w, err := session.NewWork(wc)
	if err == nil {
		repos.UseProjectData(w)
		reg.UseFirstSteps(w)
	}
	if err == nil {
		reg.OnDocsCheck(w.DocsCheck)
	}
	return w, err
}

func knackpackCatalog(pins *npm.Plugins, logger shared.Logger) *session.Catalogs {
	if pins == nil {
		return nil
	}
	c := session.NewCatalogs(func() session.CatalogFile {
		return pins.Catalog("knackpack", workflow.CatalogFile, workflow.MaxCatalogBytes)
	}, logger)
	c.Load()
	return c
}

func knackpackDir(pins *npm.Plugins) func() (string, string, error) {
	if pins == nil {
		return nil
	}
	return func() (string, string, error) { return pins.PluginAt(npm.Knackpack.ID) }
}

func knackpackRefused(pins *npm.Plugins) func(string) bool {
	return func(commit string) bool {
		return slices.Contains(npm.Knackpack.Refused, commit) || pins != nil && pins.Unusable(npm.Knackpack.ID, commit)
	}
}

func tellSession(reg *session.Registry) func(sid, text string) error {
	return func(sid, text string) error { return reg.Message(sid, text, false, nil, nil) }
}

func typedSession(reg *session.Registry) func(sid, text string) error {
	return func(sid, text string) error { return reg.PersonMessage(sid, text, false, nil, nil) }
}

func (p engineParts) auditPage(cfg PageConfig) session.AuditPage {
	if p.dispatcher == nil {
		return nil
	}
	return session.NewAuditLog(state.NewAudit(cfg.StateDir), p.dispatcher.PushTarget, p.dispatcher.HeldTarget)
}

func (p engineParts) workPage() session.WorkPage {
	if p.work == nil {
		return nil
	}
	return p.work
}

func (p engineParts) identities() session.IdentityPage {
	if p.identity == nil {
		return nil
	}
	return p.identity
}
