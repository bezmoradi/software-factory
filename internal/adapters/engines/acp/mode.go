package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/agent"
)

const modeConfirmWait = 5 * time.Second

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
	if opt, ok := modeOption(created.ConfigOptions); ok {
		return c.setModeOption(ctx, opt)
	}
	if created.Modes != nil {
		available := make([]string, 0, len(created.Modes.AvailableModes))
		for _, m := range created.Modes.AvailableModes {
			available = append(available, m.ID)
		}
		return c.setMode(ctx, created.Modes.CurrentModeID, available)
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
	if _, err := c.call(ctx, "session/set_mode", params, nil); err != nil {
		c.emitMode()
		return fmt.Errorf("%w: %w", ErrNotAsking, err)
	}
	if err := c.confirmMode(ctx, target); err != nil {
		c.emitMode()
		c.warn("mode-not-kept")
		return err
	}
	c.note("mode-set")
	return nil
}

func (c *Client) confirmMode(ctx context.Context, target string) error {
	timer := time.NewTimer(modeConfirmWait)
	defer timer.Stop()
	for {
		if mode, asking := c.Mode(); asking && mode == target {
			return nil
		}
		select {
		case <-c.modeSeen:
		case <-timer.C:
			return ErrModeNotKept
		case <-c.stop:
			return ErrClosed
		case <-ctx.Done():
			return ctx.Err()
		}
	}
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
	shown := false
	_, err := c.call(ctx, "session/set_config_option", params, func(result json.RawMessage) {
		var r struct {
			ConfigOptions json.RawMessage `json:"configOptions"`
		}
		if json.Unmarshal(result, &r) != nil {
			return
		}
		c.mu.Lock()
		shown, _ = c.readModeOptionLocked(r.ConfigOptions)
		c.mu.Unlock()
	})
	c.emitMode()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNotAsking, err)
	}
	if mode, asking := c.Mode(); !asking || mode != target {
		if shown {
			c.warn("mode-not-kept")
			return ErrModeNotKept
		}
		if err := c.confirmMode(ctx, target); err != nil {
			c.emitMode()
			c.warn("mode-not-kept")
			return err
		}
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

func (c *Client) readModeOptionLocked(raw json.RawMessage) (shown, changed bool) {
	var opts []configOption
	if json.Unmarshal(raw, &opts) != nil {
		return false, false
	}
	for _, o := range opts {
		if (c.mode.configID != "" && o.ID != c.mode.configID) || (c.mode.configID == "" && !isModeSelect(o)) {
			continue
		}
		v := stringValue(o.CurrentValue)
		if v == "" {
			return false, false
		}
		c.mode.configID = o.ID
		if v == c.mode.current {
			return true, false
		}
		c.mode.current = v
		return true, true
	}
	return false, false
}

func (c *Client) onConfigOptionsLocked(raw json.RawMessage) {
	shown, changed := c.readModeOptionLocked(raw)
	if changed {
		c.emitLocked(agent.Event{Type: agent.Mode, Mode: c.mode.current})
	}
	if shown {
		c.modeReportedLocked()
	}
}

func (c *Client) modeReportedLocked() {
	select {
	case c.modeSeen <- struct{}{}:
	default:
	}
}

func isModeSelect(o configOption) bool {
	return o.Category == "mode" && o.Type == "select" && o.ID != ""
}

func modeOption(raw json.RawMessage) (configOption, bool) {
	var opts []configOption
	if len(raw) == 0 || json.Unmarshal(raw, &opts) != nil {
		return configOption{}, false
	}
	for _, o := range opts {
		if isModeSelect(o) {
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
