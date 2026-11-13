package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/bezmoradi/software-factory/internal/core/agent"
)

type modeState struct {
	current  string
	configID string
}

type newSessionResult struct {
	SessionID string `json:"sessionId"`
	Modes     *struct {
		CurrentModeID  string `json:"currentModeId"`
		AvailableModes []struct {
			ID string `json:"id"`
		} `json:"availableModes"`
	} `json:"modes"`
	ConfigOptions json.RawMessage `json:"configOptions"`
}

type configOption struct {
	ID           string            `json:"id"`
	Category     string            `json:"category"`
	Type         string            `json:"type"`
	CurrentValue json.RawMessage   `json:"currentValue"`
	Options      []json.RawMessage `json:"options"`
}

func (c *Client) askingLocked(id string) bool {
	if id == "" {
		return false
	}
	return slices.Contains(c.cfg.AskingModes, id)
}

func (c *Client) pickAsking(available []string) string {
	for _, want := range c.cfg.AskingModes {
		for _, id := range available {
			if id == want {
				return id
			}
		}
	}
	return ""
}

func (c *Client) askingMode(ctx context.Context, created newSessionResult) error {
	if created.Modes != nil {
		available := make([]string, 0, len(created.Modes.AvailableModes))
		for _, m := range created.Modes.AvailableModes {
			available = append(available, m.ID)
		}
		return c.setMode(ctx, created.Modes.CurrentModeID, available)
	}
	if opt, ok := modeOption(created.ConfigOptions); ok {
		return c.setModeOption(ctx, opt)
	}
	return nil
}

func (c *Client) setMode(ctx context.Context, current string, available []string) error {
	c.mu.Lock()
	c.mode.current = current
	asking := c.askingLocked(current)
	sessionID := c.sessionID
	c.mu.Unlock()
	if asking {
		c.emitMode()
		return nil
	}
	target := c.pickAsking(available)
	if target == "" {
		c.emitMode()
		return ErrNotAsking
	}
	params := map[string]string{"sessionId": sessionID, "modeId": target}
	_, err := c.call(ctx, "session/set_mode", params, func(json.RawMessage) {
		c.mu.Lock()
		c.mode.current = target
		c.mu.Unlock()
	})
	c.emitMode()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNotAsking, err)
	}
	c.note("mode-set")
	return nil
}

func (c *Client) setModeOption(ctx context.Context, opt configOption) error {
	current := stringValue(opt.CurrentValue)
	c.mu.Lock()
	c.mode = modeState{current: current, configID: opt.ID}
	asking := c.askingLocked(current)
	sessionID := c.sessionID
	c.mu.Unlock()
	if asking {
		c.emitMode()
		return nil
	}
	target := c.pickAsking(selectValues(opt.Options))
	if target == "" {
		c.emitMode()
		return ErrNotAsking
	}
	params := map[string]string{"sessionId": sessionID, "configId": opt.ID, "value": target}
	_, err := c.call(ctx, "session/set_config_option", params, func(result json.RawMessage) {
		var r struct {
			ConfigOptions json.RawMessage `json:"configOptions"`
		}
		if json.Unmarshal(result, &r) != nil {
			return
		}
		c.mu.Lock()
		c.readModeOptionLocked(r.ConfigOptions)
		c.mu.Unlock()
	})
	c.emitMode()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNotAsking, err)
	}
	if mode, asking := c.Mode(); !asking || mode != target {
		return ErrNotAsking
	}
	c.note("mode-set")
	return nil
}

func (c *Client) emitMode() {
	c.mu.Lock()
	if c.mode.current != "" {
		c.emitLocked(agent.Event{Type: agent.Mode, Mode: c.mode.current})
	}
	c.mu.Unlock()
	c.signal()
}

func (c *Client) readModeOptionLocked(raw json.RawMessage) bool {
	var opts []configOption
	if c.mode.configID == "" || json.Unmarshal(raw, &opts) != nil {
		return false
	}
	for _, o := range opts {
		if o.ID == c.mode.configID {
			if v := stringValue(o.CurrentValue); v != "" && v != c.mode.current {
				c.mode.current = v
				return true
			}
			return false
		}
	}
	return false
}

func (c *Client) onConfigOptionsLocked(raw json.RawMessage) {
	if c.readModeOptionLocked(raw) {
		c.emitLocked(agent.Event{Type: agent.Mode, Mode: c.mode.current})
	}
}

func modeOption(raw json.RawMessage) (configOption, bool) {
	var opts []configOption
	if len(raw) == 0 || json.Unmarshal(raw, &opts) != nil {
		return configOption{}, false
	}
	for _, o := range opts {
		if o.Category == "mode" && o.Type == "select" && o.ID != "" {
			return o, true
		}
	}
	return configOption{}, false
}

func stringValue(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

func selectValues(options []json.RawMessage) []string {
	var values []string
	for _, raw := range options {
		var o struct {
			Value   *string           `json:"value"`
			Options []json.RawMessage `json:"options"`
		}
		if json.Unmarshal(raw, &o) != nil {
			continue
		}
		if o.Value != nil {
			values = append(values, *o.Value)
			continue
		}
		for _, inner := range o.Options {
			var v struct {
				Value *string `json:"value"`
			}
			if json.Unmarshal(inner, &v) == nil && v.Value != nil {
				values = append(values, *v.Value)
			}
		}
	}
	return values
}
