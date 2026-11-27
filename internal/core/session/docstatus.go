package session

import "github.com/bezmoradi/software-factory/internal/core/workflow"

const (
	DocRequirements = "requirements"
	DocPlan         = "plan"
	DocRootCause    = "root-cause"
)

func DocKind(requirements bool, profile workflow.Profile) string {
	switch {
	case requirements:
		return DocRequirements
	case profile == workflow.Incident:
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
	switch {
	case d.Approved && d.Unread:
		why := "the file isn't in the session's documents folder"
		if d.UnreadWhy != "" {
			why = "the file " + d.UnreadWhy
		}
		return "You approved this version of " + what + ". The engine kept its own copy, which is what counts and what this shows: it couldn't read the current file, since " + why + "."
	case d.Approved:
		return "You approved this version of " + what + "; the engine keeps its own copy of it."
	case d.Changed:
		return What + " changed after you approved " + them + ", so that approval no longer counts. Read " + them + " and approve again."
	case reqs:
		return "The next step starts once you read the agreed requirements and approve them."
	case cause:
		return "Fix starts once you read the confirmed root cause and approve it. Approving it lets this branch push code; an edit to the file clears it."
	}
	return "Implement starts once you read the plan and approve it."
}
