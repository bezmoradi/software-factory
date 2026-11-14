package session

import (
	"regexp"
	"strings"
	"time"
)

const (
	SignInWarnBefore = 10 * 24 * time.Hour
	SignInEarlier    = 3 * 24 * time.Hour
	TokenWarnBefore  = 7 * 24 * time.Hour
	maxTokenHosts    = 200
)

var (
	expiryFloor   = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	expiryCeiling = time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC)
	tokenHost     = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62})(\.[a-z0-9]([a-z0-9-]{0,62}))*(:[0-9]{1,5})?$`)
)

var tokenLayouts = []string{"2006-01-02 15:04:05 MST", "2006-01-02 15:04:05 -0700", "2006-01-02 15:04:05 UTC", time.RFC3339}

func CleanExpiry(s string) time.Time {
	at, err := time.Parse(time.RFC3339Nano, clip(strings.TrimSpace(s), 64))
	if err != nil || at.Before(expiryFloor) || at.After(expiryCeiling) {
		return time.Time{}
	}
	return at.UTC()
}

func CleanTokenHost(host string) string {
	host = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(host), "https://"))
	if len(host) > 253 || !tokenHost.MatchString(host) {
		return ""
	}
	return host
}

func TokenExpiryOf(header string) (time.Time, bool) {
	header = clip(strings.TrimSpace(header), 64)
	for _, layout := range tokenLayouts {
		if at, err := time.Parse(layout, header); err == nil && !at.Before(expiryFloor) && !at.After(expiryCeiling) {
			return at.UTC(), true
		}
	}
	return time.Time{}, false
}

func (l *Limits) TokenExpiry(host, header string) {
	host = CleanTokenHost(host)
	if host == "" {
		return
	}
	at, known := time.Time{}, false
	if header != "" {
		at, known = TokenExpiryOf(header)
		if !known {
			return
		}
	}
	l.mu.Lock()
	old, had := l.tokens[host]
	switch {
	case known && (!had || !old.Equal(at)):
		if !had && len(l.tokens) >= maxTokenHosts {
			l.mu.Unlock()
			return
		}
		l.tokens[host] = at
		l.unsaved = true
	case !known && had:
		delete(l.tokens, host)
		l.unsaved = true
	default:
		l.mu.Unlock()
		return
	}
	l.mu.Unlock()
	_ = l.save()
}

func (l *Limits) TokenSaved(host string) {
	host = CleanTokenHost(host)
	l.mu.Lock()
	_, had := l.tokens[host]
	delete(l.tokens, host)
	l.mu.Unlock()
	if had {
		_ = l.save()
	}
}

func signInEnding(s *SignIn, now time.Time) bool {
	return s != nil && s.Method == SignInSubscription && !s.ExpiresAt.IsZero() && !now.Before(s.ExpiresAt.Add(-SignInWarnBefore))
}

type expiryNote struct {
	what  string
	side  string
	host  string
	from  time.Time
	until time.Time
}

func (l *Limits) expiriesLocked(now time.Time) []expiryNote {
	var out []expiryNote
	signIn := func(side string, s *SignIn) {
		if !signInEnding(s, now) {
			return
		}
		out = append(out, expiryNote{what: "sign-in", side: side, from: s.ExpiresAt.Add(-SignInEarlier), until: s.ExpiresAt})
	}
	signIn(SideAgents, l.latestClaudeLocked())
	signIn(SideReviewers, l.reviewerSignIn)
	for host, at := range l.tokens {
		if !now.Before(at.Add(-TokenWarnBefore)) {
			out = append(out, expiryNote{what: "token", host: host, until: at})
		}
	}
	return out
}
