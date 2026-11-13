package workflow

import (
	"strings"
)

type Change struct {
	Path   string
	Status string
}

type Facts struct {
	Profile       Profile
	Head          string
	PlanFile      string
	PlanCommand   string
	ReqHash       string
	ReqBaseline   string
	PlanHash      string
	ReqOk         bool
	PlanOk        bool
	UsesArtifacts bool
	Artifacts     []string
	ArtifactsOnly bool
}

type ActionKind string

const (
	StartDefine         ActionKind = "define"
	StartPlan           ActionKind = "plan"
	StartTasks          ActionKind = "tasks"
	StartImplement      ActionKind = "implement"
	ApproveRequirements ActionKind = "approve-requirements"
	ApprovePlan         ActionKind = "approve-plan"
	EditFile            ActionKind = "edit"
)

type Action struct {
	Kind ActionKind
	Hash string
	Path string
}

const (
	order    = "In a Feature the steps run in order: Define, Plan, Implement, Code review, Deliver."
	noDetour = "Don't make the change another way, such as through a shell command: the gate checks the working tree when the plan is approved and before every push."
)

func Decide(f Facts, a Action) string {
	feature := f.Profile == Feature
	switch a.Kind {
	case StartDefine:
		return ""
	case ApproveRequirements:
		switch {
		case f.ReqHash == "":
			return "There are no agreed requirements to approve yet."
		case a.Hash != f.ReqHash:
			return "The requirements changed before your approval arrived. Read them again and approve this version."
		case f.Head == "":
			return "The requirements weren't approved: the current commit couldn't be read."
		}
		return ""
	case StartPlan:
		if feature && !f.ReqOk {
			return "Plan can't start until the developer approves the requirements on the software factory page. " + order
		}
		return ""
	case StartTasks:
		switch {
		case feature && !f.ReqOk:
			return "Tasks can't start until the developer approves the requirements on the software factory page, and Plan has written the plan. " + order
		case feature && f.PlanFile == "":
			with := ""
			if f.PlanCommand != "" {
				with = " with " + f.PlanCommand
			}
			return "Tasks can't start before Plan has written the plan" + with + ". " + order
		case feature && f.PlanOk:
			return "Tasks can't run again: the developer approved the plan and its tasks together, and writing the tasks again would cancel that approval. " + order
		}
		return ""
	case StartImplement:
		if feature && !f.PlanOk {
			return "Implement can't start until the developer approves the plan on the software factory page. " + order
		}
		return ""
	case ApprovePlan:
		return approvePlan(f, a)
	case EditFile:
		return editHold(f, a.Path)
	}
	return ""
}

func approvePlan(f Facts, a Action) string {
	if f.Profile != Feature {
		return ""
	}
	if !f.ReqOk || f.ReqBaseline == "" {
		return "The plan wasn't approved: approve the requirements first."
	}
	if a.Hash != f.PlanHash {
		return "The plan changed before your approval arrived, so it wasn't approved. Read the new version and approve it again."
	}
	if f.Head == "" || (f.Head != f.ReqBaseline && !f.ArtifactsOnly) {
		more := ""
		if f.UsesArtifacts {
			more = ", and they change more than the plan's own files"
		}
		return "The plan wasn't approved: there are commits since the requirements were approved, at " + f.ReqBaseline[:min(len(f.ReqBaseline), 7)] + more + ". Nothing is built before the plan is approved, so remove those commits or approve the requirements again from here."
	}
	return ""
}

func editHold(f Facts, path string) string {
	switch {
	case f.Profile == CodeReview:
		return "This session's work type is Code review, which doesn't change files: " + path + " stays as it is. To fix what the review found, the developer picks Fix on the software factory page. " + noDetour
	case f.Profile == Docs && !DocFile(path):
		return "This session's work type is Docs only, which changes documentation only, and " + path + " isn't documentation. Pick Fix or Feature on the software factory page to change it. " + noDetour
	case f.Profile != Feature || f.PlanOk || (f.UsesArtifacts && artifactPath(path, f.Artifacts)):
		return ""
	}
	needs := "the plan needs"
	if !f.ReqOk {
		needs = "the requirements and then the plan need"
	}
	until := "Write notes, the requirements brief and the plan in your scratchpad, not in the repository."
	switch {
	case f.UsesArtifacts && len(f.Artifacts) > 0:
		until = "Until then only this step's own files can change: " + strings.Join(f.Artifacts[:min(len(f.Artifacts), 8)], ", ") + "."
	case f.UsesArtifacts:
		until = "Until then only this step's own files can change, once Spec Kit has named the feature's folder."
	}
	return path + " can't change yet: " + needs + " the developer's approval on the software factory page first. " + order + " " + until + " " + noDetour
}

type Build struct {
	Profile     Profile
	Head        bool
	PlanOk      bool
	Built       bool
	Commits     int
	CommitsRead bool
	NoBase      bool
}

func ImplementWait(w Build) string {
	switch {
	case w.Profile == CodeReview || w.Profile == Exploring:
		return ""
	case !w.Head:
		return "the gate couldn't read the current commit"
	case w.Profile == Feature && !w.PlanOk:
		return "approve the plan first"
	case w.Profile == Feature:
		if w.Built {
			return ""
		}
		return "commit the work made since the plan was approved"
	case w.NoBase:
		return ""
	case !w.CommitsRead:
		return "the gate couldn't read this branch's commits"
	case w.Commits > 0:
		return ""
	}
	return "commit the work on this branch first"
}
