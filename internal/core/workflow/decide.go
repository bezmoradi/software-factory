package workflow

import (
	"strconv"
	"strings"
)

type Facts struct {
	Profile       Profile
	PlanChosen    bool
	Head          string
	PlanFile      string
	ReqHash       string
	ReqBaseline   string
	PlanHash      string
	CauseStatus   string
	ReqOk         bool
	PlanOk        bool
	UsesArtifacts bool
	Artifacts     []string
	ArtifactsOnly bool
	Brief         BriefCheck
	Untyped       bool
	Offered       []string
}

type ActionKind string

const (
	StartDefine         ActionKind = "define"
	StartPlan           ActionKind = "plan"
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
	order    = "In a Code change the developer approves the requirements first, and chooses there whether a plan follows, which they then approve too, before Implement."
	noDetour = "Don't make the change another way, such as through a shell command: the gate checks the working tree when the plan is approved and before every push."
)

func Decide(f Facts, a Action) string {
	change := f.Profile == Change
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
		return briefHold(f.Brief)
	case StartPlan:
		switch {
		case change && !f.ReqOk:
			return "Plan can't start until the developer approves the requirements on the software factory page. " + order
		case change && !f.PlanChosen:
			return "Plan doesn't run: the developer approved the requirements without a plan. To plan, they press Add a plan on the approved requirements, or, once work is committed, approve changed requirements with a plan."
		}
		return ""
	case StartImplement:
		switch {
		case change && !f.ReqOk:
			return "Implement can't start until the developer approves the requirements on the software factory page. " + order
		case change && f.PlanChosen && !f.PlanOk:
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

func briefHold(c BriefCheck) string {
	switch c.Kind {
	case BriefBlocked:
		if len(c.Blocking) == 1 {
			return "Can't approve yet: " + listed(c.Blocking, 8) + " is a blocking open question. Answer it with Claude, then approve."
		}
		return "Can't approve yet: " + listed(c.Blocking, 8) + " are blocking open questions. Answer them with Claude, then approve."
	case BriefInvalid:
		return "Can't approve yet: " + c.Problem + "."
	case BriefNone:
		return "Written before the checked format; ask Claude to rewrite it."
	}
	return ""
}

func listed(ids []string, most int) string {
	shown := make([]string, 0, min(len(ids), most))
	for _, id := range ids[:min(len(ids), most)] {
		shown = append(shown, clip(id, 40))
	}
	switch {
	case len(ids) > most:
		return strings.Join(shown, ", ") + " and " + strconv.Itoa(len(ids)-most) + " more"
	case len(shown) < 2:
		return strings.Join(shown, "")
	}
	return strings.Join(shown[:len(shown)-1], ", ") + " and " + shown[len(shown)-1]
}

func approvePlan(f Facts, a Action) string {
	if f.Profile == Incident {
		return approveCause(f, a)
	}
	if f.Profile != Change {
		return ""
	}
	if !f.ReqOk || f.ReqBaseline == "" {
		return "The plan wasn't approved: approve the requirements first."
	}
	if !f.PlanChosen {
		return "The plan wasn't approved: you approved the requirements without a plan. To plan, press Add a plan on the approved requirements, or, once work is committed, approve changed requirements with a plan."
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

func approveCause(f Facts, a Action) string {
	switch {
	case a.Hash != f.PlanHash:
		return "The root cause changed before your approval arrived, so it wasn't approved. Read the new version and approve it again."
	case f.CauseStatus != Confirmed:
		status := clip(f.CauseStatus, 40)
		if status == "" {
			status = "missing"
		}
		return "The root cause wasn't approved: the Status in " + RootCauseFile + " is " + status + ", and only a " + Confirmed + " diagnosis can be approved. Press Continue on the Root cause step, or give the evidence the file asks for."
	case f.Head == "":
		return "The root cause wasn't approved: the current commit couldn't be read."
	}
	return ""
}

func editHold(f Facts, path string) string {
	switch {
	case f.Untyped:
		return untypedHold(path, f.Offered)
	case f.Profile == CodeReview:
		return "This session's work type is Code review, which doesn't change files: " + path + " stays as it is. To fix what the review found, the developer picks Code change on the software factory page. " + noDetour
	case f.Profile != Change || f.ReqOk && (!f.PlanChosen || f.PlanOk) || (f.UsesArtifacts && artifactPath(path, f.Artifacts)):
		return ""
	}
	needs := "the plan, which the developer chose, needs"
	if !f.ReqOk {
		needs = "the requirements, and then the plan if the developer chooses one, need"
	}
	until := "Write notes, the requirements brief and the plan in your scratchpad, not in the repository."
	if f.UsesArtifacts && len(f.Artifacts) > 0 {
		until = "Until then only this step's own files can change: " + strings.Join(f.Artifacts[:min(len(f.Artifacts), 8)], ", ") + "."
	}
	return path + " can't change yet: " + needs + " the developer's approval on the software factory page first. " + order + " " + until + " " + noDetour
}

func untypedHold(path string, offered []string) string {
	ids := make([]string, 0, min(len(offered), 12))
	for _, id := range offered[:min(len(offered), 12)] {
		ids = append(ids, clip(id, 40))
	}
	return path + " can't change yet: this session has no work type, and the work type decides which steps and reviews apply. Suggest one with the factory_work_type tool, giving type, one of " + strings.Join(ids, ", ") + ", and reason, one line saying why it fits. Then end your turn: the developer picks the work type on the software factory page, and your next message says when you can go on. " + noDetour
}

type Build struct {
	Profile     Profile
	Head        bool
	ReqOk       bool
	PlanChosen  bool
	PlanOk      bool
	Built       bool
	Commits     int
	CommitsRead bool
	NoBase      bool
}

func ImplementWait(w Build) string {
	switch {
	case w.Profile == CodeReview || w.Profile == Explore:
		return ""
	case !w.Head:
		return "the gate couldn't read the current commit"
	case w.Profile == Incident && !w.PlanOk:
		return "approve the root cause first"
	case w.Profile == Incident && !w.Built:
		return "commit the fix made since the root cause was approved"
	case w.Profile == Change && !w.ReqOk:
		return "approve the requirements first"
	case w.Profile == Change && w.PlanChosen && !w.PlanOk:
		return "approve the plan first"
	case w.Profile == Change && w.Built:
		return ""
	case w.Profile == Change && w.PlanChosen:
		return "commit the work made since the plan was approved"
	case w.Profile == Change:
		return "commit the work made since the requirements were approved"
	case w.NoBase:
		return ""
	case !w.CommitsRead:
		return "the gate couldn't read this branch's commits"
	case w.Commits > 0:
		return ""
	}
	return "commit the work on this branch first"
}
