package acp

import (
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
	c.note("auth-required")
	return &AuthRequiredError{Methods: append([]AuthMethod(nil), c.authMethods...), Message: clip(rerr.Message)}
}
