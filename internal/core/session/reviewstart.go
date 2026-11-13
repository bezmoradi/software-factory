package session

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/review"
	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

func (r *Reviews) Start(ctx context.Context, t ReviewTarget) error {
	if t.Repo == "" || t.Name == "" || t.Branch == "" {
		return explained(conflict("no clone"), "The engine doesn't know which clone and branch this session works in, so it can't review it.")
	}
	r.mu.Lock()
	if r.running[t.SID] {
		r.mu.Unlock()
		return explained(conflict("reviewing"), "A review of this session is running. Wait for it to finish.")
	}
	if len(r.running) >= MaxReviewing {
		r.mu.Unlock()
		return explained(limited("reviews"), fmt.Sprintf("The engine reviews at most %d sessions at once. Wait for one to finish.", MaxReviewing))
	}
	r.running[t.SID] = true
	r.mu.Unlock()
	release := func() {
		r.mu.Lock()
		delete(r.running, t.SID)
		r.mu.Unlock()
	}
	signIn, err := r.CheckSignIn(ctx)
	if err != nil {
		release()
		return err
	}
	if err := judge("The reviewer container's Claude Code", signIn, r.cfg.Expected.Adopt(ReviewerAgent, signIn.Method), ReviewerSignInStep, t.CostOK, "the review"); err != nil {
		release()
		r.cfg.Logger.Info("review", "event", "refused", "why", err.Error())
		return err
	}
	plugin, err := r.cfg.Plugins.PluginPath(r.cfg.Plugin)
	if err != nil {
		release()
		return explained(conflict("no plugin"), clip("The reviewers' plugin isn't ready: "+err.Error(), maxOutput))
	}
	head, err := r.cfg.Snapshots.Head(t.Repo, t.Branch)
	if err != nil {
		release()
		return explained(conflict("branch"), clip("The engine couldn't read the session's branch "+t.Branch+": "+err.Error(), maxOutput))
	}
	main, err := r.cfg.Mains.Fresh(ctx, t.Name)
	if err != nil {
		release()
		if e, ok := errors.AsType[*Error](err); ok {
			return e
		}
		return explained(conflict("fetch"), "The engine couldn't fetch the remote's main branch, so it can't tell what to review.")
	}
	base, err := r.cfg.History.MergeBase(t.Repo, head, main)
	if err != nil {
		release()
		return explained(conflict("merge base"), clip("The engine couldn't find where "+t.Branch+" leaves the remote's main branch: "+err.Error(), maxOutput))
	}
	if base == head {
		release()
		return explained(conflict("no change"), "The session's branch has nothing the remote's main branch lacks, so there's nothing to review. Commit the work first.")
	}
	var required []workflow.Requirement
	if t.Plan != nil {
		required = t.Plan(base, head)
	} else {
		for _, role := range r.roles() {
			required = append(required, workflow.Requirement{Agent: r.cfg.Agents[role], Role: role})
		}
	}
	if len(required) == 0 {
		release()
		return explained(conflict("no reviews"), "This session's work type needs no review, and it can't push. Pick another work type to review and push it.")
	}
	entries, err := r.Entries()
	if err != nil {
		release()
		return explained(conflict("records"), "The engine couldn't read its review records.")
	}
	limit := roundLimit(t.Rounds)
	var plans []plannedRole
	var capped []string
	var finished []RoleReview
	for _, q := range required {
		rd := RoundOf(entries, t.Branch, q.Agent)
		key := review.Key{Commit: head, Branch: t.Branch, Agent: q.Agent}
		if e, ok := entries[key]; t.Retry && ok && !Retryable(e) {
			finished = append(finished, finishedRole(q, e, rd.Count, limit))
			continue
		}
		if rd.Capped(limit) {
			capped = append(capped, ReviewerName(q))
			continue
		}
		p := plannedRole{q: q, base: base, covers: base, round: rd.Count + 1, attempt: nextAttempt(entries, key)}
		if e, ok := entries[key]; ok {
			p.auto = e.Auto
			if e.Record.State != review.Running {
				p.round = rd.Count
			}
		}
		if prev := r.incremental(t, entries, q.Agent, head, main); prev != nil {
			p.base, p.since, p.covers, p.previous = prev.Key.Commit, prev.Key.Commit, prev.Coverage(), prev.Message
		}
		plans = append(plans, p)
	}
	if len(plans) == 0 && t.Retry {
		release()
		return explained(conflict("nothing to retry"), "Every review this commit needs has an outcome, so the engine doesn't run one again.")
	}
	if len(plans) == 0 {
		release()
		return explained(conflict("capped"), fmt.Sprintf("Every reviewer this commit needs has blocked in %d %s on this branch, the limit this project set, so the engine doesn't start them again. Decide on the push: override a verdict, allow it with your reason, deny it, or hand the findings off.", limit, plural(limit, "round", "rounds")))
	}
	if t.Auto && autoRuns(entries, t.Branch)+len(plans) > MaxAutoRuns {
		release()
		r.cfg.Logger.Info("review", "event", "refused", "why", "auto-capped")
		return explained(conflict("auto-capped"), fmt.Sprintf("The engine already ran %d automatic reviews on this branch, its limit, so it doesn't start more on the agent's request. Press Review to start them yourself.", MaxAutoRuns))
	}
	view := ReviewView{Commit: head, Base: base, Branch: t.Branch, Running: true, Note: "Checking the reviewer container's sandbox…", SignIn: &signIn, SessionCostUSD: r.branchCost(t.Branch)}
	for _, p := range plans {
		view.Roles = append(view.Roles, RoleReview{Role: p.q.Role, Name: ReviewerName(p.q), Agent: p.q.Agent, State: review.Running, Why: p.q.Why,
			Round: p.round, Limit: limit, Since: p.since, Previous: p.previous != ""})
	}
	view.Roles = append(view.Roles, finished...)
	held := ""
	if len(capped) > 0 {
		held = strings.Join(capped, " and ") + " blocked in " + strconv.Itoa(limit) + " " + plural(limit, "round", "rounds") + " on this branch, the limit this project set, so the engine didn't start " + plural(len(capped), "it", "them") + " again: you decide on the push."
		view.Note += " " + held
	}
	r.show(t.SID, view)
	r.cfg.Logger.Info("review", "event", "started", "roles", len(plans), "capped", len(capped))
	r.wg.Go(func() {
		r.run(t, head, plans, plugin, view, held)
		release()
		if r.cfg.Base.Err() == nil {
			r.finished(t.SID, head)
		}
	})
	return nil
}

