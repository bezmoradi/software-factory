package session

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/review"
	"github.com/bezmoradi/software-factory/internal/core/shared"
	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

func (r *Reviews) Start(ctx context.Context, t ReviewTarget) error {
	if t.Req == "" {
		t.Req = shared.RequestOf(ctx)
	}
	if t.Repo == "" || t.Name == "" || t.Branch == "" {
		return explained(conflict("no clone"), "The engine doesn't know which clone and branch this session works in, so it can't review it.")
	}
	r.mu.Lock()
	if _, busy := r.running[t.SID]; busy {
		r.mu.Unlock()
		return explained(conflict("reviewing"), "A review of this session is running. Wait for it to finish.")
	}
	if err := r.holdLocked(t.SID); err != nil {
		r.mu.Unlock()
		return err
	}
	r.mu.Unlock()
	release := func() {
		r.mu.Lock()
		delete(r.running, t.SID)
		r.mu.Unlock()
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
	if t.CloneSync != nil {
		if why := t.CloneSync(main); why != "" {
			release()
			return explained(conflict("clone"), clip(keepSentence(strings.ToUpper(why[:1])+why[1:])+" No review started, since the reviewers would read the branch against an older main. Try again in a moment.", maxOutput))
		}
	}
	base, err := r.cfg.History.MergeBase(ctx, t.Repo, head, main)
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
		if required, err = t.Plan(ctx, base, head); err != nil {
			release()
			return planStopped()
		}
	} else {
		for _, role := range r.roles() {
			required = append(required, workflow.Requirement{Agent: r.cfg.Agents[role], Role: role})
		}
	}
	if !slices.ContainsFunc(required, func(q workflow.Requirement) bool { return !q.Advisory }) {
		release()
		return explained(conflict("no reviews"), "This session's work type needs no review, and it can't push. Pick another work type to review and push it.")
	}
	entries, err := r.Entries()
	if err != nil {
		release()
		return explained(conflict("records"), "The engine couldn't read its review records.")
	}
	given := reviewGiven{asked: t.Approved != nil, rules: rulesGiven{main: main}}
	ticket := r.approvalTicket()
	if t.Approved != nil {
		if given.approved, err = t.Approved(ctx, head, main); err != nil {
			release()
			return planStopped()
		}
	}
	if t.TeamRules != nil {
		if given.team, err = t.TeamRules(ctx, base, head); err != nil {
			release()
			return planStopped()
		}
	}
	approval := given.approved.Given()
	approval.Team = given.team.Digest()
	approval.hold(entries, t.Branch, head)
	r.approvalIs(t.SID, head, ticket, approval)
	limit := roundLimit(t.Rounds)
	var plans []plannedRole
	var capped []string
	var missing []string
	var finished []RoleReview
	var checking []string
	for _, q := range required {
		if q.Missing {
			missing = append(missing, q.Agent)
			continue
		}
		rd := RoundOf(entries, t.Branch, q.Agent, q.Advisory)
		var open []review.Key
		for _, key := range (review.Key{Commit: head, Branch: t.Branch, Agent: q.Agent}).Runs(q.RunCount()) {
			if e, ok := entries[key]; ok && t.Scope.skips(e) && e.Record.Advisory == q.Advisory {
				f := finishedRole(q, r.withReply(t.SID, e), rd.Count, limit)
				f.Run, f.Runs, f.Advisory = key.Number(), q.RunCount(), q.Advisory
				finished = append(finished, f)
				if e.Record.Channel == review.Checking {
					checking = append(checking, ReviewerName(q))
				}
				continue
			}
			open = append(open, key)
		}
		if len(open) == 0 {
			continue
		}
		if !q.Advisory && rd.Capped(limit) {
			capped = append(capped, ReviewerName(q))
			continue
		}
		for _, key := range open {
			p := plannedRole{q: q, base: base, covers: base, round: rd.Count + 1, attempt: nextAttempt(entries, key), run: key.Number(), runs: q.RunCount()}
			if e, ok := entries[key]; ok {
				p.auto = e.Auto
				if e.Record.State != review.Running {
					p.autoAttempts = autoAttemptsOf(e)
				}
			}
			for _, e := range runsOf(entries, key) {
				if e.Record.Override != "" {
					p.kept = e.Record.Override
				}
				if e.Record.State != review.Running {
					p.round = rd.Count
				}
			}
			if prev, previous := r.incremental(ctx, t, entries, q.Agent, head, main, key.Number(), q.Advisory, approval); prev != nil {
				p.base, p.since, p.covers, p.previous = prev.Key.Commit, prev.Key.Commit, prev.Coverage(), previous
			}
			p.overridden = acceptedRound(entries, t.Branch, q.Agent, q.Advisory)
			plans = append(plans, p)
		}
	}
	if len(plans) == 0 && t.Scope == ScopeRetry {
		release()
		return explained(conflict("nothing to retry"), "Every review this commit needs has an outcome, so the engine doesn't run one again.")
	}
	notInstalled := ""
	if len(missing) > 0 {
		notInstalled = "No plugin installed in the reviewer image provides " + strings.Join(missing, " or ") + ", so the engine doesn't start " + plural(len(missing), "that review", "those reviews") + " and the push stays held."
	}
	if len(plans) == 0 && len(capped) == 0 && notInstalled != "" {
		release()
		return explained(conflict("not installed"), notInstalled)
	}
	if len(plans) == 0 && len(capped) == 0 {
		release()
		return explained(conflict("reviewed"), reviewedMessage(head, checking))
	}
	if len(plans) == 0 && len(finished) > 0 {
		release()
		return explained(conflict("capped"), fmt.Sprintf("Every other reviewer this commit needs has a verdict, and %s blocked in %d %s on this branch, the limit this project set, so the engine doesn't start %s again. Decide on the push: override a verdict, allow it with your reason, deny it, or hand the findings off.", strings.Join(capped, " and "), limit, plural(limit, "round", "rounds"), plural(len(capped), "it", "them")))
	}
	if len(plans) == 0 {
		release()
		return explained(conflict("capped"), fmt.Sprintf("Every reviewer this commit needs has blocked in %d %s on this branch, the limit this project set, so the engine doesn't start them again. Decide on the push: override a verdict, allow it with your reason, deny it, or hand the findings off.", limit, plural(limit, "round", "rounds")))
	}
	if t.Auto && autoRuns(entries, t.Branch)+len(plans) > MaxAutoRuns {
		release()
		shared.For(ctx, r.cfg.Logger).Info("review", "event", "refused", "why", "auto-capped")
		return explained(conflict("auto-capped"), fmt.Sprintf("The engine already ran %d automatic reviews on this branch, its limit, so it doesn't start more on the agent's request. Press Review to start them yourself.", MaxAutoRuns))
	}
	if !r.growRuns(t.SID, len(plans)) {
		release()
		return explained(limited("reviews"), fmt.Sprintf("This review needs %d reviewer runs, and with the runs already going the engine would pass its limit of %d at once. Wait for one to finish.", len(plans), MaxReviewerRuns))
	}
	signIn, err := r.CheckSignIn(ctx)
	if err != nil {
		release()
		return err
	}
	if err := judge("The reviewers' Claude Code", signIn, r.cfg.Expected.AdoptChecked(ReviewerAgent, signIn), ReviewerSignInStep(), t.CostOK, "the review"); err != nil {
		release()
		shared.For(ctx, r.cfg.Logger).Info("review", "event", "refused", "why", err.Error())
		return err
	}
	if ctx.Err() != nil {
		release()
		return planStopped()
	}
	view := ReviewView{Commit: head, Base: base, Branch: t.Branch, Running: true, Note: "Checking the reviewers' sandbox…", SignIn: &signIn, SessionCostUSD: r.branchCost(t.Branch), Req: t.Req}
	for _, p := range plans {
		view.Roles = append(view.Roles, RoleReview{Role: p.q.Role, Name: ReviewerName(p.q), Agent: p.q.Agent, State: review.Running, Why: p.q.Why,
			Round: p.round, Limit: limit, Since: p.since, Previous: p.previous != "", Override: p.kept, Run: p.run, Runs: p.runs, Advisory: p.q.Advisory})
	}
	view.Roles = append(view.Roles, finished...)
	held := ""
	if len(capped) > 0 {
		held = strings.Join(capped, " and ") + " blocked in " + strconv.Itoa(limit) + " " + plural(limit, "round", "rounds") + " on this branch, the limit this project set, so the engine didn't start " + plural(len(capped), "it", "them") + " again: you decide on the push."
		view.Note += " " + held
	}
	if notInstalled != "" {
		held = strings.TrimSpace(held + " " + notInstalled)
		view.Note += " " + notInstalled
	}
	r.show(t.SID, view)
	shared.For(ctx, r.cfg.Logger).Info("review", "event", "started", "roles", len(plans), "capped", len(capped))
	r.began()
	r.wg.Go(func() {
		r.run(t, head, r.reviewerVersion(shared.WithRequest(r.cfg.Base, t.Req)), plans, plugin, view, held, given)
		r.ended()
		release()
		if r.cfg.Base.Err() == nil {
			r.finished(t.SID, head)
		}
	})
	return nil
}

