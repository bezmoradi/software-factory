package session

import (
	"context"
	"slices"
	"strings"
)

const (
	errorWorking       = "working"
	errorPushWaits     = "push waits"
	errorPlanApproved  = "plan approved"
	planReviewTooLate  = "The plan is already approved; Code review checks the work now."
	pushNeedsPushType  = "This work type doesn't push: pick one that pushes."
	pushNeedsHead      = "The engine couldn't read the current commit."
	pushRefusedAllow   = "The engine can't push this commit, even with your Allow: the list under Push says why."
	pushNeedsSandbox   = "Push waits for the sandbox check to pass."
	pushNeedsReview    = "Waits for the reviews: press Review."
	pushNeedsVerdicts  = "Waits for the reviews to finish."
	pushNeedsOverride  = "A review blocked this commit: override it, or have the agent fix it."
	implementNotDone   = "Implement isn't done: "
	reviewWaitsWorking = " finishes: reviews start by themselves when it asks to push."
	pushWaitsWorking   = " finishes: it asks to push once its work is done."
)

var noVerdictStates = []string{"none", "stale"}

type PageOffers struct {
	Info func(sid string) (Info, bool)
	View func(ctx context.Context, sid string) (GateView, error)
}

func (o PageOffers) Push(ctx context.Context, sid string) error {
	if o.Info == nil {
		return nil
	}
	i, ok := o.Info(sid)
	switch {
	case !ok:
		return nil
	case o.View == nil && agentWorking(i):
		return conflict(errorWorking, pushWorking(i))
	case o.View == nil:
		return nil
	}
	v, err := o.View(ctx, sid)
	if err != nil {
		return nil
	}
	if code, why := pushWaits(i, v); why != "" {
		return conflict(code, why)
	}
	return nil
}

func (o PageOffers) Review(sid string) error {
	if o.Info == nil {
		return nil
	}
	if i, ok := o.Info(sid); ok && agentWorking(i) {
		return conflict(errorWorking, ReviewWait(i))
	}
	return nil
}

func ReviewWait(i Info) string {
	if !agentWorking(i) {
		return ""
	}
	return "Waits until " + AgentWord(i.Agent) + reviewWaitsWorking
}

func ImplementDone(i Info, v GateView) bool {
	return implementBuilt(v, agentWorking(i), uncommittedAny(i.Uncommitted))
}

func ReviewNext(i Info, v GateView) bool {
	if OfferReview(i, v).Reason != "" || !ImplementDone(i, v) {
		return false
	}
	return slices.ContainsFunc(v.Reviewers, func(r WorkReviewer) bool { return slices.Contains(noVerdictStates, r.State) })
}

func PushWait(i Info, v GateView) string {
	_, why := pushWaits(i, v)
	return why
}

func pushWaits(i Info, v GateView) (string, string) {
	if why := OfferReview(i, v).Reason; why != "" {
		return errorPushWaits, why
	}
	if agentWorking(i) {
		return errorWorking, pushWorking(i)
	}
	return errorPushWaits, v.PushWait
}

func pushWorking(i Info) string {
	return "Waits until " + AgentWord(i.Agent) + pushWaitsWorking
}

func pushWaitOf(v GateView, pushes, refused, unsandboxed bool) string {
	switch {
	case !pushes:
		return pushNeedsPushType
	case v.Head == "":
		return pushNeedsHead
	case refused:
		return pushRefusedAllow
	case v.Waiting != "":
		return implementNotDone + v.Waiting + "."
	case unsandboxed:
		return pushNeedsSandbox
	}
	counted := slices.DeleteFunc(slices.Clone(v.Reviewers), func(r WorkReviewer) bool { return r.State == WorkNotInstalled || r.Capped })
	switch {
	case slices.ContainsFunc(counted, func(r WorkReviewer) bool { return r.State == "block" && r.Override == "" }):
		return pushNeedsOverride
	case slices.ContainsFunc(counted, func(r WorkReviewer) bool { return slices.Contains(noVerdictStates, r.State) }):
		return pushNeedsReview
	case slices.ContainsFunc(counted, func(r WorkReviewer) bool { return r.State == "running" }):
		return pushNeedsVerdicts
	}
	return ""
}

func PlanReviewOffered(v GateView) bool {
	return v.Plan != nil && strings.TrimSpace(v.Plan.Text) != "" && !v.Plan.Approved
}
