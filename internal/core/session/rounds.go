package session

import (
	"github.com/bezmoradi/software-factory/internal/core/review"
)

type Round struct {
	Count   int
	Blocked int
	Last    *ReviewEntry
}

func RoundOf(entries map[review.Key]ReviewEntry, branch, agent string) Round {
	var out Round
	for k, e := range entries {
		if k.Branch != branch || k.Agent != agent || e.Record.State == review.Running {
			continue
		}
		out.Count++
		if e.Record.State == review.Block && e.Record.Override == "" {
			out.Blocked++
		}
		switch e.Record.State {
		case review.Pass, review.Block, review.Unknown:
		default:
			continue
		}
		if out.Last == nil || e.StartedAt.After(out.Last.StartedAt) {
			c := e
			out.Last = &c
		}
	}
	return out
}

func (r Round) Capped(limit int) bool {
	return limit > 0 && r.Blocked >= limit && r.Last != nil && r.Last.Record.State == review.Block && r.Last.Record.Override == ""
}

func roundLimit(n int) int {
	if n <= 0 {
		return DefaultRounds
	}
	return n
}

func (e ReviewEntry) Coverage() string {
	if e.Since != "" {
		return e.Covers
	}
	return e.Base
}

func previousRound(entries map[review.Key]ReviewEntry, branch, agent, head string) *ReviewEntry {
	var out *ReviewEntry
	for k, e := range entries {
		if k.Branch != branch || k.Agent != agent || k.Commit == head {
			continue
		}
		if e.Record.State != review.Pass && e.Record.State != review.Block {
			continue
		}
		if out == nil || e.StartedAt.After(out.StartedAt) {
			c := e
			out = &c
		}
	}
	return out
}
