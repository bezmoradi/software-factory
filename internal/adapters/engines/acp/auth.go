package acp

import (
	"context"
	"encoding/json"
)

type AuthMethod struct {
	ID          string
	Name        string
	Description string
}

type AuthRequiredError struct {
	Methods []AuthMethod
	Message string
}

func (e *AuthRequiredError) Error() string {
	return "acp: the agent requires sign-in before session/new"
}

func agentAuthMethods(raw []json.RawMessage) []AuthMethod {
	var out []AuthMethod
	for _, r := range raw {
		var m struct {
			ID          string  `json:"id"`
			Name        string  `json:"name"`
			Description string  `json:"description"`
			Type        *string `json:"type"`
		}
		if json.Unmarshal(r, &m) != nil || m.ID == "" {
			continue
		}
		if m.Type != nil && *m.Type != "agent" {
			continue
		}
		out = append(out, AuthMethod{ID: m.ID, Name: m.Name, Description: m.Description})
	}
	return out
}

func (c *Client) authRequiredError(rerr *RPCError) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.authRequired = true
	c.note("auth-required")
	return &AuthRequiredError{Methods: append([]AuthMethod(nil), c.authMethods...), Message: clip(rerr.Message)}
}

func (c *Client) AuthMethods() []AuthMethod {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]AuthMethod(nil), c.authMethods...)
}

func (c *Client) Authenticate(ctx context.Context, methodID string) error {
	c.startMu.Lock()
	defer c.startMu.Unlock()
	c.mu.Lock()
	required := c.authRequired && c.sessionID == ""
	known := false
	for _, m := range c.authMethods {
		if m.ID == methodID {
			known = true
		}
	}
	c.mu.Unlock()
	if !required {
		return ErrAuthNotRequired
	}
	if !known {
		return ErrUnknownAuthMethod
	}
	c.mu.Lock()
	c.authRequired = false
	c.mu.Unlock()
	c.note("authenticate")
	_, err := c.call(ctx, "authenticate", map[string]string{"methodId": methodID}, nil)
	return err
}
