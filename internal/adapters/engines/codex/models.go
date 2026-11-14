package codex

import (
	"context"
	"encoding/json"
	"io"
	"slices"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	modelPages   = 5
	modelPageMax = 100
	modelListFor = 10 * time.Second
)

var (
	_ session.ModelReporter      = (*Client)(nil)
	_ session.TurnSettingSession = (*Client)(nil)
	_ session.ModelLister        = Lister{}
)

type modelListParams struct {
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit"`
}

type modelEffortJSON struct {
	ReasoningEffort string `json:"reasoningEffort"`
}

type modelJSON struct {
	ID                        string            `json:"id"`
	Model                     string            `json:"model"`
	DisplayName               string            `json:"displayName"`
	Hidden                    bool              `json:"hidden"`
	SupportedReasoningEfforts []modelEffortJSON `json:"supportedReasoningEfforts"`
	DefaultReasoningEffort    string            `json:"defaultReasoningEffort"`
	IsDefault                 bool              `json:"isDefault"`
}

type modelListReply struct {
	Data       []modelJSON `json:"data"`
	NextCursor *string     `json:"nextCursor"`
}

func (c *Client) listModels(ctx context.Context) ([]session.AgentModel, error) {
	var out []session.AgentModel
	cursor := ""
	for range modelPages {
		raw, err := c.call(ctx, methodModelList, modelListParams{Cursor: cursor, Limit: modelPageMax})
		if err != nil {
			return out, err
		}
		var page modelListReply
		if json.Unmarshal(raw, &page) != nil {
			return out, ErrProtocol
		}
		for _, m := range page.Data {
			if m.Hidden {
				continue
			}
			id := m.Model
			if id == "" {
				id = m.ID
			}
			a := session.AgentModel{ID: id, Name: m.DisplayName, DefaultEffort: m.DefaultReasoningEffort, Default: m.IsDefault}
			for _, e := range m.SupportedReasoningEfforts {
				a.Efforts = append(a.Efforts, e.ReasoningEffort)
			}
			out = append(out, a)
		}
		if page.NextCursor == nil || *page.NextCursor == "" || len(*page.NextCursor) > maxIDLen {
			break
		}
		cursor = *page.NextCursor
	}
	return session.CleanModels(out), nil
}

func (c *Client) readModels(ctx context.Context) {
	c.mu.Lock()
	out := c.signedOut
	c.mu.Unlock()
	if out {
		return
	}
	mctx, cancel := context.WithTimeout(ctx, modelListFor)
	list, err := c.listModels(mctx)
	cancel()
	if err != nil {
		c.log.Info("codex: model list not read")
		return
	}
	c.mu.Lock()
	c.models = list
	c.mu.Unlock()
}

func (c *Client) Models() []session.AgentModel {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.models)
}

func (c *Client) SetTurnSettings(model, effort string) {
	c.mu.Lock()
	c.nextModel, c.nextEffort = model, effort
	c.mu.Unlock()
}

func (c *Client) sentSettings(model, effort string) {
	c.mu.Lock()
	if c.nextModel == model && c.nextEffort == effort {
		c.nextModel, c.nextEffort = "", ""
	}
	c.mu.Unlock()
}

type Lister struct {
	Logger  shared.Logger
	Version string
}

func (l Lister) ListModels(ctx context.Context, rw io.ReadWriteCloser) ([]session.AgentModel, error) {
	c := newClient(rw, Config{Logger: l.Logger})
	defer func() {
		close(c.quit)
		_ = c.Close()
	}()
	version := l.Version
	if version == "" {
		version = "0.0.0"
	}
	if _, err := c.call(ctx, methodInitialize, initializeParams{
		ClientInfo:   clientInfo{Name: clientName, Title: clientText, Version: version},
		Capabilities: capabilities{OptOut: optOut},
	}); err != nil {
		return nil, err
	}
	if err := c.write(outNotification{Method: methodInitialized}); err != nil {
		return nil, err
	}
	return c.listModels(ctx)
}
