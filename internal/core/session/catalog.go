package session

import (
	"sync"

	"github.com/bezmoradi/software-factory/internal/core/shared"
	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

type CatalogFile struct {
	Data    []byte
	Missing bool
	Pending bool
	Unread  string
}

type Catalogs struct {
	mu      sync.Mutex
	read    func() CatalogFile
	logger  shared.Logger
	current workflow.Catalog
	loaded  bool
	said    bool
}

func NewCatalogs(read func() CatalogFile, logger shared.Logger) *Catalogs {
	if logger == nil {
		logger = quiet{}
	}
	return &Catalogs{read: read, logger: logger, current: workflow.NoCatalog()}
}

func (c *Catalogs) Load() workflow.Catalog {
	if c == nil || c.read == nil {
		return workflow.NoCatalog()
	}
	f := c.read()
	var got workflow.Catalog
	switch {
	case f.Pending, f.Missing:
		got = workflow.NoCatalog()
	case f.Unread != "":
		got = workflow.RefusedCatalog(f.Unread)
	default:
		got = workflow.ParseCatalog(f.Data)
	}
	c.mu.Lock()
	same := c.said && c.current.State == got.State && c.current.Reason == got.Reason && len(c.current.Agents) == len(got.Agents)
	c.current, c.loaded, c.said = got, !f.Pending, true
	c.mu.Unlock()
	if same {
		return got.Clone()
	}
	switch got.State {
	case workflow.CatalogUsed:
		c.logger.Info("reviewer catalog", "event", "used", "agents", len(got.Agents))
	case workflow.CatalogRefused:
		c.logger.Warn("reviewer catalog", "event", "refused", "reason", got.Reason)
	default:
		c.logger.Info("reviewer catalog", "event", "none", "pending", f.Pending)
	}
	return got.Clone()
}

func (c *Catalogs) Current() workflow.Catalog {
	if c == nil {
		return workflow.NoCatalog()
	}
	c.mu.Lock()
	cur, loaded := c.current, c.loaded
	c.mu.Unlock()
	if !loaded {
		return c.Load()
	}
	return cur.Clone()
}
