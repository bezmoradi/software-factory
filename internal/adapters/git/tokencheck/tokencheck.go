package tokencheck

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	maxAnswer   = 64 << 10
	soonExpires = 7 * 24 * time.Hour
	userAgent   = "software-factory"
)

type Checker struct {
	Client *http.Client
	Now    func() time.Time
}

var _ session.TokenChecker = Checker{}

func (c Checker) CheckToken(ctx context.Context, host, token string) session.TokenCheck {
	name := strings.TrimPrefix(host, "https://")
	switch {
	case name == "github.com" || strings.HasSuffix(name, ".ghe.com") && !strings.HasPrefix(name, "api."):
		return c.github(ctx, name, token)
	case name == "gitlab.com" || strings.HasPrefix(name, "gitlab."):
		return c.gitlab(ctx, name, token)
	}
	return session.TokenCheck{}
}

func (c Checker) client() *http.Client {
	base := http.DefaultClient
	if c.Client != nil {
		base = c.Client
	}
	out := *base
	out.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &out
}

func (c Checker) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func unchecked(site string) session.TokenCheck {
	return session.TokenCheck{Warning: "The engine couldn't reach " + site + " to check this token, so it saved it unchecked."}
}

func (c Checker) ask(ctx context.Context, address string, headers map[string]string) (int, http.Header, []byte, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return 0, nil, nil, false
	}
	req.Header.Set("User-Agent", userAgent)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return 0, nil, nil, false
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer))
	if err != nil {
		return 0, nil, nil, false
	}
	return resp.StatusCode, resp.Header, data, true
}

func (c Checker) github(ctx context.Context, name, token string) session.TokenCheck {
	base := "https://api.github.com"
	if name != "github.com" {
		base = "https://api." + name
	}
	status, header, _, ok := c.ask(ctx, base+"/user", map[string]string{
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": "2022-11-28",
		"Authorization":        "Bearer " + token,
	})
	switch {
	case !ok:
		return unchecked("GitHub")
	case status == http.StatusUnauthorized:
		return session.TokenCheck{Rejected: true, Problem: "GitHub didn't accept this token."}
	case status != http.StatusOK:
		return unchecked("GitHub")
	}
	if when, ok := githubExpiry(header.Get("GitHub-Authentication-Token-Expiration")); ok && when.Sub(c.now()) < soonExpires {
		return session.TokenCheck{Warning: "This token expires on " + when.Format("Jan 2, 2006") + ". Create a new one before then."}
	}
	return session.TokenCheck{}
}

func githubExpiry(v string) (time.Time, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{"2006-01-02 15:04:05 MST", "2006-01-02 15:04:05 -0700"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func (c Checker) gitlab(ctx context.Context, name, token string) session.TokenCheck {
	status, _, data, ok := c.ask(ctx, "https://"+name+"/api/v4/personal_access_tokens/self", map[string]string{
		"Accept":        "application/json",
		"PRIVATE-TOKEN": token,
	})
	switch {
	case !ok:
		return unchecked("GitLab")
	case status == http.StatusUnauthorized:
		return session.TokenCheck{Rejected: true, Problem: "GitLab didn't accept this token."}
	case status != http.StatusOK:
		return unchecked("GitLab")
	}
	var self struct {
		Active    *bool    `json:"active"`
		Revoked   bool     `json:"revoked"`
		Scopes    []string `json:"scopes"`
		ExpiresAt string   `json:"expires_at"`
	}
	if json.Unmarshal(data, &self) != nil {
		return unchecked("GitLab")
	}
	if self.Revoked || self.Active != nil && !*self.Active {
		return session.TokenCheck{Rejected: true, Problem: "GitLab says this token is revoked or expired."}
	}
	var warnings []string
	if !slices.Contains(self.Scopes, "write_repository") && !slices.Contains(self.Scopes, "api") {
		warnings = append(warnings, "This token has neither the write_repository nor the api scope, so pushes to GitLab will fail. Create one with write_repository to push.")
	}
	if when, err := time.Parse("2006-01-02", self.ExpiresAt); err == nil && when.Sub(c.now()) < soonExpires {
		warnings = append(warnings, "This token expires on "+when.Format("Jan 2, 2006")+". Create a new one before then.")
	}
	return session.TokenCheck{Warning: strings.Join(warnings, " ")}
}
