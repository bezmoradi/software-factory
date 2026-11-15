package server

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/bezmoradi/software-factory/internal/adapters/git/files"
	"github.com/bezmoradi/software-factory/internal/adapters/git/state"
	"github.com/bezmoradi/software-factory/internal/adapters/install/npm"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

func workflowParts(cfg PageConfig, docs bool, reg *session.Registry, repos *session.Repos, reviews *session.Reviews, pins *npm.Plugins, targets func(string) (session.PushTarget, bool), agentFiles *session.ReviewerAgents, logger shared.Logger) (*session.Work, error) {
	if repos == nil {
		return nil, nil
	}
	wc := session.WorkConfig{
		Targets: targets, Heads: files.Reviews{}, History: files.Reader{}, Files: files.Reader{}, Repos: repos,
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
	}
	if agentFiles != nil {
		wc.RoleAgents = agentFiles
	}
	w, err := session.NewWork(wc)
	if err == nil && reviews != nil {
		reg.OnDocsReported(w.RecheckApproval)
	}
	return w, err
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

func parseDocs(cfg *PageConfig, getenv func(string) string) error {
	switch v := getenv("SF_DEV_KNACKPACK_DOCS"); v {
	case "":
	case "repo":
		if cfg.DevACPAgent == "" {
			return errors.New("SF_DEV_KNACKPACK_DOCS is only for the development overlay, which also sets SF_DEV_ACP_AGENT, so this engine doesn't start: remove SF_DEV_KNACKPACK_DOCS from the engine's environment")
		}
		cfg.DevDocs = true
	default:
		return fmt.Errorf("SF_DEV_KNACKPACK_DOCS must be repo or unset, got %q", v)
	}
	return nil
}

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

func docsInRepo(cfg PageConfig, logger shared.Logger) bool {
	if cfg.DevDocs {
		logger.Warn("development switch: knackpack's requirements and plan count only as committed, and the development ACP agent takes knackpack's workflow", "variable", "SF_DEV_KNACKPACK_DOCS")
		return true
	}
	return atLeast(pinnedKnackpack(), docsFrom)
}

func (p engineParts) identities() session.IdentityPage {
	if p.identity == nil {
		return nil
	}
	return p.identity
}
