package session

import (
	"github.com/bezmoradi/software-factory/internal/core/review"
)

type Round struct {
	Count   int
	Blocked int
	Last    *ReviewEntry
}

func RoundOf(entries map[review.Key]ReviewEntry, branch, agent string, advisory bool) Round {
	var out Round
	commits := map[string]bool{}
	blocked := map[string]*ReviewEntry{}
	for k, e := range entries {
		if k.Branch != branch || k.Agent != agent || e.Record.Advisory != advisory || e.Record.State == review.Running {
			continue
		}
		commits[k.Commit] = true
		if e.Record.State == review.Block && e.Record.Override == "" && blocked[k.Commit] == nil {
			c := e
			blocked[k.Commit] = &c
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
	out.Count, out.Blocked = len(commits), len(blocked)
	if out.Last != nil && out.Last.Record.State != review.Block {
		if b := blocked[out.Last.Key.Commit]; b != nil {
			out.Last = b
		}
	}
	return out
}

func runsOf(entries map[review.Key]ReviewEntry, k review.Key) []ReviewEntry {
	var out []ReviewEntry
	for run := 1; run <= review.MaxRuns; run++ {
		if e, ok := entries[k.AtRun(run)]; ok {
			out = append(out, e)
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

func previousRound(entries map[review.Key]ReviewEntry, branch, agent, head string, run int, advisory bool) *ReviewEntry {
	var out *ReviewEntry
	for k, e := range entries {
		if k.Branch != branch || k.Agent != agent || e.Record.Advisory != advisory || k.Commit == head || k.Number() != max(run, 1) {
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

func acceptedRound(entries map[review.Key]ReviewEntry, branch, agent string, advisory bool) *ReviewEntry {
	var accepted, blocked *ReviewEntry
	for k, e := range entries {
		if k.Branch != branch || k.Agent != agent || e.Record.Advisory != advisory || e.Record.State == review.Running {
			continue
		}
		c := e
		switch {
		case e.Record.Override != "":
			if accepted == nil || e.StartedAt.After(accepted.StartedAt) {
				accepted = &c
			}
		case e.Record.State == review.Block:
			if blocked == nil || e.StartedAt.After(blocked.StartedAt) {
				blocked = &c
			}
		}
	}
	if accepted == nil || blocked != nil && blocked.StartedAt.After(accepted.StartedAt) {
		return nil
	}
	return accepted
}
