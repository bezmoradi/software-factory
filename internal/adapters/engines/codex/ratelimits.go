package codex

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const rateLimitsReadFor = 20 * time.Second

type rateWindowJSON struct {
	UsedPercent        *float64 `json:"usedPercent"`
	WindowDurationMins *int64   `json:"windowDurationMins"`
	ResetsAt           *int64   `json:"resetsAt"`
}

type rateSnapshotJSON struct {
	Primary   *rateWindowJSON `json:"primary"`
	Secondary *rateWindowJSON `json:"secondary"`
	PlanType  *string         `json:"planType"`
}

type rateLimits struct {
	primary   *rateWindowJSON
	secondary *rateWindowJSON
}

func (c *Client) mergeRatesLocked(snap rateSnapshotJSON) {
	if snap.Primary != nil {
		c.rates.primary = snap.Primary
	}
	if snap.Secondary != nil {
		c.rates.secondary = snap.Secondary
	}
	if snap.PlanType != nil && *snap.PlanType != "" {
		c.plan = *snap.PlanType
	}
}

func windowOf(name string, w *rateWindowJSON) (session.RateWindow, bool) {
	if w == nil || w.UsedPercent == nil || math.IsNaN(*w.UsedPercent) || math.IsInf(*w.UsedPercent, 0) {
		return session.RateWindow{}, false
	}
	kind := name
	if w.WindowDurationMins != nil && *w.WindowDurationMins > 0 && *w.WindowDurationMins < 1_000_000 {
		kind += ":" + strconv.FormatInt(*w.WindowDurationMins, 10)
	}
	out := session.RateWindow{Kind: kind, Utilization: *w.UsedPercent / 100}
	if w.ResetsAt != nil && *w.ResetsAt > 0 && *w.ResetsAt < 1<<40 {
		out.ResetsAt = time.Unix(*w.ResetsAt, 0).UTC()
	}
	return out, true
}

func (c *Client) reportRatesLocked() {
	if c.rateHook == nil || c.signIn != SignInChatGPT {
		return
	}
	windows := []session.RateWindow{}
	for _, x := range []struct {
		name string
		w    *rateWindowJSON
	}{{"primary", c.rates.primary}, {"secondary", c.rates.secondary}} {
		if w, ok := windowOf(x.name, x.w); ok {
			windows = append(windows, w)
		}
	}
	hook, plan := c.rateHook, c.plan
	go hook(plan, windows)
}

func (c *Client) rateLimitsLocked(p json.RawMessage) {
	var n struct {
		RateLimits rateSnapshotJSON `json:"rateLimits"`
	}
	if json.Unmarshal(p, &n) != nil {
		return
	}
	c.mergeRatesLocked(n.RateLimits)
	c.reportRatesLocked()
}

func (c *Client) watchRates() {
	c.mu.Lock()
	on := c.rateHook != nil && c.signIn == SignInChatGPT
	c.mu.Unlock()
	if on {
		go c.readRates()
	}
}

func (c *Client) readRates() {
	ctx, cancel := context.WithTimeout(context.Background(), rateLimitsReadFor)
	defer cancel()
	raw, err := c.call(ctx, methodRateLimitsRead, struct{}{})
	if err != nil {
		c.log.Info("codex: rate limits not read")
		return
	}
	var r struct {
		RateLimits rateSnapshotJSON `json:"rateLimits"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mergeRatesLocked(r.RateLimits)
	c.reportRatesLocked()
}
