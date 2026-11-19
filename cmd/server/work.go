package server

import (
	"strconv"
	"strings"

	"github.com/bezmoradi/software-factory/internal/adapters/git/files"
	"github.com/bezmoradi/software-factory/internal/adapters/git/state"
	"github.com/bezmoradi/software-factory/internal/adapters/install/npm"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

func workflowParts(cfg PageConfig, docs bool, reg *session.Registry, repos *session.Repos, reviews *session.Reviews, pins *npm.Plugins, catalogs *session.Catalogs, targets func(string) (session.PushTarget, bool), agentFiles *session.ReviewerAgents, logger shared.Logger) (*session.Work, error) {
	if repos == nil {
		return nil, nil
	}
	wc := session.WorkConfig{
		Targets: targets, Heads: files.Reviews{}, History: files.Reader{}, Files: files.Reader{}, Bounded: files.Reader{}, Blobs: files.Reader{}, Repos: repos,
		Reported: reg.Workflow, Store: state.NewWork(cfg.StateDir), Logger: logger,
		Contents: files.Reader{}, Audit: state.NewAudit(cfg.StateDir), Tell: tellSession(reg), DocsInRepo: docs,
		Changes: files.Reader{}, Level: reg.PushLevel,
	}
	if reviews != nil {
		wc.Reviews = reviews
		wc.ApprovalMoved = reviews.ApprovalMoved
	}
	if pins != nil {
		wc.Installed = func() []string { return pins.Agents("knackpack") }
		wc.Catalog = catalogs.Current
	}
	if agentFiles != nil {
		wc.RoleAgents = agentFiles
	}
	w, err := session.NewWork(wc)
	if err == nil {
		repos.UseProjectData(w)
	}
	if err == nil && reviews != nil {
		reg.OnDocsReported(w.RecheckApproval)
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

func knackpackDir(pins *npm.Plugins) func() (string, error) {
	if pins == nil {
		return nil
	}
	return func() (string, error) { return pins.PluginPath("knackpack") }
}

func tellSession(reg *session.Registry) func(sid, text string) error {
	return func(sid, text string) error { return reg.Message(sid, text, false, nil, nil) }
}

func (p engineParts) auditPage(cfg PageConfig) session.AuditPage {
	if p.dispatcher == nil {
		return nil
	}
	return session.NewAuditLog(state.NewAudit(cfg.StateDir), p.dispatcher.PushTarget)
}

func (p engineParts) workPage() session.WorkPage {
	if p.work == nil {
		return nil
	}
	return p.work
}

const docsFrom = "0.26.0"

func versionParts(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || strconv.Itoa(n) != p {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

func atLeast(v, floor string) bool {
	a, okA := versionParts(v)
	b, okB := versionParts(floor)
	if !okA || !okB {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return true
}

func pinnedKnackpack() string {
	for _, p := range npm.PinnedPlugins {
		if p.ID == "knackpack" {
			return p.Version
		}
	}
	return ""
}

func docsInRepo() bool {
	return atLeast(pinnedKnackpack(), docsFrom)
}

func (p engineParts) identities() session.IdentityPage {
	if p.identity == nil {
		return nil
	}
	return p.identity
}
