package session

import "github.com/bezmoradi/software-factory/internal/core/workflow"

const (
	DocRequirements = "requirements"
	DocPlan         = "plan"
	DocRootCause    = "root-cause"
)

func DocKind(requirements bool, effective workflow.Profile) string {
	switch {
	case requirements:
		return DocRequirements
	case effective == workflow.Incident:
		return DocRootCause
	}
	return DocPlan
}

func DocStatus(kind string, d WorkDoc) string {
	reqs, cause := kind == DocRequirements, kind == DocRootCause
	what, What, them := "the plan", "The plan", "it"
	switch {
	case reqs:
		what, What, them = "the requirements", "The requirements", "them"
	case cause:
		what, What = "the root cause", "The root cause"
	}
	pick := func(plural, single string) string {
		if reqs {
			return plural
		}
		return single
	}
	noLonger := ""
	if d.Changed {
		noLonger = ", so your earlier approval no longer counts"
	}
	switch {
	case d.OnBase:
		return What + " " + pick("are", "is") + " from an earlier task, already on the base branch, so " + pick("they aren’t", "it isn’t") + " approvable here. The engine counts only text this session’s own commits added or changed" + noLonger + "."
	case d.Reported:
		return What + " " + pick("aren’t", "isn’t") + " committed on the session’s branch: this is the text the agent reported. The engine counts an approval only of committed text" + noLonger + "."
	case d.Approved:
		if d.ApprovedAt != "" {
			return "You approved this version of " + what + ", read from commit " + shortSHA7(d.ApprovedAt) + "."
		}
		return "You approved this version of " + what + "."
	case d.Changed:
		if d.ApprovedAt != "" && d.Commit != "" {
			return What + " in commit " + shortSHA7(d.Commit) + " " + pick("differ", "differs") + " from the version you approved in commit " + shortSHA7(d.ApprovedAt) + ", so that approval no longer counts. Read " + them + " and approve again."
		}
		return What + " changed after you approved " + them + ". Read " + them + " and approve again."
	case reqs:
		return "Plan starts once you read the agreed requirements and approve them."
	case cause:
		return "Fix starts once you read the confirmed root cause and approve it. Approving it lets this branch push code; an edit to the file clears it."
	}
	return "Implement starts once you read the plan and approve it."
}
