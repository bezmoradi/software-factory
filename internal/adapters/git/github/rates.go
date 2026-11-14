package github

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	maxRateHosts   = 100
	rateLimitedFor = time.Minute
	maxRateWait    = time.Hour
)

type Rates struct {
	mu    sync.Mutex
	now   func() time.Time
	hosts map[string]session.HostRate
}

func NewRates() *Rates {
	return &Rates{now: time.Now, hosts: map[string]session.HostRate{}}
}

func headerInt(h http.Header, name string) (int, bool) {
	v := h.Get(name)
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	return n, err == nil && n >= 0
}

func (r *Rates) note(host string, status int, h http.Header) {
	if r == nil {
		return
	}
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	rate := r.hosts[host]
	resource := h.Get("X-RateLimit-Resource")
	limit, okLimit := headerInt(h, "X-RateLimit-Limit")
	remaining, okRemaining := headerInt(h, "X-RateLimit-Remaining")
	reset, okReset := headerInt(h, "X-RateLimit-Reset")
	if (resource == "" || resource == "core") && okLimit && okRemaining {
		rate.Limit, rate.Remaining = limit, remaining
		if okReset {
			rate.Reset = time.Unix(int64(reset), 0)
		}
	}
	limited := status == http.StatusTooManyRequests || status == http.StatusForbidden && (h.Get("Retry-After") != "" || okRemaining && remaining == 0)
	if limited {
		until := now.Add(rateLimitedFor)
		if after, ok := headerInt(h, "Retry-After"); ok && after > 0 {
			until = now.Add(time.Duration(after) * time.Second)
		} else if okReset && time.Unix(int64(reset), 0).After(now) {
			until = time.Unix(int64(reset), 0)
		}
		if until.Sub(now) > maxRateWait {
			until = now.Add(maxRateWait)
		}
		rate.Until = until
	}
	if rate.Reset.Sub(now) > maxRateWait {
		rate.Reset = now.Add(maxRateWait)
	}
	if _, known := r.hosts[host]; !known && len(r.hosts) >= maxRateHosts {
		clear(r.hosts)
	}
	r.hosts[host] = rate
}

func (a API) Rate(host string) session.HostRate {
	if a.Rates == nil {
		return session.HostRate{}
	}
	a.Rates.mu.Lock()
	defer a.Rates.mu.Unlock()
	return a.Rates.hosts[bare(host)]
}
