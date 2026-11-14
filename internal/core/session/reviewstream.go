package session

import (
	"context"
	"encoding/json"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/review"
)

const (
	MaxRateLine       = 64 << 10
	MaxToolCalls      = 1_000_000
	progressShowEvery = 5 * time.Second
	maxUtilization    = 10
	earliestReset     = 1_500_000_000
	latestReset       = 4_000_000_000
	maxGivenFiles     = 40
)

type RunProgress struct {
	Elapsed   time.Duration
	ToolCalls int
	Done      bool
}

type StreamedRun struct {
	Code      int
	Result    string
	RateLimit string
	Stderr    string
	TimedOut  bool
	Elapsed   time.Duration
	ToolCalls int
}

type StreamHost interface {
	RunStreamed(ctx context.Context, c Command, progress func(RunProgress)) (StreamedRun, error)
}

type ReviewProgress struct {
	Elapsed   time.Duration
	ToolCalls int
	Done      bool
}

type RunGiven struct {
	Prompt string
	Flags  []string
	Files  []string
}

var (
	rateKinds    = []string{"five_hour", "seven_day", "seven_day_overage_included"}
	rateStatuses = []string{"allowed", "allowed_warning", "rejected"}
)

func (r *Reviews) reviewerRun(ctx context.Context, c Command, progress func(RunProgress)) (RunResult, error) {
	h, ok := r.cfg.Host.(StreamHost)
	if !ok {
		res, err := r.cfg.Host.Run(ctx, c)
		if err == nil {
			r.rateFrom(lastRateLine(res.Stdout))
		}
		return res, err
	}
	if progress == nil {
		progress = func(RunProgress) {}
	}
	s, err := h.RunStreamed(ctx, c, progress)
	if err != nil {
		return RunResult{}, err
	}
	r.rateFrom(s.RateLimit)
	progress(RunProgress{Elapsed: s.Elapsed, ToolCalls: s.ToolCalls, Done: true})
	return RunResult{Code: s.Code, Stdout: s.Result, Stderr: s.Stderr, TimedOut: s.TimedOut}, nil
}

func lastRateLine(stdout string) string {
	lines := strings.Split(stdout, "\n")
	for _, l := range slices.Backward(lines) {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "{") && strings.Contains(l, `"rate_limit_event"`) && len(l) <= MaxRateLine {
			return l
		}
	}
	return ""
}

func (r *Reviews) OnRateWindows(f func(side string, w []RateWindow)) {
	if f == nil {
		return
	}
	r.mu.Lock()
	r.rateHandlers = append(r.rateHandlers, f)
	r.mu.Unlock()
}

func (r *Reviews) rateFrom(line string) {
	w, ok := RateWindowsOf(line)
	if !ok {
		return
	}
	r.mu.Lock()
	handlers := slices.Clone(r.rateHandlers)
	r.mu.Unlock()
	for _, h := range handlers {
		h(SideReviewers, slices.Clone(w))
	}
}

func RateWindowsOf(line string) ([]RateWindow, bool) {
	line = strings.TrimSpace(line)
	if line == "" || len(line) > MaxRateLine || !strings.HasPrefix(line, "{") {
		return nil, false
	}
	var m map[string]any
	if json.Unmarshal([]byte(line), &m) != nil || m["type"] != "rate_limit_event" {
		return nil, false
	}
	info, ok := m["rate_limit_info"].(map[string]any)
	if !ok {
		return nil, false
	}
	windows, ok := info["unifiedWindows"].(map[string]any)
	if !ok {
		return nil, false
	}
	var out []RateWindow
	for _, kind := range rateKinds {
		w, ok := windows[kind].(map[string]any)
		if !ok {
			continue
		}
		if rw, ok := rateWindowOf(kind, w); ok {
			out = append(out, rw)
		}
	}
	return out, len(out) > 0
}

func rateWindowOf(kind string, w map[string]any) (RateWindow, bool) {
	u, ok := w["utilization"].(float64)
	if !ok || math.IsNaN(u) || math.IsInf(u, 0) || u < 0 || u > maxUtilization {
		return RateWindow{}, false
	}
	reset, ok := w["resetsAt"].(float64)
	if !ok || reset != math.Trunc(reset) || reset < earliestReset || reset > latestReset {
		return RateWindow{}, false
	}
	status := ""
	if v, present := w["status"]; present {
		s, ok := v.(string)
		if !ok || !slices.Contains(rateStatuses, s) {
			return RateWindow{}, false
		}
		status = s
	}
	return RateWindow{Kind: kind, Utilization: u, ResetsAt: time.Unix(int64(reset), 0).UTC(), Status: status}, true
}

func (r *Reviews) progressShown(sid string, mu *sync.Mutex, view *ReviewView, i int) func(RunProgress) {
	last := -1
	var shownAt time.Time
	return func(p RunProgress) {
		p.ToolCalls = min(max(p.ToolCalls, 0), MaxToolCalls)
		p.Elapsed = max(p.Elapsed, 0)
		now := r.cfg.Now()
		mu.Lock()
		if i >= len(view.Roles) || view.Roles[i].State != review.Running {
			mu.Unlock()
			return
		}
		view.Roles[i].Progress = &ReviewProgress{Elapsed: p.Elapsed, ToolCalls: p.ToolCalls, Done: p.Done}
		if p.Done || p.ToolCalls == last && now.Sub(shownAt) < progressShowEvery {
			mu.Unlock()
			return
		}
		last, shownAt = p.ToolCalls, now
		shown := view.clone()
		mu.Unlock()
		r.show(sid, shown)
	}
}

func givenOf(argv []string, base, head string, extra []SnapshotFile) *RunGiven {
	if len(argv) == 0 {
		return nil
	}
	g := &RunGiven{Prompt: argv[len(argv)-1], Flags: slices.Clone(argv[1 : len(argv)-1])}
	if base != "" {
		g.Files = append(g.Files, "base/ (commit "+base+")")
	} else {
		g.Files = append(g.Files, "base/ (empty)")
	}
	g.Files = append(g.Files, "head/ (commit "+head+")", "change.diff", CommitsFile)
	var names []string
	for _, f := range extra {
		if f.Name != "" && !slices.Contains(names, f.Name) {
			names = append(names, f.Name)
		}
	}
	slices.Sort(names)
	g.Files = append(g.Files, names...)
	if len(g.Files) > maxGivenFiles {
		g.Files = append(g.Files[:maxGivenFiles], "…")
	}
	return g
}