func reviewedMessage(head string, checking []string) string {
	out := "Every reviewer this commit needs already has a verdict on commit " + shortSHA(head) + ", so Review runs none of them again. Press Run all again for fresh verdicts."
	if len(checking) > 0 {
		out = "Every reviewer this commit needs already has a verdict on commit " + shortSHA(head) + ", so Review runs none of them again; the engine is checking the " + strings.Join(checking, " and ") + " after an update, and " + plural(len(checking), "its pass counts", "their passes count") + " once the check ends. Press Run all again for fresh verdicts."
	}
	return out
}

type plannedRole struct {
	q            workflow.Requirement
	base         string
	since        string
	covers       string
	previous     string
	overridden   *ReviewEntry
	kept         string
	round        int
	auto         bool
	attempt      int
	autoAttempts int
	run          int
	runs         int
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func (r *Reviews) incremental(ctx context.Context, t ReviewTarget, entries map[review.Key]ReviewEntry, agent, head, main string, run int, advisory bool, approval ApprovalGiven) (*ReviewEntry, string) {
	prev := previousRound(entries, t.Branch, agent, head, run, advisory)
	if prev == nil || prev.Record.Shaped != "" || approval.differs(*prev) {
		return nil, ""
	}
	previous, cut := r.ReplyOf(t.SID, *prev)
	unsure := previous == prev.Message && len(previous) >= maxReviewText-4
	if prev.Record.State == review.Block && (previous == "" || cut > 0 || unsure) {
		return nil, ""
	}
	in, err := r.cfg.History.IsAncestor(ctx, t.Repo, prev.Key.Commit, head)
	if err != nil || !in {
		return nil, ""
	}
	if coverProblem(ctx, r.cfg.History, t.Repo, prev.Coverage(), head, main) != "" {
		return nil, ""
	}
	return prev, previous
}

func coverProblem(ctx context.Context, h History, repo, base, head, main string) string {
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
		in, err := h.IsAncestor(ctx, repo, base, c.commit)
		if err != nil {
			return "the engine couldn't check that the review's base " + shortSHA(base) + " is in " + c.name
		}
		if !in {
			return "the review compared with " + shortSHA(base) + ", which " + c.name + " doesn't contain now"
		}
	}
	return ""
}

func planStopped() error {
	return explained(conflict("stopped"), "No review started: the request ended while the engine planned it. Press Review again.")
}