type plannedRole struct {
	q        workflow.Requirement
	base     string
	since    string
	covers   string
	previous string
	round    int
	auto     bool
	attempt  int
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func (r *Reviews) incremental(t ReviewTarget, entries map[review.Key]ReviewEntry, agent, head, main string) *ReviewEntry {
	prev := previousRound(entries, t.Branch, agent, head)
	if prev == nil {
		return nil
	}
	if prev.Record.State == review.Block && (prev.Message == "" || prev.Clipped || len(prev.Message) >= maxReviewText-4) {
		return nil
	}
	in, err := r.cfg.History.IsAncestor(t.Repo, prev.Key.Commit, head)
	if err != nil || !in {
		return nil
	}
	if coverProblem(r.cfg.History, t.Repo, prev.Coverage(), head, main) != "" {
		return nil
	}
	return prev
}

func coverProblem(h History, repo, base, head, main string) string {
	if base == "" {
		return ""
	}
	if main == "" {
		return "the remote has no main branch now, so everything in the branch is new, and the review compared it with " + shortSHA(base)
	}
	for _, c := range []struct{ commit, name string }{{main, "the remote's main branch"}, {head, "the branch"}} {
		if base == c.commit {
			continue
		}
		in, err := h.IsAncestor(repo, base, c.commit)
		if err != nil {
			return "the engine couldn't check that the review's base " + shortSHA(base) + " is in " + c.name
		}
		if !in {
			return "the review compared with " + shortSHA(base) + ", which " + c.name + " doesn't contain now"
		}
	}
	return ""
}
